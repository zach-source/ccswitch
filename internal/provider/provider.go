// Package provider is the seam between ccswitch and the CLIs it swaps logins
// for. Codex, Grok and Antigravity each keep one live login slot. ccswitch
// parks a copy of every account in a backend and writes one copy into the
// live slot. A Provider hides where that slot is and how a login renews.
//
// Every method treats the login as opaque bytes.
package provider

import (
	"context"
	"errors"
	"strconv"
	"time"
)

var (
	// ErrNoLiveLogin means the live slot holds no login.
	ErrNoLiveLogin = errors.New("no live login")
	// ErrNotSupported means the provider does not offer the operation, for
	// example refresh for a provider whose stored logins never expire.
	ErrNotSupported = errors.New("not supported by this provider")
	// ErrSessionEnded means the server will never accept this refresh token
	// again. The user must sign in again.
	ErrSessionEnded = errors.New("the session has ended")
	// ErrUsageRejected means the server refused the access token. A retry
	// cannot help; the user must sign in again.
	ErrUsageRejected = errors.New("usage request rejected, sign in again")
)

// Identity describes a login for display and for matching.
type Identity struct {
	Mode      string // for example "chatgpt" or "apikey"; "" when absent
	Email     string
	Plan      string
	AccountID string
}

// Window is one rate-limit window: how much of it the account has used and
// when it resets.
type Window struct {
	Seconds     int       // window length; 0 when the provider does not say
	UsedPercent float64   // 0-100
	ResetAt     time.Time // zero when unknown
	Label       string    // overrides the name derived from Seconds
}

// Name gives the short label of the window: Label when set, else "5h",
// "weekly", or the length in hours or minutes.
func (w Window) Name() string {
	switch {
	case w.Label != "":
		return w.Label
	case w.Seconds == 18000:
		return "5h"
	case w.Seconds == 604800:
		return "weekly"
	case w.Seconds < 3600:
		return strconv.Itoa(w.Seconds/60) + "m"
	}
	return strconv.Itoa(w.Seconds/3600) + "h"
}

// Usage is the rate-limit state of one account. Plans can have one window
// only, so Windows holds 0, 1 or more entries.
type Usage struct {
	Plan    string
	Windows []Window
}

// Load is the highest UsedPercent across the windows, or 0 with no windows.
// The switcher uses it to rank accounts: the fullest window limits the account.
func (u Usage) Load() float64 {
	var max float64
	for _, w := range u.Windows {
		if w.UsedPercent > max {
			max = w.UsedPercent
		}
	}
	return max
}

// LoginPrompt tells the user how to finish a sign-in.
type LoginPrompt struct {
	URL     string // the verification address
	Code    string // the one-time code
	Browser bool   // the CLI opened the default browser itself
}

// Provider is one CLI whose login ccswitch swaps.
type Provider interface {
	Name() string          // "codex", "grok", "antigravity"
	CredKeyPrefix() string // backend key prefix, for example "Codex-Account-"
	RegistryPath() string

	// ReadLive returns the live login, or ErrNoLiveLogin when it is empty.
	ReadLive() ([]byte, error)
	WriteLive([]byte) error

	Describe([]byte) Identity
	// Same tells whether two logins belong to the same account.
	Same(a, b []byte) bool
	// Expiry returns when the access token expires, when it is readable.
	Expiry([]byte) (time.Time, bool)

	// Refresh renews the login. It may return ErrNotSupported or
	// ErrSessionEnded.
	Refresh(ctx context.Context, blob []byte) ([]byte, error)
	// RotatesRefreshToken tells whether a refresh spends the old refresh
	// token. If so, never renew the live login: its CLI does that itself.
	RotatesRefreshToken() bool

	// FetchUsage may return ErrUsageRejected or ErrNotSupported.
	FetchUsage(ctx context.Context, blob []byte) (Usage, error)
	// Login signs in a new account without touching the live login and
	// returns its login bytes. onReady runs once the prompt is known. It may
	// return ErrNotSupported.
	Login(ctx context.Context, onReady func(LoginPrompt)) ([]byte, error)
}
