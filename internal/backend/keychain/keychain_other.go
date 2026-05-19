//go:build !darwin

// Package keychain provides a stub on non-Darwin platforms so that the package
// compiles everywhere. All operations return ErrNotSupported (declared in
// errors.go).
package keychain

import (
	"context"
	"time"

	"github.com/zach-source/ccswitch/internal/backend"
)

// Backend is an unsupported stub on non-Darwin builds.
type Backend struct{}

// New always returns an instance; all methods return ErrNotSupported.
func New() *Backend { return &Backend{} }

// Name implements backend.Backend.
func (b *Backend) Name() string { return "keychain" }

// Read implements backend.Backend.
func (b *Backend) Read(_ context.Context, _ string) ([]byte, error) {
	return nil, ErrNotSupported
}

// Write implements backend.Backend.
func (b *Backend) Write(_ context.Context, _ string, _ []byte) error {
	return ErrNotSupported
}

// Delete implements backend.Backend.
func (b *Backend) Delete(_ context.Context, _ string) error {
	return ErrNotSupported
}

// HealthCheck implements backend.Backend.
func (b *Backend) HealthCheck(_ context.Context) error {
	return ErrNotSupported
}

// LookupHashedActiveSlot is unsupported on non-Darwin platforms — claude
// 2.x's per-CLAUDE_CONFIG_DIR hashed keychain item is a macOS-only mechanism.
func (b *Backend) LookupHashedActiveSlot(_ context.Context, _ time.Time) ([]byte, error) {
	return nil, ErrNotSupported
}

// Compile-time interface check.
var _ backend.Backend = (*Backend)(nil)
