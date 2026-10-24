package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/codex"
)

// codexTokenRenewBefore is how long before the access token expires that
// ccswitch renews a Codex token session.
const codexTokenRenewBefore = 24 * time.Hour

// codexTokenLockWait is how long a command waits for the session lock.
const codexTokenLockWait = 30 * time.Second

func codexTokenKey(email string) string { return "Codex-Token-" + strings.ToLower(email) }

// lockFileWait is lockFile that waits for the lock for up to wait.
func lockFileWait(name string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		unlock, locked, err := lockFile(name)
		if err != nil {
			return nil, err
		}
		if locked {
			return unlock, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another ccswitch process holds %s", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// codexTokenLocal returns the local backend, or nil when it is not available.
func codexTokenLocal() backend.Backend {
	local, err := resolveLocalBackend()
	if err != nil {
		return nil
	}
	return local
}

// saveCodexToken writes the session to the store. When that fails it writes
// the local copy and warns. When both fail it warns that the session is lost.
func saveCodexToken(ctx context.Context, store, local backend.Backend, email string, blob []byte) {
	key := codexTokenKey(email)
	storeErr := store.Write(ctx, key, blob)
	if storeErr == nil {
		return
	}
	if local != nil && local != store {
		if localErr := local.Write(ctx, key, blob); localErr == nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save the Codex token session for %s to the store (%v); saved a local copy.\n", email, storeErr)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "Warning: could not save the renewed Codex token session for %s (%v). "+
		"The old refresh token is now spent: run `ccswitch token create --codex %s` again.\n", email, storeErr, email)
}

// renewCodexTokenSession returns the session of email. It renews the tokens
// first when the access token has under codexTokenRenewBefore left. renewed
// tells whether it did. It holds the per-email lock, so two processes never
// spend the same refresh token. A missing session returns backend.ErrNotFound.
func renewCodexTokenSession(ctx context.Context, store, local backend.Backend, email string) (blob []byte, renewed bool, err error) {
	name := "codex-token-" + strings.ReplaceAll(strings.ToLower(email), "/", "_") + ".lock"
	unlock, err := lockFileWait(name, codexTokenLockWait)
	if err != nil {
		return nil, false, err
	}
	defer unlock()

	blob, err = codexFresher(ctx, store, local, codexTokenKey(email))
	if err != nil {
		return nil, false, err
	}
	exp, ok := codex.AccessTokenExpiry(blob)
	if ok && time.Until(exp) >= codexTokenRenewBefore {
		return blob, false, nil
	}
	fresh, err := codex.Refresh(ctx, blob)
	if err != nil {
		// A temporary failure leaves the refresh token unspent. Keep the
		// current access token while it has more than an hour left.
		if !errors.Is(err, codex.ErrSessionEnded) && ok && time.Until(exp) > time.Hour {
			fmt.Fprintf(os.Stderr, "Warning: could not renew the Codex token session for %s (%v); the current access token is valid until %s.\n",
				email, err, exp.Local().Format("2006-01-02 15:04"))
			return blob, false, nil
		}
		return nil, false, err
	}
	saveCodexToken(ctx, store, local, email, fresh)
	return fresh, true, nil
}

// codexTokenEmails lists the emails of the Codex registry profiles, once each.
func codexTokenEmails() ([]string, error) {
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		return nil, err
	}
	var emails []string
	seen := map[string]bool{}
	for _, label := range reg.Labels() {
		email := reg.Profiles[label].Email
		if email != "" && !seen[strings.ToLower(email)] {
			seen[strings.ToLower(email)] = true
			emails = append(emails, email)
		}
	}
	return emails, nil
}

// renewCodexSessions renews every Codex token session that is near expiry.
// A missing session is skipped. A failure is logged and counted.
func renewCodexSessions(ctx context.Context, store, local backend.Backend, log *slog.Logger) (int, error) {
	emails, err := codexTokenEmails()
	if err != nil {
		return 0, err
	}
	renewed, failed := 0, 0
	for _, email := range emails {
		_, did, err := renewCodexTokenSession(ctx, store, local, email)
		switch {
		case errors.Is(err, backend.ErrNotFound):
		case err != nil:
			failed++
			log.Error("Codex token session not renewed", "email", email, "err", err)
		case did:
			renewed++
			log.Info("Codex token session renewed", "email", email)
		}
	}
	if failed > 0 {
		return renewed, fmt.Errorf("%d Codex token session(s) failed to renew", failed)
	}
	return renewed, nil
}

func untilText(blob []byte) string {
	if exp, ok := codex.AccessTokenExpiry(blob); ok {
		return exp.Format("2006-01-02")
	}
	return "unknown"
}

func codexTokenCreate(cmd *cobra.Command, email string) error {
	store, err := codexStoreFunc()
	if err != nil {
		return err
	}
	blob, err := codex.SignIn(cmd.Context(), filepath.Join(backupDir(), "codex-login.lock"),
		func(d codex.DeviceLogin) { announceCodexLogin(email, d) })
	if err != nil {
		return err
	}
	if id := codex.Describe(blob); !strings.EqualFold(id.Email, email) {
		who := id.Email
		if who == "" {
			who = "an unknown account"
		}
		return fmt.Errorf("signed in as %s, not %s — nothing was saved", who, email)
	}
	if err := store.Write(cmd.Context(), codexTokenKey(email), blob); err != nil {
		return fmt.Errorf("save Codex token session: %w", err)
	}
	fmt.Printf("Saved a Codex token session for %s (access token valid until %s)\n", email, untilText(blob))
	return nil
}

func codexTokenGet(cmd *cobra.Command, email, home string) error {
	store, err := codexStoreFunc()
	if err != nil {
		return err
	}
	blob, _, err := renewCodexTokenSession(cmd.Context(), store, codexTokenLocal(), email)
	switch {
	case errors.Is(err, backend.ErrNotFound):
		return fmt.Errorf("no token session for %s (run `ccswitch token create --codex %s`)", email, email)
	case errors.Is(err, codex.ErrSessionEnded):
		return fmt.Errorf("the token session for %s has ended; run `ccswitch token create --codex %s`", email, email)
	case err != nil:
		return err
	}
	out, err := codex.WithoutRefreshToken(blob)
	if err != nil {
		return err
	}
	if home == "" {
		fmt.Println(string(out))
		return nil
	}
	return writeAuthFile(home, out)
}

// writeAuthFile writes dir/auth.json with mode 0600, through a temp file.
func writeAuthFile(dir string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, codex.AuthFile))
}

func codexTokenRemove(cmd *cobra.Command, email string) error {
	store, err := codexStoreFunc()
	if err != nil {
		return err
	}
	local := codexTokenLocal()
	ctx := cmd.Context()
	if blob, err := codexFresher(ctx, store, local, codexTokenKey(email)); err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			return fmt.Errorf("no token session for %s", email)
		}
		return err
	} else if err := codex.Revoke(ctx, blob); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not revoke the session for %s: %v\n", email, err)
	}
	for _, b := range []backend.Backend{store, local} {
		if b == nil {
			continue
		}
		if err := b.Delete(ctx, codexTokenKey(email)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete a copy of the session: %v\n", err)
		}
	}
	fmt.Printf("Removed the Codex token session for %s\n", email)
	return nil
}

// printCodexTokens prints one row for each Codex profile email that has a
// session. It reports whether it printed any.
func printCodexTokens(ctx context.Context, store backend.Backend) bool {
	emails, err := codexTokenEmails()
	if err != nil {
		return false
	}
	local, found := codexTokenLocal(), false
	for _, email := range emails {
		blob, err := codexFresher(ctx, store, local, codexTokenKey(email))
		if err != nil {
			continue
		}
		found = true
		renewed := "unknown"
		if t, ok := codex.LastRefresh(blob); ok {
			renewed = t.Format("2006-01-02")
		}
		fmt.Printf("codex  %s  access token valid until %s  last renewed %s\n", email, untilText(blob), renewed)
	}
	return found
}
