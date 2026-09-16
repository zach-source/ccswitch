package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/file"
)

// migrateMarker records that the one-time keychain→file backup migration has
// run. Its presence — not its contents — is the signal.
const migrateMarker = ".backups-migrated"

// migrateOnce guards against repeating the work within a single process; the
// marker file guards across processes.
var migrateOnce sync.Once

// migrateKeychainBackups copies per-account credential backups out of the
// macOS login keychain and into the file store, once.
//
// Backups used to default to the keychain on macOS. autoStoreBackend now
// defaults them to files, so without this an upgrade would silently look at
// an empty store and report every account as having no stored credentials.
//
// Deliberately best-effort and quiet on the happy path: it runs from
// resolveBackend, so a hard failure here would break every command. A partial
// copy is safe to retry because the marker is only written when every item
// was copied, and because the keychain items are left in place — `ccswitch
// prune` does not touch them, and removing them is the user's call.
//
// Skipped entirely when the local backend is not the keychain, which keeps
// the conformance suite (CCSWITCH_LOCAL_BACKEND=file) hermetic.
func migrateKeychainBackups() {
	migrateOnce.Do(func() {
		if err := runMigrateKeychainBackups(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not migrate keychain backups to files: %v\n", err)
		}
	})
}

func runMigrateKeychainBackups() error {
	if autoLocalBackend() != backend.TypeKeychain {
		return nil
	}
	marker := filepath.Join(backupDir(), migrateMarker)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}

	local, err := resolveLocalBackend()
	if err != nil {
		return err
	}
	lister, ok := local.(interface {
		ListByPrefix(ctx context.Context, prefix string) ([]string, error)
	})
	if !ok {
		return nil
	}

	ctx := context.Background()
	keys, err := lister.ListByPrefix(ctx, backupKeyPrefix)
	if err != nil {
		return fmt.Errorf("list keychain backups: %w", err)
	}

	copied, err := copyBackups(ctx, local, file.New(filepath.Join(backupDir(), "credentials")), keys)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(backupDir(), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", backupDir(), err)
	}
	if err := os.WriteFile(marker, []byte("keychain backups migrated to files\n"), 0o600); err != nil {
		return fmt.Errorf("write migration marker: %w", err)
	}
	if copied > 0 {
		fmt.Fprintf(os.Stderr,
			"Moved %d account backup(s) out of the login keychain into %s.\n"+
				"This stops macOS prompting for the keychain password after every ccswitch\n"+
				"rebuild. The keychain copies are left in place; delete them by hand if you\n"+
				"want them gone.\n",
			copied, filepath.Join(backupDir(), "credentials"))
	}
	return nil
}

// copyBackups copies each key from src to dst, returning how many it wrote.
//
// An existing non-empty value in dst is never overwritten: a file the user
// already has is at least as fresh as the keychain copy and may be fresher,
// having been written by a refresh since the migration last ran.
func copyBackups(ctx context.Context, src, dst backend.Backend, keys []string) (int, error) {
	copied := 0
	for _, key := range keys {
		if existing, err := dst.Read(ctx, key); err == nil && len(existing) > 0 {
			continue
		}
		data, err := src.Read(ctx, key)
		if err != nil {
			return copied, fmt.Errorf("read %q from %s: %w", key, src.Name(), err)
		}
		if err := dst.Write(ctx, key, data); err != nil {
			return copied, fmt.Errorf("write %q to %s: %w", key, dst.Name(), err)
		}
		copied++
	}
	return copied, nil
}

// backupKeyPrefix matches every per-account backup key, whatever the id and
// email. TestBackupKeyPrefixMatchesBackupCredKey keeps it honest against
// account.BackupCredKey.
const backupKeyPrefix = "Claude Code-Account-"
