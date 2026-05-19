package onepassword_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/onepassword"
)

// fakeServer builds a minimal in-process 1Password Connect stub.
type fakeServer struct {
	items map[string]*fakeItem // itemID → item
	vault struct {
		id   string
		name string
	}
	mux *http.ServeMux
}

type fakeItem struct {
	ID     string
	Title  string
	Fields []map[string]string
}

func newFakeServer(vaultName string) *fakeServer {
	fs := &fakeServer{
		items: make(map[string]*fakeItem),
	}
	fs.vault.id = "vault-uuid-1"
	fs.vault.name = vaultName
	fs.mux = http.NewServeMux()

	// GET /v1/vaults
	fs.mux.HandleFunc("/v1/vaults", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && !strings.Contains(r.URL.Path, "/items") {
			json.NewEncoder(w).Encode([]map[string]string{ //nolint:errcheck
				{"id": fs.vault.id, "name": fs.vault.name},
			})
			return
		}
		http.NotFound(w, r)
	})

	// /v1/vaults/{vid}/items and /v1/vaults/{vid}/items/{iid}
	fs.mux.HandleFunc("/v1/vaults/"+fs.vault.id+"/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			filter := r.URL.Query().Get("filter")
			// filter = `title eq "..."` — extract the title
			title := ""
			if after, ok := strings.CutPrefix(filter, "title eq \""); ok {
				title = strings.TrimSuffix(after, "\"")
			}
			var out []map[string]string
			for _, it := range fs.items {
				if it.Title == title {
					out = append(out, map[string]string{"id": it.ID})
				}
			}
			json.NewEncoder(w).Encode(out) //nolint:errcheck
		case http.MethodPost:
			var body struct {
				Title  string              `json:"title"`
				Fields []map[string]string `json:"fields"`
			}
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			id := "item-" + body.Title
			fs.items[id] = &fakeItem{ID: id, Title: body.Title, Fields: body.Fields}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"id": id}) //nolint:errcheck
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	fs.mux.HandleFunc("/v1/vaults/"+fs.vault.id+"/items/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/vaults/"+fs.vault.id+"/items/")
		switch r.Method {
		case http.MethodGet:
			it, ok := fs.items[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var fields []map[string]string
			for _, f := range it.Fields {
				fields = append(fields, f)
			}
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":     it.ID,
				"title":  it.Title,
				"fields": fields,
			})
		case http.MethodPut:
			if _, ok := fs.items[id]; !ok {
				http.NotFound(w, r)
				return
			}
			var body struct {
				Fields []map[string]string `json:"fields"`
			}
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			fs.items[id].Fields = body.Fields
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"id": id}) //nolint:errcheck
		case http.MethodDelete:
			delete(fs.items, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return fs
}

func (fs *fakeServer) newBackend(t *testing.T) *onepassword.Backend {
	t.Helper()
	srv := httptest.NewServer(fs.mux)
	t.Cleanup(srv.Close)

	b, err := onepassword.New(onepassword.Config{
		Host:        srv.URL,
		BearerToken: "test-token",
		VaultName:   fs.vault.name,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestBackend_ReadWriteDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("read_missing_returns_ErrNotFound", func(t *testing.T) {
		fs := newFakeServer("MyVault")
		b := fs.newBackend(t)
		_, err := b.Read(ctx, "missing-key")
		if !errors.Is(err, backend.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("write_then_read", func(t *testing.T) {
		fs := newFakeServer("MyVault")
		b := fs.newBackend(t)
		want := []byte(`{"token":"abc"}`)
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

	t.Run("write_update_idempotent", func(t *testing.T) {
		fs := newFakeServer("MyVault")
		b := fs.newBackend(t)
		if err := b.Write(ctx, "k", []byte("v1")); err != nil {
			t.Fatal(err)
		}
		if err := b.Write(ctx, "k", []byte("v2")); err != nil {
			t.Fatalf("second Write: %v", err)
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
		fs := newFakeServer("MyVault")
		b := fs.newBackend(t)
		if err := b.Write(ctx, "del-key", []byte("data")); err != nil {
			t.Fatal(err)
		}
		if err := b.Delete(ctx, "del-key"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := b.Delete(ctx, "del-key"); err != nil {
			t.Fatalf("second Delete: %v", err)
		}
		_, err := b.Read(ctx, "del-key")
		if !errors.Is(err, backend.ErrNotFound) {
			t.Errorf("after delete expected ErrNotFound, got %v", err)
		}
	})
}

func TestBackend_HealthCheck(t *testing.T) {
	fs := newFakeServer("MyVault")
	b := fs.newBackend(t)
	if err := b.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestBackend_Name(t *testing.T) {
	fs := newFakeServer("MyVault")
	b := fs.newBackend(t)
	if b.Name() != "1password" {
		t.Errorf("Name() = %q, want \"1password\"", b.Name())
	}
}

// Compile-time interface check.
var _ backend.Backend = (*onepassword.Backend)(nil)
