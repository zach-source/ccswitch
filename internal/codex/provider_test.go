package codex

import (
	"errors"
	"testing"

	"github.com/zach-source/ccswitch/internal/provider"
)

func TestSentinelsMatchProviderErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		shared error
	}{
		{"no live login", ErrNoLiveLogin, provider.ErrNoLiveLogin},
		{"session ended", ErrSessionEnded, provider.ErrSessionEnded},
		{"usage rejected", ErrUsageRejected, provider.ErrUsageRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !errors.Is(c.err, c.shared) || !errors.Is(c.err, c.err) {
				t.Fatalf("%v should match %v and itself", c.err, c.shared)
			}
			if errors.Is(c.err, provider.ErrNotSupported) {
				t.Fatalf("%v must not match ErrNotSupported", c.err)
			}
		})
	}
}

func TestProviderMatch(t *testing.T) {
	mk := func(account string) []byte {
		return []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"` + account + `"}}`)
	}
	reg := &provider.Registry{Profiles: map[string]provider.Profile{"work": {Label: "work", AccountID: "a"}}}
	p := Provider{}
	if label, ok := p.Match(reg, mk("a")); !ok || label != "work" {
		t.Errorf("equal account IDs should match, got %q %v", label, ok)
	}
	if _, ok := p.Match(reg, mk("b")); ok {
		t.Error("different account IDs should not match")
	}
	if _, ok := p.Match(reg, mk("")); ok {
		t.Error("an empty account ID should never match")
	}
}

func TestProviderBasics(t *testing.T) {
	p := Provider{RegistryFile: "/x/codex.json"}
	if p.Name() != "codex" || p.CredKeyPrefix() != "Codex-Account-" || p.RegistryPath() != "/x/codex.json" {
		t.Fatalf("unexpected basics: %s %s %s", p.Name(), p.CredKeyPrefix(), p.RegistryPath())
	}
}
