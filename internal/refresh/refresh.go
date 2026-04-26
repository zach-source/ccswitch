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
// Strategy (mirrors _refresh_via_claude_auth in ccswitch.sh):
//  1. Create an isolated CLAUDE_CONFIG_DIR (tmpdir).
//  2. Seed it with the existing credential blob and a stub .claude.json.
//  3. Run `claude auth login` with the refresh token in the environment.
//  4. Wait for .credentials.json to be (re-)written, parse it, return it.
//  5. Clean up tmpdirs on exit regardless of success.
func RefreshOne(ctx context.Context, cred *credentials.Credentials) (*credentials.Credentials, error) {
	refreshToken := cred.ClaudeAIOAuth.RefreshToken
	if refreshToken == "" {
		return nil, errors.New("refresh: no refresh token present in credentials")
	}

	// Isolated config dir — never touches the active keychain/session.
	tmpConfig, err := os.MkdirTemp("", "ccswitch-refresh-config-*")
	if err != nil {
		return nil, fmt.Errorf("refresh: create config tmpdir: %w", err)
	}
	tmpWork, err := os.MkdirTemp("", "ccswitch-refresh-work-*")
	if err != nil {
		_ = os.RemoveAll(tmpConfig)
		return nil, fmt.Errorf("refresh: create work tmpdir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpConfig)
		_ = os.RemoveAll(tmpWork)
	}()

	// Seed credentials so claude can read the existing token.
	credData, err := cred.Marshal()
	if err != nil {
		return nil, fmt.Errorf("refresh: marshal existing cred: %w", err)
	}
	credFile := filepath.Join(tmpConfig, ".credentials.json")
	if err := os.WriteFile(credFile, credData, 0o600); err != nil {
		return nil, fmt.Errorf("refresh: seed credentials: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpConfig, ".claude.json"), []byte(seedJSON), 0o600); err != nil {
		return nil, fmt.Errorf("refresh: seed claude.json: %w", err)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("refresh: claude CLI not found in PATH: %w", err)
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

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("refresh: claude auth login: %w", err)
	}

	// Read the freshly written credentials.
	newData, err := os.ReadFile(credFile)
	if err != nil {
		return nil, fmt.Errorf("refresh: read refreshed credentials: %w", err)
	}
	newCred, err := credentials.Parse(newData)
	if err != nil {
		return nil, fmt.Errorf("refresh: parse refreshed credentials: %w", err)
	}
	return newCred, nil
}

// RefreshAll iterates every account in seq, refreshes those whose credentials
// are within expiryBuffer of expiry, and writes the result to b.
// It returns the number of accounts successfully refreshed and any non-fatal
// per-account errors logged via log.
func RefreshAll(
	ctx context.Context,
	seq *account.Sequence,
	b backend.Backend,
	expiryBuffer time.Duration,
	log *slog.Logger,
) (int, error) {
	if log == nil {
		log = slog.Default()
	}

	refreshed := 0
	for _, id := range seq.IDs() {
		acct, ok := seq.Accounts[id]
		if !ok {
			continue
		}

		key := credKey(id, acct.Email)
		data, err := b.Read(ctx, key)
		if err != nil {
			if errors.Is(err, backend.ErrNotFound) {
				log.Warn("refresh-all: no credentials for account", "id", id, "email", acct.Email)
				continue
			}
			log.Error("refresh-all: read failed", "id", id, "err", err)
			continue
		}

		cred, err := credentials.Parse(data)
		if err != nil {
			log.Error("refresh-all: parse failed", "id", id, "err", err)
			continue
		}

		if !cred.IsExpired(expiryBuffer) {
			log.Info("refresh-all: token still valid", "id", id, "hours_left", fmt.Sprintf("%.1f", cred.HoursLeft()))
			continue
		}

		log.Info("refresh-all: refreshing", "id", id, "email", acct.Email)
		newCred, err := RefreshOne(ctx, cred)
		if err != nil {
			log.Error("refresh-all: refresh failed", "id", id, "err", err)
			continue
		}

		newData, err := newCred.Marshal()
		if err != nil {
			log.Error("refresh-all: marshal failed", "id", id, "err", err)
			continue
		}
		if err := b.Write(ctx, key, newData); err != nil {
			log.Error("refresh-all: write failed", "id", id, "err", err)
			continue
		}

		log.Info("refresh-all: refreshed", "id", id, "email", acct.Email, "hours_left", fmt.Sprintf("%.1f", newCred.HoursLeft()))
		refreshed++
	}

	return refreshed, nil
}

// credKey returns the backend key for (id, email) — matches sync package convention.
func credKey(id, email string) string {
	return fmt.Sprintf("ccswitch - %s-%s", id, email)
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
		key := kv
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				key = kv[:i]
				break
			}
		}
		if !blocked[key] {
			out = append(out, kv)
		}
	}
	return out
}
