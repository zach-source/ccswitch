// Package antigravity swaps the login of agy, the Antigravity CLI. agy keeps
// one login in a macOS keychain item. ccswitch treats it as opaque bytes.
//
// agy finds its keychain through $HOME, so a temporary HOME with its own
// keychain runs agy against any stored login without touching the live one.
package antigravity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
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

var (
	_ provider.Provider      = Provider{}
	_ provider.IdentityLogin = Provider{}
)

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

// Describe knows nothing: the login holds no email. The registry keeps what
// the user gave at add time, and Login reads the email from the agy log.
func (Provider) Describe([]byte) provider.Identity { return provider.Identity{} }

// Match names the registry's active profile. The live login changes whenever
// agy refreshes it, so its bytes match no stored copy for long, and Match does
// not read stored copies. The rule is therefore: a live login belongs to the
// profile that `add` or `use` made active last. Signing in to another account
// inside agy by hand breaks this rule; run `add` after that.
func (Provider) Match(reg *provider.Registry, live []byte) (string, bool) {
	if _, ok := reg.Profiles[reg.Active]; ok && len(live) > 0 {
		return reg.Active, true
	}
	return "", false
}

// Expiry is unknown: the token payload format is not verified.
func (Provider) Expiry([]byte) (time.Time, bool)      { return time.Time{}, false }
func (Provider) LastRefresh([]byte) (time.Time, bool) { return time.Time{}, false }

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
func (p Provider) Login(ctx context.Context, onReady func(provider.LoginPrompt)) ([]byte, error) {
	blob, _, err := p.LoginIdentity(ctx, onReady)
	return blob, err
}

// emailLine is the agy log line that names the signed-in account.
var emailLine = regexp.MustCompile(`applyAuthResult: email=([^,\s]+)`)

// LoginIdentity is Login that also reads the email from the agy log, since
// the login itself holds none.
func (Provider) LoginIdentity(ctx context.Context, onReady func(provider.LoginPrompt)) ([]byte, provider.Identity, error) {
	var email string
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
		email = logEmail(home)
		return nil
	})
	if err != nil {
		return nil, provider.Identity{}, err
	}
	if len(blob) == 0 {
		if runErr != nil {
			return nil, provider.Identity{}, fmt.Errorf("agy ended without a login: %w", runErr)
		}
		return nil, provider.Identity{}, errors.New("agy ended without a login — nothing was saved")
	}
	return blob, provider.Identity{Email: email}, nil
}

// logEmail returns the last signed-in email in the agy logs under home, or "".
func logEmail(home string) string {
	logs, _ := filepath.Glob(filepath.Join(home, ".gemini", "antigravity-cli", "log", "*.log"))
	var email string
	for _, l := range logs {
		data, err := os.ReadFile(l)
		if err != nil {
			continue
		}
		if m := emailLine.FindAllSubmatch(data, -1); len(m) > 0 {
			email = string(m[len(m)-1][1])
		}
	}
	return email
}
