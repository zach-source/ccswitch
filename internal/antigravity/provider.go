// Package antigravity swaps the login of agy, the Antigravity CLI. agy keeps
// one login in a macOS keychain item. ccswitch treats it as opaque bytes.
//
// agy finds its keychain through $HOME, so a temporary HOME with its own
// keychain runs agy against any stored login without touching the live one.
package antigravity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/backend/keychain"
	"github.com/zach-source/ccswitch/internal/provider"
)

// usageTimeout bounds one `agy -p /usage` run.
var usageTimeout = 90 * time.Second

// Provider is Antigravity as a provider.Provider. RegistryFile is the registry
// path, which lives under the ccswitch backup dir.
type Provider struct {
	RegistryFile string
}

var _ provider.Provider = Provider{}

func (Provider) Name() string           { return "antigravity" }
func (Provider) CredKeyPrefix() string  { return "Antigravity-Account-" }
func (p Provider) RegistryPath() string { return p.RegistryFile }
func (Provider) LiveLocation() string {
	return "the macOS keychain (service " + keychainService + ", account " + keychainAccount + ")"
}

func (Provider) ReadLive() ([]byte, error) { return readItem(context.Background(), nil) }

func (Provider) WriteLive(b []byte) error {
	return keychain.AddGenericPassword(securityBin, nil, keychainService, keychainAccount, b, "")
}

// keyringPrefix starts the value go-keyring wrote.
const keyringPrefix = "go-keyring-base64:"

// payload is the part of the decoded login that ccswitch reads. The rest of
// the login stays opaque.
type payload struct {
	Token struct {
		Expiry string `json:"expiry"`
	} `json:"token"`
	AuthMethod   string `json:"auth_method"`
	IDTokenEmail string `json:"id_token_email"`
}

// decode reads the payload. A login that does not decode gives the zero value.
func decode(blob []byte) payload {
	var p payload
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(blob), keyringPrefix))
	if err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func (Provider) Describe(b []byte) provider.Identity {
	p := decode(b)
	return provider.Identity{Email: p.IDTokenEmail, Mode: p.AuthMethod}
}

// Match finds the one profile with the email of the live login.
func (p Provider) Match(reg *provider.Registry, live []byte) (string, bool) {
	email := p.Describe(live).Email
	if email == "" {
		return "", false
	}
	var match string
	var n int
	for label, pr := range reg.Profiles {
		if strings.EqualFold(pr.Email, email) {
			match = label
			n++
		}
	}
	return match, n == 1
}

// Expiry is the expiry of the access token.
func (Provider) Expiry(b []byte) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, decode(b).Token.Expiry)
	return t, err == nil
}

// LastRefresh is the expiry too: a later one means a later refresh, so the
// newer copy of a profile wins.
func (p Provider) LastRefresh(b []byte) (time.Time, bool) { return p.Expiry(b) }

// Refresh is not needed. Google refresh tokens do not rotate, and a usage run
// renews the access token inside agy.
func (Provider) Refresh(context.Context, []byte) ([]byte, error) {
	return nil, provider.ErrNotSupported
}

func (Provider) Revoke(context.Context, []byte) error { return provider.ErrNotSupported }

// FetchUsage runs `agy -p /usage` in an isolated home that holds blob. The
// returned bytes are non-nil when agy changed the login.
func (Provider) FetchUsage(ctx context.Context, blob []byte) (provider.Usage, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	var out []byte
	var runErr error
	changed, err := withIsolatedHome(ctx, blob, func(home string) error {
		cmd := exec.CommandContext(ctx, agyBin, "-p", "/usage")
		cmd.Dir = home
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, runErr = cmd.Output()
		return nil
	})
	if err != nil {
		return provider.Usage{}, nil, err
	}
	u := ParseUsage(string(out))
	switch {
	case len(u.Windows) > 0:
		return u, changed, nil
	case runErr != nil:
		return u, changed, fmt.Errorf("agy /usage: %w", runErr)
	case strings.Contains(strings.ToLower(string(out)), "accounts.google.com"):
		return u, changed, provider.ErrUsageRejected
	}
	return u, changed, errors.New("agy /usage printed no usage rows")
}

// ParseUsage reads the TSV of `agy -p /usage`: group, window, remaining
// percent, reset time. Lines that do not parse are ignored.
func ParseUsage(out string) provider.Usage {
	var u provider.Usage
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 4 {
			continue
		}
		remaining, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(f[2]), "%"), 64)
		if err != nil {
			continue
		}
		reset, err := time.Parse(time.RFC3339, strings.TrimSpace(f[3]))
		if err != nil {
			continue
		}
		win, secs := shortWindow(f[1])
		u.Windows = append(u.Windows, provider.Window{
			Label:       shortGroup(f[0]) + " " + win,
			Seconds:     secs,
			UsedPercent: 100 - remaining,
			ResetAt:     reset,
		})
	}
	return u
}

func shortGroup(g string) string {
	switch g = strings.TrimSpace(g); {
	case strings.HasPrefix(g, "Gemini"):
		return "Gemini"
	case strings.HasPrefix(g, "Claude"):
		return "Claude/GPT"
	}
	return g
}

func shortWindow(w string) (string, int) {
	switch lw := strings.ToLower(w); {
	case strings.Contains(lw, "weekly"):
		return "weekly", 604800
	case strings.Contains(lw, "five hour"):
		return "5h", 18000
	}
	return strings.TrimSpace(strings.TrimSuffix(w, "Limit Remaining")), 0
}

// Login runs agy on the user's terminal in an isolated home with an empty
// keychain, then returns the login agy saved there.
func (Provider) Login(ctx context.Context, onReady func(provider.LoginPrompt)) ([]byte, error) {
	var runErr error
	blob, err := withIsolatedHome(ctx, nil, func(home string) error {
		onReady(provider.LoginPrompt{Message: "agy opens now. Sign in there, then exit agy (Ctrl-C or /exit)."})
		// Ctrl-C reaches ccswitch too. Ignore it so the cleanup still runs.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		defer signal.Stop(sig)
		cmd := exec.CommandContext(ctx, agyBin)
		cmd.Dir, cmd.Env = home, append(os.Environ(), "HOME="+home)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr = cmd.Run()
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(blob) == 0 {
		if runErr != nil {
			return nil, fmt.Errorf("agy ended without a login: %w", runErr)
		}
		return nil, errors.New("agy ended without a login — nothing was saved")
	}
	return blob, nil
}
