//go:build darwin

// Package keychain implements a macOS Keychain credential backend using
// Security.framework directly via github.com/keybase/go-keychain, eliminating
// the argv-exposure risk of the `security` CLI subprocess.
package keychain

import (
	"context"
	"fmt"
	"os/user"

	gokeychain "github.com/keybase/go-keychain"

	"github.com/zach-source/ccswitch/internal/backend"
)

// Backend stores credentials in the login keychain. It is safe for concurrent
// use because go-keychain calls are serialised through Security.framework's own
// locking internally.
type Backend struct{}

// New returns a Backend targeting the current user's login keychain. No
// configuration is required.
func New() *Backend { return &Backend{} }

// Name implements backend.Backend.
func (b *Backend) Name() string { return "keychain" }

// currentUser returns the OS username used as the keychain account name.
func currentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("keychain backend: resolve current user: %w", err)
	}
	return u.Username, nil
}

// Read retrieves the credential blob stored under key. Returns
// backend.ErrNotFound when no matching item exists in the keychain.
func (b *Backend) Read(_ context.Context, key string) ([]byte, error) {
	acct, err := currentUser()
	if err != nil {
		return nil, err
	}

	q := gokeychain.NewItem()
	q.SetSecClass(gokeychain.SecClassGenericPassword)
	q.SetService(key)
	q.SetAccount(acct)
	q.SetMatchLimit(gokeychain.MatchLimitOne)
	q.SetReturnData(true)

	results, err := gokeychain.QueryItem(q)
	if err != nil {
		if err == gokeychain.ErrorItemNotFound { //nolint:errorlint
			return nil, backend.ErrNotFound
		}
		return nil, fmt.Errorf("keychain backend: read %q: %w", key, err)
	}
	if len(results) == 0 {
		return nil, backend.ErrNotFound
	}
	return results[0].Data, nil
}

// Write stores data under key. If an item already exists it is updated
// in-place; otherwise a new item is created (idempotent).
func (b *Backend) Write(_ context.Context, key string, data []byte) error {
	acct, err := currentUser()
	if err != nil {
		return err
	}

	item := gokeychain.NewItem()
	item.SetSecClass(gokeychain.SecClassGenericPassword)
	item.SetService(key)
	item.SetAccount(acct)
	item.SetData(data)
	item.SetAccessible(gokeychain.AccessibleWhenUnlocked)

	// Attempt add; if the item already exists, fall back to update.
	if err := gokeychain.AddItem(item); err != nil {
		if err != gokeychain.ErrorDuplicateItem { //nolint:errorlint
			return fmt.Errorf("keychain backend: add %q: %w", key, err)
		}
		// Build a query that identifies the existing item.
		query := gokeychain.NewItem()
		query.SetSecClass(gokeychain.SecClassGenericPassword)
		query.SetService(key)
		query.SetAccount(acct)

		// Build the update payload (only the data changes).
		update := gokeychain.NewItem()
		update.SetData(data)

		if err := gokeychain.UpdateItem(query, update); err != nil {
			return fmt.Errorf("keychain backend: update %q: %w", key, err)
		}
	}
	return nil
}

// Delete removes the keychain item for key. Returns nil if no item exists
// (idempotent).
func (b *Backend) Delete(_ context.Context, key string) error {
	acct, err := currentUser()
	if err != nil {
		return err
	}

	item := gokeychain.NewItem()
	item.SetSecClass(gokeychain.SecClassGenericPassword)
	item.SetService(key)
	item.SetAccount(acct)

	if err := gokeychain.DeleteItem(item); err != nil {
		if err == gokeychain.ErrorItemNotFound { //nolint:errorlint
			return nil
		}
		return fmt.Errorf("keychain backend: delete %q: %w", key, err)
	}
	return nil
}

// HealthCheck verifies that the keychain is accessible by performing a
// no-result query against the login keychain.
func (b *Backend) HealthCheck(_ context.Context) error {
	q := gokeychain.NewItem()
	q.SetSecClass(gokeychain.SecClassGenericPassword)
	q.SetService("ccswitch-health-probe")
	q.SetMatchLimit(gokeychain.MatchLimitOne)
	q.SetReturnData(false)

	_, err := gokeychain.QueryItem(q)
	// ErrorItemNotFound means the keychain is reachable but the probe item
	// doesn't exist — that's perfectly healthy.
	if err != nil && err != gokeychain.ErrorItemNotFound { //nolint:errorlint
		return fmt.Errorf("keychain backend: health check: %w", err)
	}
	return nil
}
