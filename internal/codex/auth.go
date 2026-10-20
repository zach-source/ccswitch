// Package codex manages Codex CLI logins the same way the rest of ccswitch
// manages Claude Code's: one live slot that gets swapped, with a copy of each
// account parked in a ccswitch backend.
//
// Codex keeps its credential in $CODEX_HOME/auth.json (default ~/.codex),
// mode 0600. The shape, from the upstream AuthDotJson struct:
//
//	{
//	  "auth_mode": "chatgpt",
//	  "OPENAI_API_KEY": "sk-…",
//	  "tokens": {"id_token": "<JWT>", "access_token": "<JWT>",
//	             "refresh_token": "…", "account_id": "…"},
//	  "last_refresh": "2026-09-16T…Z"
//	}
//
// Everything here treats that blob as opaque bytes. Codex refreshes its own
// tokens and ccswitch never mints or exchanges one — the only writes are a
// whole-file save and restore, so a schema change upstream cannot corrupt a
// profile.
package codex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// AuthFile is Codex's credential file inside CODEX_HOME.
	AuthFile = "auth.json"
	// configFile holds the credential-store selection read by IsFileBacked.
	configFile = "config.toml"
)

// ErrNoLiveLogin reports that CODEX_HOME holds no auth.json to capture.
var ErrNoLiveLogin = errors.New("no Codex login found")

// Home returns Codex's root directory: $CODEX_HOME when set, else ~/.codex.
func Home() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// AuthPath returns the live credential path.
func AuthPath() string { return filepath.Join(Home(), AuthFile) }

// ReadLive returns the live credential bytes verbatim.
func ReadLive() ([]byte, error) {
	data, err := os.ReadFile(AuthPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoLiveLogin
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", AuthPath(), err)
	}
	if len(data) == 0 {
		return nil, ErrNoLiveLogin
	}
	return data, nil
}

// WriteLive replaces the live credential atomically at mode 0600.
//
// Temp file plus rename, with the mode set at creation rather than chmod'd
// afterwards so a loose umask never opens a readable window. Codex may be
// running; rename is atomic, so it reads either the whole old file or the
// whole new one, never a torn write.
func WriteLive(data []byte) error {
	dir := Home()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
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

// IsFileBacked reports whether Codex keeps its credential in auth.json rather
// than the OS keyring, along with the configured value when it is not.
//
// Recent Codex builds accept cli_auth_credentials_store = "keyring", which
// moves the credential into a keychain item named "Codex Auth" keyed by a
// hash of the canonicalised CODEX_HOME path. ccswitch cannot swap what it
// cannot address, so the commands refuse rather than silently capture a
// stale or empty auth.json. An unreadable or absent config.toml reads as
// file-backed, which is Codex's own default.
func IsFileBacked() (bool, string) {
	data, err := os.ReadFile(filepath.Join(Home(), configFile))
	if err != nil {
		return true, ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "cli_auth_credentials_store" {
			continue
		}
		store := strings.Trim(strings.TrimSpace(value), `"'`)
		return store != "keyring", store
	}
	return true, ""
}

// Identity is the human-facing description of a Codex credential, used for
// list output and for confirming that a capture grabbed the account meant.
type Identity struct {
	Mode      string // "chatgpt", "apikey", or "" when absent
	Email     string
	Plan      string
	AccountID string
}

// Describe decodes as much of a credential blob as is worth showing. Every
// field is best-effort: a blob it cannot read yields a zero Identity rather
// than an error, because a profile is still perfectly usable unlabelled.
func Describe(data []byte) Identity {
	var blob struct {
		AuthMode string `json:"auth_mode"`
		APIKey   string `json:"OPENAI_API_KEY"`
		Tokens   struct {
			IDToken   string `json:"id_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &blob) != nil {
		return Identity{}
	}

	id := Identity{Mode: blob.AuthMode, AccountID: blob.Tokens.AccountID}
	if id.Mode == "" && blob.APIKey != "" {
		id.Mode = "apikey"
	}

	claims := decodeJWTClaims(blob.Tokens.IDToken)
	id.Email = claims.Email
	if id.Email == "" {
		id.Email = claims.Profile.Email
	}
	id.Plan = claims.Auth.PlanType
	if claims.Auth.AccountID != "" {
		id.AccountID = claims.Auth.AccountID
	}
	return id
}

// idClaims mirrors the subset of Codex's IdClaims worth displaying, plus the
// exp claim that access tokens share.
type idClaims struct {
	Exp     int64  `json:"exp"`
	Email   string `json:"email"`
	Profile struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth struct {
		PlanType  string `json:"chatgpt_plan_type"`
		AccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// decodeJWTClaims reads a JWT's payload segment without verifying anything.
//
// The signature is deliberately not checked: this is display metadata for a
// credential ccswitch already holds in full, not an authorization decision.
// Verifying would mean shipping OpenAI's signing keys and buying a rotation
// problem for a string that goes in a table.
func decodeJWTClaims(jwt string) idClaims {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return idClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idClaims{}
	}
	var c idClaims
	if json.Unmarshal(payload, &c) != nil {
		return idClaims{}
	}
	return c
}

// MatchProfile names the saved profile that the live auth.json belongs to.
//
// The account ID is the key, but two users can share one ChatGPT workspace
// and so share an account ID. When both sides carry an email, it must agree
// (case-insensitively) to keep them apart. Anything other than exactly one
// candidate is no match: guessing would save one user's rotated tokens over
// another user's profile.
func MatchProfile(reg *Registry, live []byte) (string, bool) {
	id := Describe(live)
	if id.AccountID == "" {
		return "", false
	}
	var match string
	var n int
	for label, p := range reg.Profiles {
		if p.AccountID != id.AccountID {
			continue
		}
		if p.Email != "" && id.Email != "" && !strings.EqualFold(p.Email, id.Email) {
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

// AccessTokenExpiry returns when the access token expires, read from its exp
// claim without verifying the signature (see decodeJWTClaims).
func AccessTokenExpiry(blob []byte) (time.Time, bool) {
	var b struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(blob, &b) != nil {
		return time.Time{}, false
	}
	exp := decodeJWTClaims(b.Tokens.AccessToken).Exp
	if exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(exp, 0), true
}

// LastRefresh returns when Codex last refreshed the tokens, from the
// top-level last_refresh field.
func LastRefresh(blob []byte) (time.Time, bool) {
	var b struct {
		LastRefresh string `json:"last_refresh"`
	}
	if json.Unmarshal(blob, &b) != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, b.LastRefresh)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
