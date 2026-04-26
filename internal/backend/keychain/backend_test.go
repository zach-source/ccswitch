//go:build darwin

package keychain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/keychain"
)

// These tests exercise the real macOS Security.framework. They require the
// test binary to be entitled to access the login keychain (true for local
// `go test` runs; may be skipped in sandboxed CI).
func TestBackend_ReadWriteDelete(t *testing.T) {
	ctx := context.Background()
	b := keychain.New()

	const key = "ccswitch-test-credential-key"

	// Clean up any leftover item from a previous failed run.
	_ = b.Delete(ctx, key)

	t.Run("read_missing_returns_ErrNotFound", func(t *testing.T) {
		_, err := b.Read(ctx, key)
		if !errors.Is(err, backend.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("write_then_read", func(t *testing.T) {
		want := []byte(`{"token":"test-value"}`)
		if err := b.Write(ctx, key, want); err != nil {
			t.Fatalf("Write: %v", err)
		}
		t.Cleanup(func() { _ = b.Delete(ctx, key) })

		got, err := b.Read(ctx, key)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("Read got %q, want %q", got, want)
		}
	})

	t.Run("write_idempotent_update", func(t *testing.T) {
		if err := b.Write(ctx, key, []byte("v1")); err != nil {
			t.Fatal(err)
		}
		if err := b.Write(ctx, key, []byte("v2")); err != nil {
			t.Fatalf("second Write: %v", err)
		}
		t.Cleanup(func() { _ = b.Delete(ctx, key) })

		got, err := b.Read(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "v2" {
			t.Errorf("got %q, want v2", got)
		}
	})

	t.Run("delete_idempotent", func(t *testing.T) {
		if err := b.Write(ctx, key, []byte("data")); err != nil {
			t.Fatal(err)
		}
		if err := b.Delete(ctx, key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// Second delete must also return nil.
		if err := b.Delete(ctx, key); err != nil {
			t.Fatalf("second Delete: %v", err)
		}
	})
}

func TestBackend_HealthCheck(t *testing.T) {
	b := keychain.New()
	if err := b.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestBackend_Name(t *testing.T) {
	if keychain.New().Name() != "keychain" {
		t.Error("Name() != keychain")
	}
}

// Compile-time interface check.
var _ backend.Backend = (*keychain.Backend)(nil)
