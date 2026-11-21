package grok

import (
	"context"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
)

// Provider is Grok as a provider.Provider. RegistryFile is the registry path,
// under the ccswitch backup dir, which this package does not know.
type Provider struct {
	RegistryFile string
}

var _ provider.Provider = Provider{}

func (Provider) Name() string           { return "grok" }
func (Provider) CredKeyPrefix() string  { return CredKey("") }
func (p Provider) RegistryPath() string { return p.RegistryFile }
func (Provider) LiveLocation() string   { return AuthPath() }

func (Provider) ReadLive() ([]byte, error) { return ReadLive() }
func (Provider) WriteLive(b []byte) error  { return WriteLive(b) }

func (Provider) Describe(b []byte) provider.Identity { return Describe(b) }
func (Provider) Match(reg *provider.Registry, live []byte) (string, bool) {
	return MatchProfile(reg, live)
}
func (Provider) Expiry(b []byte) (time.Time, bool)      { return Expiry(b) }
func (Provider) LastRefresh(b []byte) (time.Time, bool) { return LastRefresh(b) }

func (Provider) Refresh(ctx context.Context, b []byte) ([]byte, error) { return Refresh(ctx, b) }
func (Provider) Revoke(ctx context.Context, b []byte) error            { return Revoke(ctx, b) }

// FetchUsage never renews the login: the caller skips a stale token and
// renews stored profiles itself, as for Codex.
func (Provider) FetchUsage(ctx context.Context, blob []byte) (provider.Usage, []byte, error) {
	s, err := parseSession(blob)
	if err != nil {
		return provider.Usage{}, nil, err
	}
	u, err := FetchUsage(ctx, s.get("key"))
	return u, nil, err
}

func (Provider) Login(ctx context.Context, onReady func(provider.LoginPrompt)) ([]byte, error) {
	return SignIn(ctx, onReady)
}
