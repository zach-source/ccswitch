package codex_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zach-source/ccswitch/internal/codex"
)

// jwt builds an unsigned JWT whose payload carries the given claims. Only the
// payload segment is ever read, so the header and signature are filler.
func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

func TestWriteLiveRoundTripsAndIsOwnerOnly(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex"))

	if _, err := codex.ReadLive(); err != codex.ErrNoLiveLogin {
		t.Fatalf("ReadLive on an empty home = %v, want ErrNoLiveLogin", err)
	}

	blob := []byte(`{"auth_mode":"chatgpt","tokens":{"refresh_token":"rt"}}`)
	if err := codex.WriteLive(blob); err != nil {
		t.Fatalf("WriteLive: %v", err)
	}

	got, err := codex.ReadLive()
	if err != nil {
		t.Fatalf("ReadLive: %v", err)
	}
	if string(got) != string(blob) {
		t.Errorf("round trip changed the bytes:\n got %s\nwant %s", got, blob)
	}

	// The file holds a live refresh token; a loose umask must not widen it.
	info, err := os.Stat(codex.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("auth.json mode = %o, want 600", mode)
	}
}

// A replace must leave no temp file behind — the directory holds credentials.
func TestWriteLiveLeavesNoTempFiles(t *testing.T) {
	home := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", home)

	for _, body := range []string{`{"a":1}`, `{"b":2}`} {
		if err := codex.WriteLive([]byte(body)); err != nil {
			t.Fatalf("WriteLive: %v", err)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != codex.AuthFile {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("CODEX_HOME contains %v, want just %s", names, codex.AuthFile)
	}
}

func TestDescribeReadsChatGPTIdentity(t *testing.T) {
	blob := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"account_id": "acct-from-tokens",
			"id_token": jwt(t, map[string]any{
				"email": "dev@example.com",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_plan_type":  "pro",
					"chatgpt_account_id": "acct-from-claims",
				},
			}),
		},
	}
	data, err := json.Marshal(blob)
	if err != nil {
		t.Fatal(err)
	}

	got := codex.Describe(data)
	if got.Email != "dev@example.com" {
		t.Errorf("Email = %q, want dev@example.com", got.Email)
	}
	if got.Plan != "pro" {
		t.Errorf("Plan = %q, want pro", got.Plan)
	}
	if got.Mode != "chatgpt" {
		t.Errorf("Mode = %q, want chatgpt", got.Mode)
	}
	if got.AccountID != "acct-from-claims" {
		t.Errorf("AccountID = %q, want the id_token claim to win", got.AccountID)
	}
}

// The profile claim is the fallback when the top-level email is absent.
func TestDescribeFallsBackToProfileEmail(t *testing.T) {
	data := []byte(`{"tokens":{"id_token":"` + jwt(t, map[string]any{
		"https://api.openai.com/profile": map[string]any{"email": "fallback@example.com"},
	}) + `"}}`)

	if got := codex.Describe(data).Email; got != "fallback@example.com" {
		t.Errorf("Email = %q, want fallback@example.com", got)
	}
}

func TestDescribeInfersAPIKeyMode(t *testing.T) {
	if got := codex.Describe([]byte(`{"OPENAI_API_KEY":"sk-test"}`)).Mode; got != "apikey" {
		t.Errorf("Mode = %q, want apikey", got)
	}
}

// Describe is display-only: anything unreadable degrades to a blank identity
// rather than failing, because a profile is still usable unlabelled.
func TestDescribeToleratesGarbage(t *testing.T) {
	for _, in := range []string{``, `not json`, `{"tokens":{"id_token":"not.a.jwt"}}`, `{"tokens":{"id_token":"only-one-segment"}}`} {
		if got := codex.Describe([]byte(in)); got != (codex.Identity{}) {
			t.Errorf("Describe(%q) = %+v, want a zero Identity", in, got)
		}
	}
}

func TestIsFileBackedReadsConfig(t *testing.T) {
	cases := []struct {
		name      string
		config    string
		wantFile  bool
		wantStore string
	}{
		{"no config at all", "", true, ""},
		{"explicit file", "cli_auth_credentials_store = \"file\"\n", true, "file"},
		{"keyring", "cli_auth_credentials_store = \"keyring\"\n", false, "keyring"},
		{"commented out", "# cli_auth_credentials_store = \"keyring\"\n", true, ""},
		{"among other keys", "model = \"gpt-5\"\ncli_auth_credentials_store = \"keyring\"\n", false, "keyring"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "codex")
			t.Setenv("CODEX_HOME", home)
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			gotFile, gotStore := codex.IsFileBacked()
			if gotFile != tc.wantFile || gotStore != tc.wantStore {
				t.Errorf("IsFileBacked() = (%v, %q), want (%v, %q)", gotFile, gotStore, tc.wantFile, tc.wantStore)
			}
		})
	}
}

func TestHomeHonoursCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/somewhere/else")
	if got := codex.AuthPath(); got != filepath.Join("/somewhere/else", "auth.json") {
		t.Errorf("AuthPath() = %q", got)
	}
}
