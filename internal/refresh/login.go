package refresh

import (
	"bytes"
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

// LoginRotate performs an interactive re-login rotation for every managed
// account whose credentials are missing or within expiryBuffer of expiry.
// For each it invokes `claude auth login --email <email>` so the user can
// complete the OAuth flow in their browser, then stores the resulting
// credential under remote's BackupCredKey(id, email).
//
// On modern Claude Code (2.x) on macOS, `claude` writes the credential
// directly to the login keychain and ignores CLAUDE_CONFIG_DIR for
// credential storage. To capture the new credential portably this function
// reads from two places after each `claude auth login`:
//
//  1. The legacy file path "$CLAUDE_CONFIG_DIR/.credentials.json" (older
//     claude and Linux).
//  2. The active slot of the local backend — keychain on macOS — comparing
//     the post-login bytes to a pre-login snapshot to detect "claude did
//     write something new" (so a user-cancelled login is not stored).
//
// When force is true every selected account is re-authenticated regardless
// of credential state.
//
// Unlike RefreshOne, this function is interactive: it inherits the
// terminal's stdin/stdout/stderr so the user can drive the claude CLI and
// browser flow. It returns the number of accounts successfully refreshed.
func LoginRotate(
	ctx context.Context,
	seq *account.Sequence,
	remote backend.Backend,
	local backend.Backend,
	expiryBuffer time.Duration,
	force bool,
	log *slog.Logger,
) (int, error) {
	if log == nil {
		log = slog.Default()
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return 0, fmt.Errorf("login: claude CLI not found in PATH: %w", err)
	}

	type todo struct {
		id    string
		email string
	}

	// Build the list of accounts needing login.
	var pending []todo
	for _, id := range seq.IDs() {
		acct, ok := seq.Accounts[id]
		if !ok {
			continue
		}
		needsLogin := force
		if !needsLogin {
			data, err := remote.Read(ctx, account.BackupCredKey(id, acct.Email))
			if errors.Is(err, backend.ErrNotFound) || len(data) == 0 {
				needsLogin = true
			} else if err == nil {
				cred, parseErr := credentials.Parse(data)
				if parseErr != nil || cred.IsExpired(expiryBuffer) {
					needsLogin = true
				}
			}
		}
		if needsLogin {
			pending = append(pending, todo{id: id, email: acct.Email})
		}
	}

	if len(pending) == 0 {
		fmt.Println("All accounts have valid credentials.")
		return 0, nil
	}

	fmt.Printf("Found %d account(s) needing login:\n", len(pending))
	for _, t := range pending {
		fmt.Printf("  %s %s\n", t.id, t.email)
	}
	fmt.Println()

	refreshed := 0
	for i, t := range pending {
		fmt.Printf("\n%s\n", separator())
		fmt.Printf("[%d/%d] Logging in: %s (%s)\n", i+1, len(pending), t.id, t.email)
		fmt.Printf("%s\n\n", separator())
		fmt.Printf("Launching `claude auth login --email %s`...\n", t.email)
		fmt.Println("  - You will be prompted to authorize in your browser.")
		fmt.Printf("  - Make sure to log in as: %s\n", t.email)
		fmt.Println("  - claude will exit on its own when the auth flow completes.")
		fmt.Println()

		// Snapshot the local active slot AND the wall-clock so we can
		// distinguish "claude wrote new credentials this iteration" from
		// "claude wrote nothing" (cancelled login). The wall-clock feeds
		// the hashed-slot lookup, which filters by modification date.
		beforeData, _ := local.Read(ctx, account.ActiveCredKey)
		since := time.Now()

		tmpConfig, err := os.MkdirTemp("", "ccswitch-login-config-*")
		if err != nil {
			log.Error("login: create config tmpdir", "id", t.id, "err", err)
			continue
		}
		tmpWork, err := os.MkdirTemp("", "ccswitch-login-work-*")
		if err != nil {
			_ = os.RemoveAll(tmpConfig)
			log.Error("login: create work tmpdir", "id", t.id, "err", err)
			continue
		}

		// Seed a stub .claude.json so claude skips onboarding inside the
		// isolated CLAUDE_CONFIG_DIR. The isolation still keeps the real
		// ~/.claude/.claude.json from being touched even though
		// credentials themselves go to the keychain on macOS.
		_ = os.WriteFile(filepath.Join(tmpConfig, ".claude.json"), []byte(seedJSON), 0o600)

		cmd := exec.CommandContext(ctx, claudePath, "auth", "login", "--email", t.email)
		cmd.Dir = tmpWork
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(filteredEnv(), "CLAUDE_CONFIG_DIR="+tmpConfig)

		_ = cmd.Run() // interactive; ignore exit code

		// LoginRotate does not seed .credentials.json, so any file that
		// appears at the legacy path came from claude.
		newData := captureClaudeCredential(ctx, tmpConfig, nil, local, beforeData, since)

		_ = os.RemoveAll(tmpConfig)
		_ = os.RemoveAll(tmpWork)

		if len(newData) == 0 {
			fmt.Printf("\nNo credentials captured for %s (%s)\n", t.id, t.email)
			log.Warn("login: no credentials captured", "id", t.id, "email", t.email)
			continue
		}

		newCred, err := credentials.Parse(newData)
		if err != nil || newCred.IsExpired(0) {
			fmt.Printf("\nCredentials for %s appear invalid after login\n", t.email)
			log.Warn("login: credentials invalid after claude exit", "id", t.id)
			continue
		}

		// Persist the raw bytes — never a re-marshaled struct — so any field
		// the struct does not model survives.
		key := account.BackupCredKey(t.id, t.email)
		if err := remote.Write(ctx, key, newData); err != nil {
			log.Error("login: write credentials failed", "id", t.id, "err", err)
			fmt.Printf("\nFailed to save credentials for %s\n", t.email)
			continue
		}

		fmt.Printf("\nCredentials saved for %s (%s)\n", t.id, t.email)
		log.Info("login: refreshed", "id", t.id, "email", t.email)
		refreshed++
	}

	fmt.Printf("\n%s\n", separator())
	fmt.Printf("Interactive login complete: %d/%d accounts refreshed.\n", refreshed, len(pending))
	return refreshed, nil
}

// hashedSlotLookup is implemented by backends that can locate the
// "Claude Code-credentials-<hash>" keychain item claude 2.x writes when
// CLAUDE_CONFIG_DIR is set. The keychain backend implements it; other
// backends do not and are simply skipped by the type assertion below.
type hashedSlotLookup interface {
	LookupHashedActiveSlot(ctx context.Context, since time.Time) ([]byte, error)
}

// captureClaudeCredential returns the raw credential blob that `claude auth
// login` (interactive or refresh-token) produced. It tries three locations
// in order:
//
//  1. The legacy file at "$CLAUDE_CONFIG_DIR/.credentials.json" (older
//     claude and Linux). RefreshOne seeds this file with the existing
//     credential before invoking claude, so the helper also takes
//     seedData; the file path only "captures" when claude rewrote it to
//     something different. Pass nil seedData from the LoginRotate path,
//     which never seeds .credentials.json.
//  2. The local backend's active slot ("Claude Code-credentials"). claude
//     can write there on macOS when CLAUDE_CONFIG_DIR is not set, or on
//     Linux into the file the file backend points at. Compared to a
//     pre-run snapshot so a cancelled login does not re-store stale data.
//  3. Claude 2.x's per-CLAUDE_CONFIG_DIR hashed keychain slot — service
//     "Claude Code-credentials-<8hex>". Found by enumeration + a
//     modification-date filter (since), since the hash is opaque to us.
func captureClaudeCredential(ctx context.Context, tmpConfig string, seedData []byte, local backend.Backend, beforeLocal []byte, since time.Time) []byte {
	if data, err := os.ReadFile(filepath.Join(tmpConfig, ".credentials.json")); err == nil && len(data) > 0 {
		if !bytes.Equal(data, seedData) {
			return data
		}
	}
	if local == nil {
		return nil
	}
	if data, err := local.Read(ctx, account.ActiveCredKey); err == nil && len(data) > 0 && !bytes.Equal(data, beforeLocal) {
		return data
	}
	if lookup, ok := local.(hashedSlotLookup); ok {
		if data, err := lookup.LookupHashedActiveSlot(ctx, since); err == nil && len(data) > 0 {
			return data
		}
	}
	return nil
}

func separator() string {
	return "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}
