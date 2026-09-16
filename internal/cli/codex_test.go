package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/codex"
)

// newCodexHome points CODEX_HOME at a fresh dir and seeds a live login.
// Returns the dir so tests can assert on the file directly.
func newCodexHome(t *testing.T, live string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", home)
	if live != "" {
		if err := codex.WriteLive([]byte(live)); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// codexBlob builds a credential naming an account, distinctive enough to tell
// two profiles apart in an assertion.
func codexBlob(token, email string) string {
	return `{"auth_mode":"chatgpt","tokens":{"refresh_token":"` + token +
		`","id_token":"` + codexIDToken(email) + `"}}`
}

// codexIDToken builds an unsigned JWT carrying the claims Describe reads.
func codexIDToken(email string) string {
	enc := base64.RawURLEncoding.EncodeToString
	claims := `{"email":"` + email + `","https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}}`
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(claims)) + ".sig"
}

func readCodexRegistry(t *testing.T) *codex.Registry {
	t.Helper()
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestCodexAdd_CapturesLiveLoginAndIdentity(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, codexBlob("work-rt", "work@example.com"))

	if err := run(t, "codex", "add", "work"); err != nil {
		t.Fatalf("codex add: %v", err)
	}

	stored := readCred(t, home, codex.CredKey("work"))
	if !strings.Contains(string(stored), "work-rt") {
		t.Errorf("stored credential does not hold the live token: %s", stored)
	}

	reg := readCodexRegistry(t)
	p, ok := reg.Profiles["work"]
	if !ok {
		t.Fatal("profile \"work\" was not registered")
	}
	if p.Email != "work@example.com" {
		t.Errorf("Email = %q, want work@example.com", p.Email)
	}
	if p.Plan != "pro" {
		t.Errorf("Plan = %q, want pro", p.Plan)
	}
	// The captured login is live by definition.
	if reg.Active != "work" {
		t.Errorf("Active = %q, want work", reg.Active)
	}
}

func TestCodexAdd_WithoutLiveLoginFailsLoudly(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, "")

	err := run(t, "codex", "add", "work")
	if err == nil {
		t.Fatal("capturing with no login should fail, not silently store nothing")
	}
	if !strings.Contains(err.Error(), "codex login") {
		t.Errorf("error should point at `codex login`, got: %v", err)
	}
}

// The core swap: use replaces the live file, and the login it replaces is
// saved first so a token Codex rotated is never rolled back.
func TestCodexUse_SwapsLiveAndSnapshotsThePrior(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, codexBlob("work-rt", "work@example.com"))

	if err := run(t, "codex", "add", "work"); err != nil {
		t.Fatalf("codex add work: %v", err)
	}
	// A second account logs in over the top, then gets captured too.
	if err := codex.WriteLive([]byte(codexBlob("personal-rt", "me@example.com"))); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "codex", "add", "personal"); err != nil {
		t.Fatalf("codex add personal: %v", err)
	}

	// Codex rotates its own token while "personal" is live.
	rotated := codexBlob("personal-rt-rotated", "me@example.com")
	if err := codex.WriteLive([]byte(rotated)); err != nil {
		t.Fatal(err)
	}

	if err := run(t, "codex", "use", "work"); err != nil {
		t.Fatalf("codex use work: %v", err)
	}

	live, err := codex.ReadLive()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live), "work-rt") {
		t.Errorf("live login was not swapped to work: %s", live)
	}

	// The rotation that happened under "personal" must have been captured.
	saved := readCred(t, home, codex.CredKey("personal"))
	if !strings.Contains(string(saved), "personal-rt-rotated") {
		t.Errorf("switching discarded a token Codex had rotated: %s", saved)
	}

	if got := readCodexRegistry(t).Active; got != "work" {
		t.Errorf("Active = %q, want work", got)
	}
}

func TestCodexUse_UnknownProfileFailsLoudly(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, codexBlob("rt", "me@example.com"))

	err := run(t, "codex", "use", "nope")
	if err == nil {
		t.Fatal("using an unknown profile should fail")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should name the profile, got: %v", err)
	}
}

// A keyring-backed Codex cannot be swapped by writing auth.json, so every
// command that would touch it refuses instead of silently doing nothing.
func TestCodex_RefusesWhenKeyringBacked(t *testing.T) {
	newTestHome(t)
	home := newCodexHome(t, codexBlob("rt", "me@example.com"))
	cfg := "cli_auth_credentials_store = \"keyring\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run(t, "codex", "add", "work")
	if err == nil {
		t.Fatal("capture should refuse while Codex is keyring-backed")
	}
	if !strings.Contains(err.Error(), "keyring") {
		t.Errorf("error should explain the keyring, got: %v", err)
	}
}

func TestCodexRemove_ForgetsProfileButLeavesLiveLogin(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, codexBlob("work-rt", "work@example.com"))

	if err := run(t, "codex", "add", "work"); err != nil {
		t.Fatalf("codex add: %v", err)
	}
	if err := run(t, "codex", "remove", "work"); err != nil {
		t.Fatalf("codex remove: %v", err)
	}

	if _, ok := readCodexRegistry(t).Profiles["work"]; ok {
		t.Error("profile survived removal")
	}
	if data := readCred(t, home, codex.CredKey("work")); data != nil {
		t.Errorf("stored credential survived removal: %s", data)
	}
	// Removing a profile is bookkeeping — it must not log the user out.
	live, err := codex.ReadLive()
	if err != nil || !strings.Contains(string(live), "work-rt") {
		t.Errorf("remove disturbed the live login: %v / %s", err, live)
	}
}

func TestCodexList_EmptyStateIsCleanSuccess(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, "")

	if err := run(t, "codex", "list"); err != nil {
		t.Fatalf("codex list on an empty registry: %v", err)
	}
}

// The registry holds no secrets but does name accounts; keep it owner-only.
func TestCodexRegistry_IsOwnerOnly(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, codexBlob("rt", "me@example.com"))
	if err := run(t, "codex", "add", "work"); err != nil {
		t.Fatalf("codex add: %v", err)
	}

	info, err := os.Stat(codexRegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("codex.json mode = %o, want 600", mode)
	}
}

// Guard the key format: a shared 1Password vault is read by other tools.
func TestCodexCredKeyFormat(t *testing.T) {
	if got := codex.CredKey("work"); got != "Codex-Account-work" {
		t.Errorf("CredKey = %q, want Codex-Account-work", got)
	}
}
