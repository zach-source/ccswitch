// Package vault implements a HashiCorp Vault / OpenBao KV v2 credential backend.
package vault

import (
	"context"
	"errors"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/zach-source/ccswitch/internal/backend"
)

// Config holds the parameters for connecting to a Vault / OpenBao server.
type Config struct {
	// Addr is the Vault server address (e.g. "https://vault.example.com").
	Addr string
	// Token is the Vault authentication token.
	Token string
	// Path is the KV v2 mount path prefix (e.g. "secret/data/ccswitch"). Each
	// credential key is appended as a sub-path: "${Path}/${key}".
	Path string
}

// Backend is a KV v2 Vault credential store. It is safe for concurrent use
// because the underlying vault/api client is goroutine-safe.
type Backend struct {
	client *vaultapi.Client
	kv     *vaultapi.KVv2
	cfg    Config
}

// New constructs a Backend and configures the Vault client. It does not make
// any network calls; use HealthCheck to verify connectivity.
func New(cfg Config) (*Backend, error) {
	vcfg := vaultapi.DefaultConfig()
	vcfg.Address = cfg.Addr

	client, err := vaultapi.NewClient(vcfg)
	if err != nil {
		return nil, fmt.Errorf("vault backend: create client: %w", err)
	}
	client.SetToken(cfg.Token)

	// Derive the KV v2 mount from Path: everything up to (but not including)
	// the first sub-path component is the mount. We use the full path as the
	// KVv2 mount; callers should pass the mount root (e.g. "secret").
	kv := client.KVv2(cfg.Path)

	return &Backend{client: client, kv: kv, cfg: cfg}, nil
}

// Name implements backend.Backend.
func (b *Backend) Name() string { return "vault" }

// kvPath returns the full KV key path for a logical key.
func (b *Backend) kvPath(key string) string {
	return key
}

// Read fetches the "credentials" field stored at the KV path for key. Returns
// backend.ErrNotFound when the secret does not exist.
func (b *Backend) Read(ctx context.Context, key string) ([]byte, error) {
	secret, err := b.kv.Get(ctx, b.kvPath(key))
	if err != nil {
		if isNotFound(err) {
			return nil, backend.ErrNotFound
		}
		return nil, fmt.Errorf("vault backend: read %q: %w", key, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, backend.ErrNotFound
	}
	v, ok := secret.Data["credentials"]
	if !ok {
		return nil, backend.ErrNotFound
	}
	switch val := v.(type) {
	case string:
		return []byte(val), nil
	case []byte:
		return val, nil
	default:
		return nil, fmt.Errorf("vault backend: unexpected credentials type %T for %q", v, key)
	}
}

// Write stores data under the "credentials" field at the KV path for key.
// Overwrites any existing value.
func (b *Backend) Write(ctx context.Context, key string, data []byte) error {
	_, err := b.kv.Put(ctx, b.kvPath(key), map[string]interface{}{
		"credentials": string(data),
	})
	if err != nil {
		return fmt.Errorf("vault backend: write %q: %w", key, err)
	}
	return nil
}

// Delete removes the KV secret at key. Returns nil if the secret does not
// exist (idempotent).
func (b *Backend) Delete(ctx context.Context, key string) error {
	err := b.kv.Delete(ctx, b.kvPath(key))
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("vault backend: delete %q: %w", key, err)
	}
	return nil
}

// HealthCheck verifies the Vault server is reachable and the token is valid by
// calling the health endpoint.
func (b *Backend) HealthCheck(ctx context.Context) error {
	_, err := b.client.Sys().HealthWithContext(ctx)
	if err != nil {
		return fmt.Errorf("vault backend: health check: %w", err)
	}
	return nil
}

// isNotFound reports whether err from the vault/api client represents a
// missing-secret condition. The KVv2 client wraps vaultapi.ErrSecretNotFound
// (with %w) for all 404/missing-data paths, so errors.Is is the right check.
func isNotFound(err error) bool {
	return errors.Is(err, vaultapi.ErrSecretNotFound)
}
