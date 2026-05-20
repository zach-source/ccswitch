// Package backend defines the storage interface every credential backend
// (file, keychain, 1Password Connect, Vault) implements. The unified API
// lets the CLI and sync loop work without caring where credentials live.
package backend

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Read when the requested item does not exist.
// Callers distinguish "no such cred" from "backend failure" by checking
// errors.Is(err, ErrNotFound).
var ErrNotFound = errors.New("credential not found")

// Backend is the storage contract. Items are addressed by string key — the
// caller decides naming conventions (e.g. "Claude Code-credentials" for the
// active slot, "Claude Code Account - <hash>-<email>" for per-account
// backups in 1Password). Implementations must be safe for concurrent use
// across goroutines.
type Backend interface {
	// Name returns a short identifier ("file", "keychain", "1password", "vault")
	// for logging and config display.
	Name() string

	// Read returns the credential blob bytes. Returns (nil, ErrNotFound)
	// if the item does not exist; (nil, err) on backend failures.
	Read(ctx context.Context, key string) ([]byte, error)

	// Write stores a credential blob. Overwrites any existing value at the
	// same key.
	Write(ctx context.Context, key string, data []byte) error

	// Delete removes a credential blob. Returns nil if the item already
	// doesn't exist (idempotent).
	Delete(ctx context.Context, key string) error

	// HealthCheck verifies the backend is reachable and authorized.
	// Used by `--config` and at daemon startup.
	HealthCheck(ctx context.Context) error
}

// Type identifies a backend kind for config-driven selection.
type Type string

const (
	TypeAuto           Type = "auto"
	TypeFile           Type = "file"
	TypeKeychain       Type = "keychain"
	TypeOnePassword    Type = "1password"
	TypeOnePasswordCLI Type = "1password-cli"
	TypeVault          Type = "vault"
)
