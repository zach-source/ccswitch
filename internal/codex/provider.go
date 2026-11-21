package codex

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
)

// Shared types: the codex package keeps its names, the seam owns the shape.
type (
	Identity = provider.Identity
	Window   = provider.Window
	Usage    = provider.Usage
)

// kindError keeps its own message and also matches a provider sentinel, so
// Codex text stays the same while generic code tests the shared error.
type kindError struct {
	msg  string
	kind error
}

func (e *kindError) Error() string        { return e.msg }
func (e *kindError) Is(target error) bool { return target == e.kind }

// Provider is Codex as a provider.Provider.
//
// RegistryFile is the registry path and LoginLock the sign-in lock path. Both
// live under the ccswitch backup dir, which this package does not know.
type Provider struct {
	RegistryFile string
	LoginLock    string
}

var _ provider.Provider = Provider{}

func (Provider) Name() string           { return "codex" }
func (Provider) CredKeyPrefix() string  { return CredKey("") }
func (p Provider) RegistryPath() string { return p.RegistryFile }

func (Provider) ReadLive() ([]byte, error) { return ReadLive() }
func (Provider) WriteLive(b []byte) error  { return WriteLive(b) }

// Describe marks an API-key login as static: it has nothing to renew.
func (Provider) Describe(b []byte) provider.Identity {
	id := Describe(b)
	id.Static = id.Mode == "apikey"
	return id
}

func (Provider) LiveLocation() string { return AuthPath() }

// Match applies the MatchProfile rule: the account ID, and the email when
// both sides carry one.
func (Provider) Match(reg *provider.Registry, live []byte) (string, bool) {
	return MatchProfile(reg, live)
}

func (Provider) Expiry(b []byte) (time.Time, bool) { return AccessTokenExpiry(b) }

func (Provider) Refresh(ctx context.Context, b []byte) ([]byte, error) { return Refresh(ctx, b) }

func (Provider) LastRefresh(b []byte) (time.Time, bool) { return LastRefresh(b) }

// Revoke does nothing for an API key or a login with no refresh token.
func (Provider) Revoke(ctx context.Context, b []byte) error {
	if Describe(b).Mode == "apikey" || !HasRefreshToken(b) {
		return nil
	}
	return Revoke(ctx, b)
}

// FetchUsage reads the access token and account ID from the login. An API key
// has no usage.
func (Provider) FetchUsage(ctx context.Context, blob []byte) (provider.Usage, []byte, error) {
	if Describe(blob).Mode == "apikey" {
		return provider.Usage{}, nil, provider.ErrNotSupported
	}
	var t struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(blob, &t)
	u, err := FetchUsage(ctx, t.Tokens.AccessToken, Describe(blob).AccountID)
	return u, nil, err
}

func (p Provider) Login(ctx context.Context, onReady func(provider.LoginPrompt)) ([]byte, error) {
	return SignIn(ctx, p.LoginLock, func(d DeviceLogin) {
		onReady(provider.LoginPrompt{URL: d.URL, Code: d.Code, Browser: d.BrowserFallback})
	})
}
