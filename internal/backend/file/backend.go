// Package file implements a local-filesystem credential backend. Credentials
// are stored as 0600 files under a configurable root directory, matching the
// _file_read/_file_write/_file_delete semantics from the bash reference.
package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zach-source/ccswitch/internal/backend"
)

// Backend is a file-system credential store. All operations are safe for
// concurrent use; atomicity is achieved via write-to-temp-then-rename.
type Backend struct {
	root string
}

// New returns a Backend that stores credentials under root. root is typically
// ~/.claude for the active slot and ~/.claude-switch-backup/credentials/ for
// per-account backups. The directory is created with mode 0700 on first use if
// it does not already exist.
func New(root string) *Backend {
	return &Backend{root: root}
}

// Name implements backend.Backend.
func (b *Backend) Name() string { return "file" }

// keyToPath translates a logical key (e.g. "Claude Code-credentials") into an
// absolute file path under the configured root. Path separators in the key are
// replaced with underscores to prevent directory traversal.
func (b *Backend) keyToPath(key string) string {
	safe := strings.ReplaceAll(key, string(filepath.Separator), "_")
	// Also sanitise forward slash on all platforms so the rule is consistent.
	safe = strings.ReplaceAll(safe, "/", "_")
	filename := "." + safe + ".json"
	return filepath.Join(b.root, filename)
}

// ensureDir creates the root directory with mode 0700 if it does not exist.
func (b *Backend) ensureDir() error {
	if err := os.MkdirAll(b.root, 0700); err != nil {
		return fmt.Errorf("file backend: create root %q: %w", b.root, err)
	}
	return nil
}

// Read returns the raw bytes stored at key. Returns (nil, backend.ErrNotFound)
// when no file exists for that key.
func (b *Backend) Read(_ context.Context, key string) ([]byte, error) {
	path := b.keyToPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, backend.ErrNotFound
		}
		return nil, fmt.Errorf("file backend: read %q: %w", path, err)
	}
	return data, nil
}

// Write stores data at key using an atomic write (temp file + rename) so
// concurrent readers never see a partial write. The file is created with mode
// 0600.
func (b *Backend) Write(_ context.Context, key string, data []byte) error {
	if err := b.ensureDir(); err != nil {
		return err
	}
	path := b.keyToPath(key)

	// Write to a sibling temp file then rename into place.
	tmp, err := os.CreateTemp(b.root, ".ccswitch-tmp-*")
	if err != nil {
		return fmt.Errorf("file backend: create temp: %w", err)
	}
	tmpName := tmp.Name()

	// Best-effort cleanup on failure.
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName) //nolint:errcheck
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("file backend: write temp: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("file backend: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("file backend: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("file backend: rename %q -> %q: %w", tmpName, path, err)
	}
	ok = true
	return nil
}

// Delete removes the file for key. Returns nil if the file does not exist
// (idempotent).
func (b *Backend) Delete(_ context.Context, key string) error {
	path := b.keyToPath(key)
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("file backend: delete %q: %w", path, err)
	}
	return nil
}

// HealthCheck verifies that the root directory is reachable and writable by
// creating and removing a probe file.
func (b *Backend) HealthCheck(_ context.Context) error {
	if err := b.ensureDir(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.root, ".ccswitch-health-*")
	if err != nil {
		return fmt.Errorf("file backend: health check — root not writable: %w", err)
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name) //nolint:errcheck
	return nil
}
