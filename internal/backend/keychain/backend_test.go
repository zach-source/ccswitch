//go:build darwin

package keychain_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/keychain"
)

// requireRealKeychain gates every test in this file behind an opt-in.
//
// These tests hit the real login keychain, and `go test` compiles a fresh
// binary with a fresh code-signing hash on every run. That hash is in no
// keychain item's access-control list, so each run makes macOS raise a
// password prompt per item touched — and the prompts queue, so a few `go
// test ./...` runs in a row leave a stack of dialogs behind that looks like
// an infinite loop to whoever has to dismiss them.
//
// `make check` must stay hermetic. Run these deliberately with:
//
//	CCSWITCH_KEYCHAIN_TESTS=1 go test ./internal/backend/keychain/
//
// Granting "Always Allow" once per run does not help: the next build has a
// different hash and starts over.
func requireRealKeychain(t *testing.T) {
	t.Helper()
	if os.Getenv("CCSWITCH_KEYCHAIN_TESTS") != "1" {
		t.Skip("set CCSWITCH_KEYCHAIN_TESTS=1 to exercise the real login keychain")
	}
}

func TestBackend_ReadWriteDelete(t *testing.T) {
	requireRealKeychain(t)
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
	requireRealKeychain(t)
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

// TestBackend_LookupHashedActiveSlot writes a "Claude Code-credentials-<x>"
// item and asserts LookupHashedActiveSlot finds it. claude 2.x writes
// hashed slots like that when CLAUDE_CONFIG_DIR is set; ccswitch enumerates
// to capture them because the hash is opaque.
func TestBackend_LookupHashedActiveSlot(t *testing.T) {
	requireRealKeychain(t)
	ctx := context.Background()
	b := keychain.New()

	// Use a clearly-test-only suffix so we never collide with real items.
	const svc = "Claude Code-credentials-ccswitchtest"
	t.Cleanup(func() {
		_ = b.Delete(ctx, svc)
	})

	since := time.Now().Add(-1 * time.Second)
	payload := []byte(`{"claudeAiOauth":{"accessToken":"hashed-AT","refreshToken":"hashed-RT","expiresAt":1}}`)
	if err := b.Write(ctx, svc, payload); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	got, gotSvc, err := b.LookupHashedActiveSlot(ctx, since)
	if err != nil {
		t.Fatalf("LookupHashedActiveSlot: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch:\n want %s\n  got %s", payload, got)
	}
	if gotSvc != svc {
		t.Fatalf("service name mismatch: want %q got %q", svc, gotSvc)
	}

	// A future `since` filters the item out.
	future := time.Now().Add(1 * time.Hour)
	if got, gotSvc, err := b.LookupHashedActiveSlot(ctx, future); err != nil || got != nil || gotSvc != "" {
		t.Fatalf("future since must return (nil, \"\", nil): got=%q svc=%q err=%v", got, gotSvc, err)
	}
}
