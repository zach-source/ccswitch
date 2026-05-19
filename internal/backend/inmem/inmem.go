// Package inmem provides an in-memory Backend implementation for testing.
// It is safe for concurrent use.
package inmem

import (
	"context"
	"sync"

	"github.com/zach-source/ccswitch/internal/backend"
)

// Backend is a map-backed in-memory store. All operations are O(1).
type Backend struct {
	mu    sync.RWMutex
	store map[string][]byte
}

// New returns an empty in-memory Backend.
func New() *Backend {
	return &Backend{store: make(map[string][]byte)}
}

func (b *Backend) Name() string { return "inmem" }

func (b *Backend) Read(_ context.Context, key string) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.store[key]
	if !ok {
		return nil, backend.ErrNotFound
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (b *Backend) Write(_ context.Context, key string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	b.store[key] = cp
	return nil
}

func (b *Backend) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.store, key)
	return nil
}

func (b *Backend) HealthCheck(_ context.Context) error { return nil }
