package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
)

// backupKeyPrefix is duplicated from account.BackupCredKey's format string so
// the migration can enumerate backups without a sequence. If the key format
// ever changes, the migration would silently find nothing — catch that here.
func TestBackupKeyPrefixMatchesBackupCredKey(t *testing.T) {
	key := account.BackupCredKey("abc12345", "alice@example.com")
	if !strings.HasPrefix(key, backupKeyPrefix) {
		t.Fatalf("BackupCredKey produces %q, which does not start with %q", key, backupKeyPrefix)
	}
}

// The store must default to files, not the keychain: a keychain item carries
// a per-binary access-control list that every rebuild invalidates.
func TestAutoStoreBackendIsFile(t *testing.T) {
	if got := autoStoreBackend(); got != backend.TypeFile {
		t.Errorf("autoStoreBackend() = %q, want %q", got, backend.TypeFile)
	}
}

// The migration must not run — and must not touch the real login keychain —
// when the local backend has been pointed somewhere else.
func TestMigrateSkippedWhenLocalBackendIsNotKeychain(t *testing.T) {
	home := newTestHome(t) // sets CCSWITCH_LOCAL_BACKEND=file

	if err := runMigrateKeychainBackups(); err != nil {
		t.Fatalf("migration should be a no-op, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-switch-backup", migrateMarker)); err == nil {
		t.Error("migration wrote its marker despite being skipped")
	}
}

// copyBackups is the body of the keychain→file migration. Exercised against
// in-memory backends so the suite never touches the real login keychain.
func TestCopyBackupsMovesEachKeyOnce(t *testing.T) {
	ctx := context.Background()
	src, dst := inmem.New(), inmem.New()
	k1 := account.BackupCredKey("aaaaaaaa", "alice@example.com")
	k2 := account.BackupCredKey("bbbbbbbb", "bob@example.com")
	if err := src.Write(ctx, k1, []byte(`{"claudeAiOauth":{"accessToken":"a"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := src.Write(ctx, k2, []byte(`{"claudeAiOauth":{"accessToken":"b"}}`)); err != nil {
		t.Fatal(err)
	}

	n, err := copyBackups(ctx, src, dst, []string{k1, k2})
	if err != nil {
		t.Fatalf("copyBackups: %v", err)
	}
	if n != 2 {
		t.Errorf("copied = %d, want 2", n)
	}
	got, err := dst.Read(ctx, k1)
	if err != nil || !strings.Contains(string(got), `"a"`) {
		t.Errorf("alice's backup did not arrive: %s (%v)", got, err)
	}

	// Re-running must be a no-op, not a re-copy.
	if n, err = copyBackups(ctx, src, dst, []string{k1, k2}); err != nil || n != 0 {
		t.Errorf("second run copied = %d (err %v), want 0", n, err)
	}
}

// A fresher file copy must win: overwriting it with the stale keychain value
// would roll a refreshed token backwards.
func TestCopyBackupsNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	src, dst := inmem.New(), inmem.New()
	key := account.BackupCredKey("aaaaaaaa", "alice@example.com")
	if err := src.Write(ctx, key, []byte(`{"claudeAiOauth":{"accessToken":"stale"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := dst.Write(ctx, key, []byte(`{"claudeAiOauth":{"accessToken":"fresh"}}`)); err != nil {
		t.Fatal(err)
	}

	if n, err := copyBackups(ctx, src, dst, []string{key}); err != nil || n != 0 {
		t.Fatalf("copied = %d (err %v), want 0", n, err)
	}
	if got, _ := dst.Read(ctx, key); !strings.Contains(string(got), "fresh") {
		t.Errorf("migration clobbered a fresher credential: %s", got)
	}
}
