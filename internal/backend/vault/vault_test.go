package vault_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/vault"
)

// fakeVault is a minimal KV v2 stub that speaks the Vault HTTP API.
type fakeVault struct {
	mu     sync.Mutex
	store  map[string]map[string]interface{} // path → data
	health bool
}

func newFakeVault() *fakeVault {
	return &fakeVault{
		store:  make(map[string]map[string]interface{}),
		health: true,
	}
}

func (fv *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Health endpoint: GET /v1/sys/health
	if r.URL.Path == "/v1/sys/health" {
		if fv.health {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"initialized": true, "sealed": false}) //nolint:errcheck
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return
	}

	// KV v2: paths are /v1/<mount>/data/<key>
	// The vault/api KVv2 client POSTs to /v1/<mount>/data/<key> for writes.
	path := r.URL.Path
	// Strip /v1/ prefix and /data/ infix so we store by logical key.
	path = strings.TrimPrefix(path, "/v1/")
	// Remove the "/data/" segment that KVv2 inserts.
	if idx := strings.Index(path, "/data/"); idx != -1 {
		path = path[idx+len("/data/"):]
	}

	fv.mu.Lock()
	defer fv.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		data, ok := fv.store[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"errors": "secret not found"}) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": map[string]interface{}{"data": data},
		})
	case http.MethodPost, http.MethodPut:
		var body struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fv.store[path] = body.Data
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"version": 1}}) //nolint:errcheck
	case http.MethodDelete:
		delete(fv.store, path)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func newTestBackend(t *testing.T, fv *fakeVault) *vault.Backend {
	t.Helper()
	srv := httptest.NewServer(fv)
	t.Cleanup(srv.Close)

	b, err := vault.New(vault.Config{
		Addr:  srv.URL,
		Token: "test-token",
		Path:  "secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestBackend_ReadWriteDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("read_missing_returns_ErrNotFound", func(t *testing.T) {
		b := newTestBackend(t, newFakeVault())
		_, err := b.Read(ctx, "missing")
		if !errors.Is(err, backend.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("write_then_read", func(t *testing.T) {
		b := newTestBackend(t, newFakeVault())
		want := []byte(`{"token":"xyz"}`)
		if err := b.Write(ctx, "mykey", want); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := b.Read(ctx, "mykey")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		b := newTestBackend(t, newFakeVault())
		if err := b.Write(ctx, "k", []byte("v1")); err != nil {
			t.Fatal(err)
		}
		if err := b.Write(ctx, "k", []byte("v2")); err != nil {
			t.Fatal(err)
		}
		got, err := b.Read(ctx, "k")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "v2" {
			t.Errorf("got %q, want v2", got)
		}
	})

	t.Run("delete_idempotent", func(t *testing.T) {
		b := newTestBackend(t, newFakeVault())
		if err := b.Write(ctx, "del", []byte("data")); err != nil {
			t.Fatal(err)
		}
		if err := b.Delete(ctx, "del"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := b.Delete(ctx, "del"); err != nil {
			t.Fatalf("second Delete: %v", err)
		}
		_, err := b.Read(ctx, "del")
		if !errors.Is(err, backend.ErrNotFound) {
			t.Errorf("after delete expected ErrNotFound, got %v", err)
		}
	})
}

func TestBackend_HealthCheck(t *testing.T) {
	b := newTestBackend(t, newFakeVault())
	if err := b.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestBackend_Name(t *testing.T) {
	b := newTestBackend(t, newFakeVault())
	if b.Name() != "vault" {
		t.Errorf("Name() = %q, want \"vault\"", b.Name())
	}
}

// Compile-time interface check.
var _ backend.Backend = (*vault.Backend)(nil)
