package codex

import (
	"context"
	"encoding/json"
	"strings"
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

func (Provider) Describe(b []byte) provider.Identity { return Describe(b) }

// Same applies the MatchProfile rule to two logins: the account IDs must be
// equal, and two emails, when both are present, must agree.
func (Provider) Same(a, b []byte) bool {
	x, y := Describe(a), Describe(b)
	if x.AccountID == "" || x.AccountID != y.AccountID {
		return false
	}
	return x.Email == "" || y.Email == "" || strings.EqualFold(x.Email, y.Email)
}

func (Provider) Expiry(b []byte) (time.Time, bool) { return AccessTokenExpiry(b) }

func (Provider) Refresh(ctx context.Context, b []byte) ([]byte, error) { return Refresh(ctx, b) }

// RotatesRefreshToken is true: Codex refresh tokens are single use.
func (Provider) RotatesRefreshToken() bool { return true }

// FetchUsage reads the access token and account ID from the login.
func (Provider) FetchUsage(ctx context.Context, blob []byte) (provider.Usage, error) {
	var t struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(blob, &t)
	return FetchUsage(ctx, t.Tokens.AccessToken, Describe(blob).AccountID)
}

func (p Provider) Login(ctx context.Context, onReady func(provider.LoginPrompt)) ([]byte, error) {
	return SignIn(ctx, p.LoginLock, func(d DeviceLogin) {
		onReady(provider.LoginPrompt{URL: d.URL, Code: d.Code, Browser: d.BrowserFallback})
	})
}
