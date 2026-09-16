package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/credentials"
	"github.com/zach-source/ccswitch/internal/refresh"
)

// refreshActiveSlot renews the *live* credential in place and mirrors the
// result into the active account's backup slot.
//
// This exists because refresh tokens rotate. The exchange invalidates the
// token it was sent, so whatever Claude Code reads has to learn the new one
// in the same breath. The generic per-account pass only ever writes backup
// slots — correct for every account that is not live, and actively dangerous
// for the one that is: it would leave the active slot holding a superseded
// token, Claude Code's own next refresh would replay it, and a replayed
// refresh token is indistinguishable from a stolen one. The server revokes
// the whole family and every session is logged out, recoverable only by an
// interactive re-login.
//
// Ordering and locking both matter:
//
//   - The exchange and both writes run under the same two locks Claude Code
//     takes for its own refresh, so the two cannot interleave.
//   - The source blob is re-read *inside* the lock. The live slot is the
//     authority for the active account; refreshing a copy read before the
//     lock could exchange a token Claude Code has already rotated away.
//   - The live slot is written before the backup. A crash between the two
//     leaves the live session working and the backup stale, which the next
//     pass repairs. The reverse order would strand the live session.
func refreshActiveSlot(ctx context.Context, local, store backend.Backend, backupKey string) (*credentials.Credentials, error) {
	var out *credentials.Credentials
	err := withClaudeLocks(func() error {
		live, err := local.Read(ctx, account.ActiveCredKey)
		if err != nil {
			return fmt.Errorf("read active slot: %w", err)
		}

		newData, newCred, err := refresh.RefreshOne(ctx, live)
		if err != nil {
			return err
		}

		// RefreshOne splices into the raw bytes, so mcpOAuth and every other
		// sibling key survives — the live slot can be written as-is.
		if err := local.Write(ctx, account.ActiveCredKey, newData); err != nil {
			return fmt.Errorf("write active slot: %w", err)
		}

		// The backup keeps only claudeAiOauth. mcpOAuth is minted per MCP
		// server and belongs to no Claude account; filing it under this one
		// would smear these MCP logins onto whichever account is restored next.
		snapshot, err := credentials.OAuthOnly(newData)
		if err != nil {
			return fmt.Errorf("snapshot refreshed credentials: %w", err)
		}
		if err := store.Write(ctx, backupKey, snapshot); err != nil {
			return fmt.Errorf("write backup slot: %w", err)
		}

		out = newCred
		return nil
	})
	return out, err
}

// syncActiveAccount reconciles the live credential with the active account's
// backup slot, refreshing it in place first when it is the copy that has
// expired.
//
// The refresh pass that follows only reads backup slots, so without this the
// active account would be refreshed through the generic path — rotating its
// token without the live slot ever finding out. Doing it here also means the
// pass finds a fresh backup and skips the account entirely.
func syncActiveAccount(ctx context.Context, store, local backend.Backend, seq *account.Sequence, buffer time.Duration) {
	id := activeID(seq)
	if id == "" {
		return
	}
	acct, ok := seq.Accounts[id]
	if !ok {
		return
	}
	// The active slot is local by definition — read it from the local
	// backend, not the configured store, which may be 1Password or Vault.
	data, err := local.Read(ctx, account.ActiveCredKey)
	if err != nil || len(data) == 0 {
		return
	}
	key := account.BackupCredKey(id, acct.Email)

	if cred, perr := credentials.Parse(data); perr == nil && cred.IsExpired(buffer) {
		if _, rerr := refreshActiveSlot(ctx, local, store, key); rerr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not refresh the live credential in place: %v\n", rerr)
		}
		return
	}

	// Still valid — snapshot it so a freshly-authenticated active account is
	// never reported as stale (ccswitch.sh step 1).
	snapshot, oerr := credentials.OAuthOnly(data)
	if oerr != nil {
		return
	}
	_ = store.Write(ctx, key, snapshot)
}
