package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
)

// fakeProvider is a second provider with its live slot in memory. It never
// refreshes and renews its login during a usage call, like Antigravity.
type fakeProvider struct {
	live    *[]byte
	reg     string
	revoked *[]string
}

func (fakeProvider) Name() string                         { return "fake" }
func (fakeProvider) CredKeyPrefix() string                { return "Fake-Account-" }
func (f fakeProvider) RegistryPath() string               { return f.reg }
func (fakeProvider) LiveLocation() string                 { return "the fake slot" }
func (f fakeProvider) WriteLive(b []byte) error           { *f.live = b; return nil }
func (fakeProvider) Expiry([]byte) (time.Time, bool)      { return time.Time{}, false }
func (fakeProvider) LastRefresh([]byte) (time.Time, bool) { return time.Time{}, false }
func (f fakeProvider) ReadLive() ([]byte, error) {
	if len(*f.live) == 0 {
		return nil, provider.ErrNoLiveLogin
	}
	return *f.live, nil
}
func (fakeProvider) Describe(b []byte) provider.Identity { return provider.Identity{Email: string(b)} }
func (fakeProvider) Match(reg *provider.Registry, live []byte) (string, bool) {
	for l, p := range reg.Profiles {
		if p.Email == string(live) {
			return l, true
		}
	}
	return "", false
}
func (fakeProvider) Refresh(context.Context, []byte) ([]byte, error) {
	return nil, provider.ErrNotSupported
}
func (f fakeProvider) Revoke(_ context.Context, b []byte) error {
	*f.revoked = append(*f.revoked, string(b))
	return nil
}
func (fakeProvider) FetchUsage(_ context.Context, b []byte) (provider.Usage, []byte, error) {
	return provider.Usage{Windows: []provider.Window{{Label: "week", UsedPercent: 5}}}, append([]byte(nil), b...), nil
}
func (fakeProvider) Login(context.Context, func(provider.LoginPrompt)) ([]byte, error) {
	return nil, provider.ErrNotSupported
}

// TestProviderCmd_SecondProvider drives the generic commands with a provider
// that is not Codex, to show that a new provider needs only an adapter.
func TestProviderCmd_SecondProvider(t *testing.T) {
	newTestHome(t)
	var live []byte
	var revoked []string
	spec := providerSpec{
		New: func() provider.Provider {
			return fakeProvider{live: &live, reg: filepath.Join(t.TempDir(), "fake.json"), revoked: &revoked}
		},
	}
	store, err := spec.store()
	if err != nil {
		t.Fatal(err)
	}
	// New must return the same registry path on every call.
	p := spec.New()
	spec.New = func() provider.Provider { return p }

	runCmd := func(args ...string) string {
		cmd := providerCmd(spec)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("fake %v: %v", args, err)
		}
		return out.String()
	}

	live = []byte("a@example.com")
	runCmd("add", "a")
	live = []byte("b@example.com")
	runCmd("add", "b")
	runCmd("use", "a")
	if string(live) != "a@example.com" {
		t.Fatalf("live = %q, want the profile a", live)
	}
	if got, _ := store.Read(context.Background(), "Fake-Account-b"); string(got) != "b@example.com" {
		t.Errorf("the switch lost the snapshot of b: %q", got)
	}

	reg, _ := loadRegistry(p)
	rows, err := loadUsage(context.Background(), spec, time.Now())
	if err != nil || len(rows) != 2 || rows[0].Status != usageOK {
		t.Fatalf("usage rows = %+v, err %v", rows, err)
	}
	if _, err := store.Read(context.Background(), "Fake-Account-"+reg.Ordered()[0]); err != nil {
		t.Errorf("stored login missing: %v", err)
	}

	if n, err := renewProfiles(context.Background(), p, store, nil, cliLogger(true)); n != 0 || err != nil {
		t.Errorf("renewProfiles = %d, %v; a provider that cannot refresh renews nothing", n, err)
	}

	runCmd("remove", "b")
	if len(revoked) != 1 || revoked[0] != "b@example.com" {
		t.Errorf("revoked = %v, want only b", revoked)
	}
	runCmd("remove", "a")
	if len(revoked) != 1 || strings.Contains(strings.Join(revoked, ","), "a@") {
		t.Errorf("the live login a must not be revoked: %v", revoked)
	}
}
