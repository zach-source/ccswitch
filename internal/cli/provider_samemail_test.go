package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/backend/inmem"
	"github.com/zach-source/ccswitch/internal/provider"
)

// expiredProvider has expired stored logins, counts refreshes and matches
// only a unique email, like Antigravity and Grok.
type expiredProvider struct {
	fakeProvider
	refreshes *atomic.Int32
}

func (expiredProvider) Expiry([]byte) (time.Time, bool) { return time.Now().Add(-time.Hour), true }
func (e expiredProvider) Refresh(_ context.Context, b []byte) ([]byte, error) {
	e.refreshes.Add(1)
	return b, nil
}
func (expiredProvider) Match(reg *provider.Registry, live []byte) (string, bool) {
	var label string
	n := 0
	for l, p := range reg.Profiles {
		if p.Email == string(live) {
			label, n = l, n+1
		}
	}
	return label, n == 1
}

// Two profiles share the email of the live login, so Match finds none. Either
// could be live, so no renewal may touch them.
func TestRenewal_SkipsProfilesSharingTheLiveEmail(t *testing.T) {
	newTestHome(t)
	ctx := context.Background()
	live := []byte("dup@example.com")
	var refreshes atomic.Int32
	p := expiredProvider{fakeProvider{live: &live, reg: t.TempDir() + "/r.json"}, &refreshes}
	reg := &provider.Registry{Version: provider.SchemaVersion, Profiles: map[string]provider.Profile{
		"a":     {Label: "a", Email: "dup@example.com"},
		"b":     {Label: "b", Email: "dup@example.com"},
		"other": {Label: "other", Email: "other@example.com"},
	}}
	if err := reg.Save(p.RegistryPath()); err != nil {
		t.Fatal(err)
	}
	store := inmem.New()
	for l, pr := range reg.Profiles {
		_ = store.Write(ctx, p.CredKeyPrefix()+l, []byte(pr.Email))
	}

	if n, err := renewProfiles(ctx, p, store, nil, cliLogger(true)); err != nil || n != 1 {
		t.Fatalf("renewProfiles = %d, %v; only the unrelated profile may renew", n, err)
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("renewProfiles refreshed %d logins, want 1 (other)", got)
	}
	refreshes.Store(0)
	collectProviderUsage(ctx, p, reg, store, nil, live, time.Now(), true)
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("collectProviderUsage refreshed %d logins, want 1 (other)", got)
	}
}
