package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
	claims := `{"email":"` + email + `","https://api.openai.com/auth":{"chatgpt_plan_type":"pro","chatgpt_account_id":"acct-` + email + `"}}`
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

// fakeLoginEnv puts a fake `codex` on PATH that prints the device prompt and
// writes blob as the temporary login. It also sets up a fake Chrome whose
// profile "Profile 1" is signed in as chromeEmail, and returns the file that
// records the Chrome arguments. A zero exit code means success.
func fakeLoginEnv(t *testing.T, blob string, exitCode int, chromeEmail string) (chromeArgs string) {
	t.Helper()
	dir := t.TempDir()
	blobFile := filepath.Join(dir, "blob.json")
	if err := os.WriteFile(blobFile, []byte(blob), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"printf '1. Open this link in your browser and sign in to your account\\n'\n" +
		"printf '   https://auth.example.test/codex/device\\n'\n" +
		"printf '2. Enter this one-time code (expires in 15 minutes)\\n'\n" +
		"printf '   ABCD-12345\\n'\n" +
		"cp '" + blobFile + "' \"$CODEX_HOME/auth.json\"\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	chromeArgs = filepath.Join(dir, "chrome-args")
	chrome := filepath.Join(dir, "chrome")
	if err := os.WriteFile(chrome, []byte("#!/bin/sh\necho \"$@\" > '"+chromeArgs+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCSWITCH_CHROME_BINARY", chrome)
	state := filepath.Join(dir, "Local State")
	body := `{"profile":{"info_cache":{"Profile 1":{"name":"W","user_name":"` + chromeEmail + `"}}}}`
	if err := os.WriteFile(state, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCSWITCH_CHROME_LOCAL_STATE", state)
	return chromeArgs
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file %s was not written", path)
	return ""
}

func TestCodexLogin_MatchSavesProfileAndKeepsLiveLogin(t *testing.T) {
	home := newTestHome(t)
	live := codexBlob("live-rt", "live@example.com")
	codexHome := newCodexHome(t, live)
	chromeArgs := fakeLoginEnv(t, codexBlob("new-rt", "Work@Example.com"), 0, "work@example.com")

	if err := run(t, "codex", "login", "work@example.com"); err != nil {
		t.Fatalf("codex login: %v", err)
	}

	if !strings.Contains(string(readCred(t, home, codex.CredKey("work@example.com"))), "new-rt") {
		t.Error("the new login was not stored under the email label")
	}
	reg := readCodexRegistry(t)
	p := reg.Profiles["work@example.com"]
	if p.Email != "Work@Example.com" || p.Plan != "pro" {
		t.Errorf("profile = %+v", p)
	}
	if len(reg.Priority) != 1 || reg.Priority[0] != "work@example.com" {
		t.Errorf("Priority = %v", reg.Priority)
	}
	if got := waitForFile(t, chromeArgs); !strings.Contains(got, "--profile-directory=Profile 1") ||
		!strings.Contains(got, "https://auth.example.test/codex/device") {
		t.Errorf("chrome args = %q", got)
	}
	after, _ := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if string(after) != live {
		t.Error("the live login changed")
	}
}

func TestCodexLogin_LabelFlagAndNoDuplicatePriority(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, "")
	fakeLoginEnv(t, codexBlob("new-rt", "work@example.com"), 0, "nobody@example.com")

	for i := 0; i < 2; i++ {
		if err := run(t, "codex", "login", "work@example.com", "--label", "work"); err != nil {
			t.Fatalf("codex login: %v", err)
		}
	}
	if readCred(t, home, codex.CredKey("work")) == nil {
		t.Error("credential not stored under the --label")
	}
	if reg := readCodexRegistry(t); len(reg.Priority) != 1 {
		t.Errorf("Priority = %v, want one entry", reg.Priority)
	}
}

func TestCodexLogin_MismatchSavesNothing(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, "")
	fakeLoginEnv(t, codexBlob("other-rt", "other@example.com"), 0, "work@example.com")

	err := run(t, "codex", "login", "work@example.com")
	if err == nil || !strings.Contains(err.Error(), "other@example.com") {
		t.Fatalf("err = %v, want one naming other@example.com", err)
	}
	if readCred(t, home, codex.CredKey("work@example.com")) != nil {
		t.Error("a credential was stored")
	}
	if reg := readCodexRegistry(t); len(reg.Profiles) != 0 || len(reg.Priority) != 0 {
		t.Errorf("registry changed: %+v", reg)
	}
}

func TestCodexLogin_CodexFailureSavesNothing(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, "")
	fakeLoginEnv(t, codexBlob("rt", "work@example.com"), 1, "work@example.com")

	if err := run(t, "codex", "login", "work@example.com"); err == nil {
		t.Fatal("want an error when codex fails")
	}
	if readCred(t, home, codex.CredKey("work@example.com")) != nil {
		t.Error("a credential was stored")
	}
}

// seedCodexProfiles writes a registry holding the given labels.
func seedCodexProfiles(t *testing.T, priority []string, labels ...string) {
	t.Helper()
	reg := &codex.Registry{Version: codex.SchemaVersion, Profiles: map[string]codex.Profile{}, Priority: priority}
	for _, l := range labels {
		reg.Profiles[l] = codex.Profile{Label: l}
	}
	if err := reg.Save(codexRegistryPath()); err != nil {
		t.Fatal(err)
	}
}

func TestCodexPriority_SetsOrder(t *testing.T) {
	newTestHome(t)
	seedCodexProfiles(t, nil, "a", "b", "c")

	if err := run(t, "codex", "priority", "c", "a"); err != nil {
		t.Fatalf("codex priority: %v", err)
	}
	if got := readCodexRegistry(t).Priority; !slices.Equal(got, []string{"c", "a"}) {
		t.Errorf("Priority = %v", got)
	}
}

func TestCodexPriority_RejectsUnknownLabel(t *testing.T) {
	newTestHome(t)
	seedCodexProfiles(t, []string{"a"}, "a")

	err := run(t, "codex", "priority", "a", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want one naming ghost", err)
	}
	if got := readCodexRegistry(t).Priority; !slices.Equal(got, []string{"a"}) {
		t.Errorf("Priority changed to %v", got)
	}
}

func TestCodexPriority_PrintsOrder(t *testing.T) {
	newTestHome(t)
	seedCodexProfiles(t, []string{"c"}, "a", "b", "c")

	out, err := capture(t, func() error { return run(t, "codex", "priority") })
	if err != nil {
		t.Fatal(err)
	}
	if out != "c\na\nb\n" {
		t.Errorf("output = %q", out)
	}
}

func TestCodexRemove_DropsLabelFromPriority(t *testing.T) {
	newTestHome(t)
	seedCodexProfiles(t, []string{"a", "b"}, "a", "b")

	if err := run(t, "codex", "remove", "a"); err != nil {
		t.Fatalf("codex remove: %v", err)
	}
	if got := readCodexRegistry(t).Priority; !slices.Equal(got, []string{"b"}) {
		t.Errorf("Priority = %v", got)
	}
}
