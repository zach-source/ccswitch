package onepassword

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/zach-source/ccswitch/internal/backend"
)

// backendErrNotFound aliases the sentinel so errNotFound() can wrap it without
// an import cycle (this package imports backend, not the other way around).
var backendErrNotFound = backend.ErrNotFound

// Config holds the connection parameters for a 1Password Connect server.
type Config struct {
	// Host is the base URL of the 1Password Connect server (e.g.
	// "https://op-connect.example.com").
	Host string
	// BearerToken is the Connect API token. Passed via X-OP-Token header.
	BearerToken string
	// CFAccessClientID is the Cloudflare Access client ID (optional). When
	// set it is injected via the CF-Access-Client-Id header.
	CFAccessClientID string
	// CFAccessClientSecret is the Cloudflare Access client secret (optional).
	CFAccessClientSecret string
	// VaultName is the display name of the vault to target. Resolved to a UUID
	// once at construction time.
	VaultName string
	// ItemPrefix is prepended to every item title (mirrors $CCSWITCH_OP_ITEM_PREFIX).
	ItemPrefix string
}

// Backend is a 1Password Connect credential store.
type Backend struct {
	cfg     Config
	client  *http.Client
	vaultID string   // resolved at construction
	cache   sync.Map // title (string) → itemID (string)
}

// opVault is a subset of the Connect API vault response.
type opVault struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// opItem is a partial Connect API item response.
type opItem struct {
	ID     string    `json:"id"`
	Title  string    `json:"title"`
	Fields []opField `json:"fields"`
}

// opField is a single field within a Connect API item.
type opField struct {
	Label string `json:"label"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

const credentialsFieldLabel = "credentials"

// New constructs a Backend and resolves the vault name to its UUID. Returns an
// error if the vault cannot be found or the Connect server is unreachable.
func New(cfg Config) (*Backend, error) {
	transport := &authTransport{
		base:                 http.DefaultTransport,
		bearerToken:          cfg.BearerToken,
		cfAccessClientID:     cfg.CFAccessClientID,
		cfAccessClientSecret: cfg.CFAccessClientSecret,
	}
	b := &Backend{
		cfg:    cfg,
		client: &http.Client{Transport: transport},
	}

	ctx := context.Background()
	id, err := b.resolveVaultID(ctx, cfg.VaultName)
	if err != nil {
		return nil, fmt.Errorf("onepassword backend: resolve vault %q: %w", cfg.VaultName, err)
	}
	b.vaultID = id
	return b, nil
}

// Name implements backend.Backend.
func (b *Backend) Name() string { return "1password" }

// resolveVaultID fetches the vault list and returns the UUID for vaultName.
func (b *Backend) resolveVaultID(ctx context.Context, vaultName string) (string, error) {
	var vaults []opVault
	if err := b.apiGet(ctx, "/v1/vaults", &vaults); err != nil {
		return "", err
	}
	for _, v := range vaults {
		if v.Name == vaultName {
			return v.ID, nil
		}
	}
	return "", fmt.Errorf("vault %q not found", vaultName)
}

// findItemID returns the item UUID for title within b.vaultID, caching the
// result for the lifetime of the Backend. Returns "" when not found.
func (b *Backend) findItemID(ctx context.Context, title string) (string, error) {
	if v, ok := b.cache.Load(title); ok {
		return v.(string), nil
	}
	encoded := url.QueryEscape(title)
	path := fmt.Sprintf("/v1/vaults/%s/items?filter=title%%20eq%%20%%22%s%%22",
		b.vaultID, encoded)

	var items []opItem
	if err := b.apiGet(ctx, path, &items); err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", nil
	}
	id := items[0].ID
	b.cache.Store(title, id)
	return id, nil
}

// itemTitle builds the Connect item title from a logical key (mirrors
// _op_item_name in bash: "${prefix} - ${key}").
func (b *Backend) itemTitle(key string) string {
	if b.cfg.ItemPrefix == "" {
		return key
	}
	return b.cfg.ItemPrefix + " - " + key
}

// Read retrieves the credentials field from the Connect item whose title
// matches key. Returns backend.ErrNotFound when no such item exists.
func (b *Backend) Read(ctx context.Context, key string) ([]byte, error) {
	title := b.itemTitle(key)

	itemID, err := b.findItemID(ctx, title)
	if err != nil {
		return nil, fmt.Errorf("onepassword backend: read %q: %w", key, err)
	}
	if itemID == "" {
		return nil, errNotFound(key)
	}

	var item opItem
	path := fmt.Sprintf("/v1/vaults/%s/items/%s", b.vaultID, itemID)
	if err := b.apiGet(ctx, path, &item); err != nil {
		return nil, fmt.Errorf("onepassword backend: fetch item %q: %w", key, err)
	}
	for _, f := range item.Fields {
		if f.Label == credentialsFieldLabel {
			return []byte(f.Value), nil
		}
	}
	return nil, errNotFound(key)
}

// Write creates or updates the Connect item for key with the given data. The
// item is stored as a Secure Note with a CONCEALED field labelled "credentials".
func (b *Backend) Write(ctx context.Context, key string, data []byte) error {
	title := b.itemTitle(key)

	body := map[string]any{
		"vault":    map[string]string{"id": b.vaultID},
		"category": "SECURE_NOTE",
		"title":    title,
		"fields": []map[string]string{
			{"label": credentialsFieldLabel, "type": "CONCEALED", "value": string(data)},
		},
	}

	itemID, err := b.findItemID(ctx, title)
	if err != nil {
		return fmt.Errorf("onepassword backend: write %q: %w", key, err)
	}

	if itemID != "" {
		// PUT to update the existing item.
		path := fmt.Sprintf("/v1/vaults/%s/items/%s", b.vaultID, itemID)
		if err := b.apiDo(ctx, http.MethodPut, path, body, nil); err != nil {
			return fmt.Errorf("onepassword backend: update %q: %w", key, err)
		}
	} else {
		// POST to create a new item; cache the returned ID.
		var created opItem
		path := fmt.Sprintf("/v1/vaults/%s/items", b.vaultID)
		if err := b.apiDo(ctx, http.MethodPost, path, body, &created); err != nil {
			return fmt.Errorf("onepassword backend: create %q: %w", key, err)
		}
		if created.ID != "" {
			b.cache.Store(title, created.ID)
		}
	}
	return nil
}

// Delete removes the Connect item for key. Returns nil if the item does not
// exist (idempotent).
func (b *Backend) Delete(ctx context.Context, key string) error {
	title := b.itemTitle(key)

	itemID, err := b.findItemID(ctx, title)
	if err != nil {
		return fmt.Errorf("onepassword backend: delete %q: %w", key, err)
	}
	if itemID == "" {
		return nil
	}

	path := fmt.Sprintf("/v1/vaults/%s/items/%s", b.vaultID, itemID)
	if err := b.apiDo(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("onepassword backend: delete %q: %w", key, err)
	}
	b.cache.Delete(title)
	return nil
}

// HealthCheck verifies connectivity by listing vaults.
func (b *Backend) HealthCheck(ctx context.Context) error {
	var vaults []opVault
	if err := b.apiGet(ctx, "/v1/vaults", &vaults); err != nil {
		return fmt.Errorf("onepassword backend: health check: %w", err)
	}
	return nil
}

// ─── HTTP helpers ────────────────────────────────────────────────────────────

func (b *Backend) baseURL() string {
	return strings.TrimRight(b.cfg.Host, "/")
}

// apiGet performs a GET request and JSON-decodes the response into dest.
func (b *Backend) apiGet(ctx context.Context, path string, dest any) error {
	return b.apiDo(ctx, http.MethodGet, path, nil, dest)
}

// apiDo performs an HTTP request with an optional JSON body, decoding the
// response into dest when dest is non-nil.
func (b *Backend) apiDo(ctx context.Context, method, path string, body any, dest any) error {
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, b.baseURL()+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errNotFound(path)
	}
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("http %s %s: status %d: %s", method, path, resp.StatusCode, msg)
	}
	if dest != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// errNotFound wraps backend.ErrNotFound with context so Is() still works.
func errNotFound(key string) error {
	// Import-cycle-safe: use the package-level variable directly.
	// The file is in the onepassword package so we reference it via the
	// backend import.
	return fmt.Errorf("onepassword backend: %q: %w", key, backendErrNotFound)
}
