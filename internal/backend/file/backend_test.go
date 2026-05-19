package file_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/file"
)

func TestBackend_ReadWriteDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("roundtrip", func(t *testing.T) {
		root := t.TempDir()
		b := file.New(root)

		const key = "Claude Code-credentials"
		want := []byte(`{"token":"abc123"}`)

		if err := b.Write(ctx, key, want); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := b.Read(ctx, key)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("Read got %q, want %q", got, want)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		root := t.TempDir()
		b := file.New(root)
		const key = "some-key"

		if err := b.Write(ctx, key, []byte("v1")); err != nil {
			t.Fatal(err)
		}
		if err := b.Write(ctx, key, []byte("v2")); err != nil {
			t.Fatal(err)
		}
		got, err := b.Read(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "v2" {
			t.Errorf("got %q, want v2", got)
		}
	})

	t.Run("read_missing_returns_ErrNotFound", func(t *testing.T) {
		root := t.TempDir()
		b := file.New(root)
		_, err := b.Read(ctx, "nonexistent")
		if !errors.Is(err, backend.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("delete_idempotent", func(t *testing.T) {
		root := t.TempDir()
		b := file.New(root)
		const key = "del-key"

		// Delete of non-existent key must be nil.
		if err := b.Delete(ctx, key); err != nil {
			t.Fatalf("Delete non-existent: %v", err)
		}

		if err := b.Write(ctx, key, []byte("data")); err != nil {
			t.Fatal(err)
		}
		if err := b.Delete(ctx, key); err != nil {
			t.Fatalf("Delete existing: %v", err)
		}
		_, err := b.Read(ctx, key)
		if !errors.Is(err, backend.ErrNotFound) {
			t.Errorf("after delete: expected ErrNotFound, got %v", err)
		}
	})

	t.Run("key_sanitization_no_traversal", func(t *testing.T) {
		root := t.TempDir()
		b := file.New(root)
		// A key with a path separator must not escape root.
		key := filepath.Join("..", "escape")
		if err := b.Write(ctx, key, []byte("data")); err != nil {
			t.Fatal(err)
		}
		got, err := b.Read(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "data" {
			t.Errorf("got %q", got)
		}
	})
}

func TestBackend_HealthCheck(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := file.New(root)
	if err := b.HealthCheck(ctx); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestBackend_Name(t *testing.T) {
	b := file.New(t.TempDir())
	if b.Name() != "file" {
		t.Errorf("Name() = %q, want \"file\"", b.Name())
	}
}

// Ensure *Backend satisfies the backend.Backend interface at compile time.
var _ backend.Backend = (*file.Backend)(nil)
