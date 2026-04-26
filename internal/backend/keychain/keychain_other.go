//go:build !darwin

// Package keychain provides a stub on non-Darwin platforms so that the package
// compiles everywhere. All operations return a "not supported" sentinel error.
package keychain

import (
	"context"
	"errors"

	"github.com/zach-source/ccswitch/internal/backend"
)

// ErrNotSupported is returned by all operations on non-macOS platforms.
var ErrNotSupported = errors.New("keychain backend: not supported on this platform")

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

// Compile-time interface check.
var _ backend.Backend = (*Backend)(nil)
