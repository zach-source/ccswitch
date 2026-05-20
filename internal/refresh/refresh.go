// Package refresh implements non-interactive OAuth token refresh via the
// `claude auth login` command.  The refresh token is delivered exclusively
// through the CLAUDE_CODE_OAUTH_REFRESH_TOKEN environment variable — never
// via argv — to avoid leaking it into process listings.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/credentials"
)

// oauthScopes mirrors the default scopes used by Claude Code.
const oauthScopes = "openid,profile,email,offline_access"

// seedJSON is written to the isolated config dir so that `claude auth login`
// sees a plausible existing installation and skips interactive onboarding.
const seedJSON = `{"hasCompletedOnboarding":true}`

// RefreshOne exchanges the refresh token in cred for a fresh credential blob.
//
// It returns the credential file's *raw bytes* exactly as `claude auth login`
// wrote them, alongside a parsed view for inspection. Callers must persist the
// raw bytes — never re-marshal the parsed struct — so that any field the
// struct does not model (future additions to .credentials.json) is preserved.
//
// local is the active-slot backend (keychain on macOS, file elsewhere). On
// macOS, claude 2.x writes the refreshed credential directly to the keychain
// regardless of CLAUDE_CONFIG_DIR; RefreshOne reads from local as a fallback
// when the legacy file is absent. A nil local disables the fallback (mostly
// useful for tests on Linux paths).
//
// Strategy:
//  1. Create an isolated CLAUDE_CONFIG_DIR (tmpdir).
//  2. Seed it with the existing credential blob and a stub .claude.json.
//  3. Snapshot local's active slot so we can detect "claude updated it".
//  4. Run `claude auth login` with the refresh token in the environment.
//  5. Read $CLAUDE_CONFIG_DIR/.credentials.json — raw — if present;
//     otherwise read local's active slot and compare to the snapshot.
//  6. Clean up tmpdirs on exit regardless of success.
func RefreshOne(ctx context.Context, cred *credentials.Credentials, local backend.Backend) ([]byte, *credentials.Credentials, error) {
	refreshToken := cred.ClaudeAIOAuth.RefreshToken
	if refreshToken == "" {
		return nil, nil, errors.New("refresh: no refresh token present in credentials")
	}

	// Isolated config dir — never touches the active keychain/session.
	tmpConfig, err := os.MkdirTemp("", "ccswitch-refresh-config-*")
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: create config tmpdir: %w", err)
	}
	tmpWork, err := os.MkdirTemp("", "ccswitch-refresh-work-*")
	if err != nil {
		_ = os.RemoveAll(tmpConfig)
		return nil, nil, fmt.Errorf("refresh: create work tmpdir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpConfig)
		_ = os.RemoveAll(tmpWork)
	}()

	// Seed credentials so claude can read the existing token. This seed is
	// a throwaway — `claude auth login` overwrites credFile, and step 4
	// reads back that overwritten file — so re-marshaling here is harmless.
	credData, err := cred.Marshal()
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: marshal existing cred: %w", err)
	}
	credFile := filepath.Join(tmpConfig, ".credentials.json")
	if err := os.WriteFile(credFile, credData, 0o600); err != nil {
		return nil, nil, fmt.Errorf("refresh: seed credentials: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpConfig, ".claude.json"), []byte(seedJSON), 0o600); err != nil {
		return nil, nil, fmt.Errorf("refresh: seed claude.json: %w", err)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: claude CLI not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, claudePath, "auth", "login")
	cmd.Dir = tmpWork
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Env = append(filteredEnv(),
		"CLAUDE_CONFIG_DIR="+tmpConfig,
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN="+refreshToken,
		"CLAUDE_CODE_OAUTH_SCOPES="+oauthScopes,
	)

	// Snapshot the local active slot AND the wall-clock so a post-run
	// change can be attributed to claude (rather than re-reading a stale
	// prior value as if it were fresh). The wall-clock feeds the hashed-
	// slot lookup, which filters by modification date.
	var beforeActive []byte
	if local != nil {
		beforeActive, _ = local.Read(ctx, account.ActiveCredKey)
	}
	since := time.Now()

	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("refresh: claude auth login: %w", err)
	}

	// Capture the raw bytes — this is what gets persisted verbatim. The
	// parse below is only an inspection lens. credData (the seed we wrote
	// to credFile above) lets capture distinguish "claude rewrote the
	// file" from "the seed is still there."
	newData, hashedSvc := captureClaudeCredential(ctx, tmpConfig, credData, local, beforeActive, since)
	if len(newData) == 0 {
		return nil, nil, fmt.Errorf("refresh: claude auth login did not produce credentials")
	}
	// claude wrote the credential to a per-CLAUDE_CONFIG_DIR hashed keychain
	// slot; we have the bytes now, so delete that throwaway record to keep it
	// from accumulating. Best-effort.
	if hashedSvc != "" && local != nil {
		_ = local.Delete(ctx, hashedSvc)
	}
	newCred, err := credentials.Parse(newData)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: parse refreshed credentials: %w", err)
	}
	return newData, newCred, nil
}

// RefreshAll iterates every account in seq, refreshes those whose credentials
// are within expiryBuffer of expiry, and writes the result to b.
//
// It returns the number of accounts successfully refreshed. If one or more
// accounts hit a hard error (read/parse/refresh/write failure) it also
// returns a non-nil error so a non-interactive caller — a cron or launchd
// wrapper — can detect the partial failure via a non-zero exit code. An
// account with no stored credentials is a skip, not a failure.
func RefreshAll(
	ctx context.Context,
	seq *account.Sequence,
	b backend.Backend,
	local backend.Backend,
	expiryBuffer time.Duration,
	log *slog.Logger,
) (int, error) {
	if log == nil {
		log = slog.Default()
	}

	refreshed := 0
	failed := 0
	for _, id := range seq.IDs() {
		acct, ok := seq.Accounts[id]
		if !ok {
			continue
		}

		key := account.BackupCredKey(id, acct.Email)
		data, err := b.Read(ctx, key)
		if err != nil {
			if errors.Is(err, backend.ErrNotFound) {
				log.Warn("refresh-all: no credentials for account", "id", id, "email", acct.Email)
				continue
			}
			log.Error("refresh-all: read failed", "id", id, "err", err)
			failed++
			continue
		}

		cred, err := credentials.Parse(data)
		if err != nil {
			log.Error("refresh-all: parse failed", "id", id, "err", err)
			failed++
			continue
		}

		if !cred.IsExpired(expiryBuffer) {
			log.Info("refresh-all: token still valid", "id", id, "hours_left", fmt.Sprintf("%.1f", cred.HoursLeft()))
			continue
		}

		log.Info("refresh-all: refreshing", "id", id, "email", acct.Email)
		newData, newCred, err := RefreshOne(ctx, cred, local)
		if err != nil {
			log.Error("refresh-all: refresh failed", "id", id, "err", err)
			failed++
			continue
		}

		// Persist the raw credential bytes verbatim — no struct round-trip.
		if err := b.Write(ctx, key, newData); err != nil {
			log.Error("refresh-all: write failed", "id", id, "err", err)
			failed++
			continue
		}

		log.Info("refresh-all: refreshed", "id", id, "email", acct.Email, "hours_left", fmt.Sprintf("%.1f", newCred.HoursLeft()))
		refreshed++
	}

	if failed > 0 {
		return refreshed, fmt.Errorf("refresh-all: %d account(s) failed to refresh", failed)
	}
	return refreshed, nil
}

// filteredEnv returns os.Environ() with CLAUDE_CONFIG_DIR and
// CLAUDE_CODE_OAUTH_REFRESH_TOKEN stripped so our injected values take effect.
func filteredEnv() []string {
	blocked := map[string]bool{
		"CLAUDE_CONFIG_DIR":               true,
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN": true,
		"CLAUDE_CODE_OAUTH_SCOPES":        true,
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !blocked[key] {
			out = append(out, kv)
		}
	}
	return out
}
