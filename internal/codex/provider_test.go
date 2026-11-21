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

func TestProviderSame(t *testing.T) {
	mk := func(account, email string) []byte {
		return []byte(`{"auth_mode":"apikey","tokens":{"account_id":"` + account + `"}}`)
	}
	p := Provider{}
	if !p.Same(mk("a", ""), mk("a", "")) {
		t.Error("equal account IDs should match")
	}
	if p.Same(mk("a", ""), mk("b", "")) {
		t.Error("different account IDs should not match")
	}
	if p.Same(mk("", ""), mk("", "")) {
		t.Error("empty account IDs should never match")
	}
}

func TestProviderBasics(t *testing.T) {
	p := Provider{RegistryFile: "/x/codex.json"}
	if p.Name() != "codex" || p.CredKeyPrefix() != "Codex-Account-" || p.RegistryPath() != "/x/codex.json" {
		t.Fatalf("unexpected basics: %s %s %s", p.Name(), p.CredKeyPrefix(), p.RegistryPath())
	}
	if !p.RotatesRefreshToken() {
		t.Error("Codex rotates refresh tokens")
	}
}
