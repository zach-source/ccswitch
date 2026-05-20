package cli

import (
	"context"
	"testing"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
)

// TestReadAccountCred_StoreVsLocal pins the two-backend split that the
// 1password-cli default exposed: per-account backups live in the configured
// store (possibly remote), while the active account's live credential is in
// the local active slot. A single-backend assumption silently returned "no
// token" for the active account when the store was remote.
func TestReadAccountCred_StoreVsLocal(t *testing.T) {
	ctx := context.Background()
	store := inmem.New() // stand-in for a remote backend (1Password)
	local := inmem.New() // stand-in for the local keychain

	id := account.HashEmail("alice@example.com")
	email := "alice@example.com"

	activeBlob := []byte(`{"claudeAiOauth":{"accessToken":"live-AT","refreshToken":"r","expiresAt":111}}`)
	backupBlob := []byte(`{"claudeAiOauth":{"accessToken":"backup-AT","refreshToken":"r","expiresAt":222}}`)

	// Active credential only in the local active slot; backup only in store.
	if err := local.Write(ctx, account.ActiveCredKey, activeBlob); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, account.BackupCredKey(id, email), backupBlob); err != nil {
		t.Fatal(err)
	}

	// Active account: prefers the local active slot.
	if c := readAccountCred(ctx, store, local, true, id, email); c == nil {
		t.Fatal("active account returned no credential (the reported bug)")
	} else if c.ClaudeAIOAuth.AccessToken != "live-AT" {
		t.Errorf("active account token = %q, want live-AT", c.ClaudeAIOAuth.AccessToken)
	}

	// Non-active account: reads the store backup key.
	if c := readAccountCred(ctx, store, local, false, id, email); c == nil {
		t.Fatal("non-active account returned no credential")
	} else if c.ClaudeAIOAuth.AccessToken != "backup-AT" {
		t.Errorf("non-active account token = %q, want backup-AT", c.ClaudeAIOAuth.AccessToken)
	}

	// Active account with an empty local slot falls back to the store backup.
	emptyLocal := inmem.New()
	if c := readAccountCred(ctx, store, emptyLocal, true, id, email); c == nil {
		t.Fatal("active account with empty local slot should fall back to store backup")
	} else if c.ClaudeAIOAuth.AccessToken != "backup-AT" {
		t.Errorf("fallback token = %q, want backup-AT", c.ClaudeAIOAuth.AccessToken)
	}
}
