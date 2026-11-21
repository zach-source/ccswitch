// Package grok manages Grok Build CLI logins the same way the codex package
// manages Codex ones: one live slot that gets swapped, with a copy of each
// account parked in a ccswitch backend.
//
// Grok keeps its login in $GROK_HOME/auth.json (default ~/.grok), mode 0600.
// The file is an object keyed "<issuer>::<oidc_client_id>". A normal login has
// one entry:
//
//	{"https://auth.x.ai::<client id>": {
//	  "key": "<access token JWT>", "refresh_token": "…",
//	  "expires_at": "2026-11-27T14:54:00.123456Z", "email": "…",
//	  "auth_mode": "oidc", "oidc_issuer": "https://auth.x.ai",
//	  "oidc_client_id": "<client id>", …}}
//
// Fields this package does not know stay as they are when it rewrites a file.
package grok

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
)

const (
	// AuthFile is Grok's credential file inside GROK_HOME.
	AuthFile = "auth.json"
	// lockFile is the lock Grok takes around its own refresh and write.
	lockFile = "auth.json.lock"
	// issuerPrefix starts the key of the normal login entry.
	issuerPrefix = "https://auth.x.ai::"
)

// Home returns Grok's root directory: $GROK_HOME when set, else ~/.grok.
func Home() string {
	if h := os.Getenv("GROK_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".grok")
}

// AuthPath returns the live credential path.
func AuthPath() string { return filepath.Join(Home(), AuthFile) }

// CredKey returns the backend key holding a profile's captured credential.
func CredKey(label string) string { return "Grok-Account-" + label }

// ReadLive returns the live credential bytes verbatim.
func ReadLive() ([]byte, error) {
	data, err := os.ReadFile(AuthPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, provider.ErrNoLiveLogin
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", AuthPath(), err)
	}
	if len(data) == 0 {
		return nil, provider.ErrNoLiveLogin
	}
	return data, nil
}

// WriteLive replaces the live credential atomically at mode 0600, under the
// same file lock Grok takes around its refresh. A Grok that is refreshing
// then finishes first, and its write cannot land on top of ours.
func WriteLive(data []byte) error {
	dir := Home()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open auth lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("take auth lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	// The temp file is created 0600 and not chmod'd later, so a loose umask
	// never opens a readable window. Rename is atomic: Grok reads either the
	// whole old file or the whole new one.
	tmp, err := os.CreateTemp(dir, ".auth-*.json")
	if err != nil {
		return fmt.Errorf("create temp credential: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temp credential: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp credential: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flush temp credential: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp credential: %w", err)
	}
	if err := os.Rename(tmp.Name(), AuthPath()); err != nil {
		return fmt.Errorf("install credential: %w", err)
	}
	return nil
}

// session is an auth.json that keeps every field it does not know, at the top
// level and inside the entry.
type session struct {
	top      map[string]json.RawMessage
	entryKey string
	entry    map[string]json.RawMessage
}

// parseSession finds the login entry. It prefers the key of the normal login
// (issuer::client id); else it takes the first key, in sorted order, whose
// value holds an access token.
func parseSession(blob []byte) (*session, error) {
	s := &session{}
	if err := json.Unmarshal(blob, &s.top); err != nil || s.top == nil {
		return nil, errors.New("the credential is not valid JSON")
	}
	keys := make([]string, 0, len(s.top))
	for k := range s.top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, preferred := range []bool{true, false} {
		for _, k := range keys {
			if preferred && !strings.HasPrefix(k, issuerPrefix) {
				continue
			}
			var e map[string]json.RawMessage
			if json.Unmarshal(s.top[k], &e) != nil || e["key"] == nil {
				continue
			}
			s.entryKey, s.entry = k, e
			return s, nil
		}
	}
	return nil, errors.New("the credential has no login entry")
}

func (s *session) get(name string) string {
	var v string
	_ = json.Unmarshal(s.entry[name], &v)
	return v
}

func (s *session) set(name, value string) {
	s.entry[name], _ = json.Marshal(value)
}

func (s *session) bytes() ([]byte, error) {
	var err error
	if s.top[s.entryKey], err = json.Marshal(s.entry); err != nil {
		return nil, err
	}
	return json.MarshalIndent(s.top, "", "  ")
}

// Describe decodes the identity worth showing. A blob it cannot read yields a
// zero Identity. The plan comes from the usage call, not from the file.
func Describe(blob []byte) provider.Identity {
	s, err := parseSession(blob)
	if err != nil {
		return provider.Identity{}
	}
	return provider.Identity{Mode: s.get("auth_mode"), Email: s.get("email"), AccountID: s.get("user_id")}
}

// MatchProfile names the saved profile that the live login belongs to, by
// email. Anything other than exactly one candidate is no match: guessing
// would save one account's rotated tokens over another's profile.
func MatchProfile(reg *provider.Registry, live []byte) (string, bool) {
	id := Describe(live)
	if id.Email == "" {
		return "", false
	}
	var match string
	var n int
	for label, p := range reg.Profiles {
		if !strings.EqualFold(p.Email, id.Email) {
			continue
		}
		if p.AccountID != "" && id.AccountID != "" && p.AccountID != id.AccountID {
			continue
		}
		match = label
		n++
	}
	if n != 1 {
		return "", false
	}
	return match, true
}

// Expiry returns when the access token expires, from expires_at.
func Expiry(blob []byte) (time.Time, bool) {
	s, err := parseSession(blob)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s.get("expires_at"))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// LastRefresh uses expires_at as the freshness key: every renewal pushes it
// later, so the copy that expires later is the newer one. create_time is the
// sign-in time and is not known to change on a refresh.
func LastRefresh(blob []byte) (time.Time, bool) { return Expiry(blob) }
