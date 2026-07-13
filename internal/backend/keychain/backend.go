//go:build darwin

// Package keychain implements a macOS Keychain credential backend using
// Security.framework directly via github.com/keybase/go-keychain, eliminating
// the argv-exposure risk of the `security` CLI subprocess.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"strings"
	"time"

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
//
// Reads go through /usr/bin/security rather than Security.framework directly.
// The keychain records an "Always Allow" grant against the *calling binary's*
// code identity, but a Nix-built ccswitch is ad-hoc/linker-signed with no
// stable designated requirement, so macOS can never persist a grant to it —
// every direct read re-prompts for the login-keychain password, forever, and
// worse on every rebuild (new /nix/store path). /usr/bin/security is an Apple
// platform binary with a stable identity, so a single "Always Allow" for it
// sticks across ccswitch updates. The secret is returned on stdout, never in
// argv, so this keeps the argv-exposure protection that motivated go-keychain
// (only the non-secret service/account names appear in the command line).
func (b *Backend) Read(_ context.Context, key string) ([]byte, error) {
	acct, err := currentUser()
	if err != nil {
		return nil, err
	}
	return readViaSecurityCLI(key, acct)
}

// readViaSecurityCLI shells out to `security find-generic-password -w`. It
// assumes the stored blob is text (ccswitch only stores JSON credentials);
// `security -w` emits a text password followed by a single trailing newline,
// which is stripped. Exit status 44 is errSecItemNotFound.
func readViaSecurityCLI(service, account string) ([]byte, error) {
	cmd := exec.Command("/usr/bin/security", "find-generic-password",
		"-s", service, "-a", account, "-w")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return nil, backend.ErrNotFound
		}
		return nil, fmt.Errorf("keychain backend: security read %q: %w: %s",
			service, err, strings.TrimSpace(stderr.String()))
	}
	// `security -w` appends exactly one newline to the emitted password; the
	// stored credential blob has none, so remove a single trailing \n.
	return bytes.TrimSuffix(stdout.Bytes(), []byte("\n")), nil
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

// LookupHashedActiveSlot returns the data AND the service name of a
// generic-password keychain item whose service starts with "Claude
// Code-credentials-" and whose modification date is at or after since.
//
// claude 2.x writes the per-CLAUDE_CONFIG_DIR isolated active credential
// to a keychain service of the form "Claude Code-credentials-<8hex>",
// where the suffix is an opaque hash of internal config state. ccswitch
// cannot mirror that hashing — and crucially the suffix changes between
// runs even for the same CONFIG_DIR path — so each login that captures
// from this slot leaves a distinct orphaned record behind. The caller
// uses the returned service name to delete that orphan after persisting
// the credential to the backend.
//
// The freshly-written item is found by enumeration + modification-date
// filter. Returns ("", nil, nil) when no matching item exists. The
// most-recently-modified match is returned when several qualify.
func (b *Backend) LookupHashedActiveSlot(ctx context.Context, since time.Time) ([]byte, string, error) {
	acct, err := currentUser()
	if err != nil {
		return nil, "", err
	}

	// Security.framework rejects MatchLimitAll combined with both
	// SetReturnAttributes and SetReturnData (errSecParam, -50). Do this in
	// two steps: enumerate to find the freshest matching service name, then
	// Read its data via the standard single-item path.
	q := gokeychain.NewItem()
	q.SetSecClass(gokeychain.SecClassGenericPassword)
	q.SetAccount(acct)
	q.SetMatchLimit(gokeychain.MatchLimitAll)
	q.SetReturnAttributes(true)

	results, err := gokeychain.QueryItem(q)
	if err != nil {
		if err == gokeychain.ErrorItemNotFound { //nolint:errorlint
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("keychain backend: enumerate generic passwords: %w", err)
	}

	// Allow a small clock-skew tolerance — keychain mdat is second-grained.
	floor := since.Add(-2 * time.Second)

	var newestSvc string
	var newestMdat time.Time
	var found bool
	for _, r := range results {
		if !strings.HasPrefix(r.Service, "Claude Code-credentials-") {
			continue
		}
		if r.ModificationDate.Before(floor) {
			continue
		}
		if !found || r.ModificationDate.After(newestMdat) {
			newestSvc = r.Service
			newestMdat = r.ModificationDate
			found = true
		}
	}
	if !found {
		return nil, "", nil
	}
	data, err := b.Read(ctx, newestSvc)
	if err != nil {
		return nil, "", err
	}
	return data, newestSvc, nil
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
