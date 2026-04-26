package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/credentials"
)

// LoginRotate performs an interactive re-login rotation (mirrors cmd_login in
// ccswitch.sh). For each account whose credentials are missing or within
// expiryBuffer of expiry, it:
//
//  1. Prints which account it is about to refresh.
//  2. Launches `claude` interactively inside an isolated CLAUDE_CONFIG_DIR so
//     the user can authenticate via their browser.
//  3. After claude exits, reads .credentials.json from the isolated dir and
//     writes it to b under the account's key.
//
// Unlike RefreshOne, this function is interactive: it inherits the terminal's
// stdin/stdout/stderr so the user can interact with the claude CLI.
// It returns the number of accounts successfully refreshed.
func LoginRotate(
	ctx context.Context,
	seq *account.Sequence,
	b backend.Backend,
	expiryBuffer time.Duration,
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

	// Build list of accounts needing login.
	var pending []todo
	for _, id := range seq.IDs() {
		acct, ok := seq.Accounts[id]
		if !ok {
			continue
		}
		key := account.BackupCredKey(id, acct.Email)
		data, err := b.Read(ctx, key)
		needsLogin := false
		if errors.Is(err, backend.ErrNotFound) || len(data) == 0 {
			needsLogin = true
		} else if err == nil {
			cred, parseErr := credentials.Parse(data)
			if parseErr != nil || cred.IsExpired(expiryBuffer) {
				needsLogin = true
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
		fmt.Printf("Launching claude for interactive login as %s...\n", t.email)
		fmt.Println("  - You will be prompted to log in via your browser")
		fmt.Printf("  - Make sure to log in as: %s\n", t.email)
		fmt.Println("  - Type /exit or press Ctrl+D when done")
		fmt.Println()

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

		// Seed a stub .claude.json so onboarding is skipped.
		_ = os.WriteFile(
			fmt.Sprintf("%s/.claude.json", tmpConfig),
			[]byte(seedJSON),
			0o600,
		)

		cmd := exec.CommandContext(ctx, claudePath)
		cmd.Dir = tmpWork
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(filteredEnv(), "CLAUDE_CONFIG_DIR="+tmpConfig)

		_ = cmd.Run() // interactive; ignore exit code

		// Capture credentials written by claude.
		credFile := fmt.Sprintf("%s/.credentials.json", tmpConfig)
		newData, readErr := os.ReadFile(credFile)

		_ = os.RemoveAll(tmpConfig)
		_ = os.RemoveAll(tmpWork)

		if readErr != nil {
			fmt.Printf("\nNo credentials captured for %s (%s)\n", t.id, t.email)
			log.Warn("login: no credentials file after claude exit", "id", t.id, "err", readErr)
			continue
		}

		newCred, err := credentials.Parse(newData)
		if err != nil || newCred.IsExpired(0) {
			fmt.Printf("\nCredentials for %s appear invalid after login\n", t.email)
			log.Warn("login: credentials invalid after claude exit", "id", t.id)
			continue
		}

		key := account.BackupCredKey(t.id, t.email)
		if err := b.Write(ctx, key, newData); err != nil {
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

func separator() string {
	return "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}
