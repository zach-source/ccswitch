package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
	"github.com/zach-source/ccswitch/internal/codex"
)

// failingWrites wraps a backend so every Write fails.
type failingWrites struct{ backend.Backend }

func (failingWrites) Write(context.Context, string, []byte) error {
	return errors.New("store offline")
}

// useCodexStore makes `codex` commands resolve b as their store.
func useCodexStore(t *testing.T, b backend.Backend) {
	t.Helper()
	old := codexStoreFunc
	codexStoreFunc = func() (backend.Backend, error) { return b, nil }
	t.Cleanup(func() { codexStoreFunc = old })
}

// offlineStore returns a store that holds profiles a and b but rejects writes.
func offlineStore(t *testing.T) backend.Backend {
	t.Helper()
	store := inmem.New()
	for _, l := range []string{"a", "b"} {
		data := []byte(codexBlob(l+"-rt", l+"@example.com"))
		if err := store.Write(context.Background(), codex.CredKey(l), data); err != nil {
			t.Fatal(err)
		}
	}
	return failingWrites{store}
}

func TestCodexUse_StoreWriteFailureFallsBackToLocalCopy(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, "")
	addCodexProfile(t, "a", "a-rt", "a@example.com")
	addCodexProfile(t, "b", "b-rt", "b@example.com")
	useCodexStore(t, offlineStore(t))

	liveB := codexBlob("b-rt-rotated", "b@example.com")
	if err := codex.WriteLive([]byte(liveB)); err != nil {
		t.Fatal(err)
	}

	if err := run(t, "codex", "use", "a"); err != nil {
		t.Fatalf("the switch must continue on the local copy: %v", err)
	}
	if got := readCred(t, home, codex.CredKey("b")); string(got) != liveB {
		t.Errorf("local copy does not hold the live bytes: %s", got)
	}
	live, _ := codex.ReadLive()
	if !strings.Contains(string(live), "a-rt") {
		t.Errorf("live login was not swapped: %s", live)
	}
}

// When no copy can be saved, the live file is the only one left.
func TestCodexUse_StopsWhenNoCopyCanBeSaved(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, "")
	addCodexProfile(t, "a", "a-rt", "a@example.com")
	addCodexProfile(t, "b", "b-rt", "b@example.com")
	useCodexStore(t, offlineStore(t))

	// A plain file where the local backend keeps its directory makes its writes fail.
	dir := filepath.Join(home, ".claude-switch-backup", "credentials")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	liveB := codexBlob("b-rt-rotated", "b@example.com")
	if err := codex.WriteLive([]byte(liveB)); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "codex", "use", "a"); err == nil {
		t.Fatal("the switch must stop when the live login cannot be saved anywhere")
	}
	if live, _ := codex.ReadLive(); string(live) != liveB {
		t.Errorf("the live file was overwritten: %s", live)
	}
}

func TestCodexFresher(t *testing.T) {
	ctx := context.Background()
	at := func(ts string) []byte { return []byte(`{"last_refresh":"` + ts + `"}`) }
	older, newer := at("2026-01-01T00:00:00Z"), at("2026-02-01T00:00:00Z")
	const key = "k"

	seed := func(data []byte) backend.Backend {
		b := inmem.New()
		if data != nil {
			_ = b.Write(ctx, key, data)
		}
		return b
	}

	tests := []struct {
		name         string
		store, local []byte
		want         []byte // nil means an error
	}{
		{"local is later", older, newer, newer},
		{"store is later", newer, older, newer},
		{"only store", older, nil, older},
		{"only local", nil, newer, newer},
		{"neither", nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := codexFresher(ctx, seed(tt.store), seed(tt.local), key)
			if tt.want == nil {
				if !errors.Is(err, backend.ErrNotFound) {
					t.Fatalf("err = %v, want the store's error", err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, tt.want) {
				t.Errorf("got %s, %v; want %s", got, err, tt.want)
			}
		})
	}

	t.Run("same object", func(t *testing.T) {
		b := seed(older)
		got, err := codexFresher(ctx, b, b, key)
		if err != nil || !bytes.Equal(got, older) {
			t.Errorf("got %s, %v", got, err)
		}
	})
}
