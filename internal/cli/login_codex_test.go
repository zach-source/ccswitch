package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/codex"
)

// fakeCodexQueue puts a fake `codex` on PATH. Call n writes blobs[n-1] as the
// temporary login. It also points the Chrome lookup at a state file with no
// profiles. The returned function reports how many sign-ins ran.
func fakeCodexQueue(t *testing.T, blobs ...string) (calls func() int) {
	t.Helper()
	dir := t.TempDir()
	for i, b := range blobs {
		if err := os.WriteFile(filepath.Join(dir, "blob."+strconv.Itoa(i+1)), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"n=$(cat '" + dir + "/n' 2>/dev/null || echo 0)\n" +
		"n=$((n+1))\n" +
		"echo $n > '" + dir + "/n'\n" +
		"printf '1. Open this link in your browser and sign in to your account\\n'\n" +
		"printf '   https://auth.example.test/codex/device\\n'\n" +
		"printf '2. Enter this one-time code (expires in 15 minutes)\\n'\n" +
		"printf '   ABCD-12345\\n'\n" +
		"cp '" + dir + "/blob.'$n \"$CODEX_HOME/auth.json\" || exit 1\n"
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	state := filepath.Join(dir, "Local State")
	if err := os.WriteFile(state, []byte(`{"profile":{"info_cache":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCSWITCH_CHROME_LOCAL_STATE", state)
	return func() int {
		data, err := os.ReadFile(filepath.Join(dir, "n"))
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		return n
	}
}

const apiKeyBlob = `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`

// loginCodexFixture seeds four profiles in priority order: s is stale, x is
// expired (the server answers 401), o is ok and k is an API key. o is live.
func loginCodexFixture(t *testing.T, home string) map[string]string {
	t.Helper()
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	blobs := map[string]string{
		"s": usageBlob("s@example.com", past, "2026-10-02T10:00:00Z"),
		"x": usageBlob("x@example.com", future, "2026-10-20T10:00:00Z"),
		"o": usageBlob("o@example.com", future, "2026-10-20T10:00:00Z"),
		"k": apiKeyBlob,
	}
	seedUsageProfiles(t, home, blobs, []string{"s", "x", "o", "k"})
	newCodexHome(t, blobs["o"])
	usageServer(t, map[string]func(http.ResponseWriter){
		"acct-x@example.com": usageStatus(http.StatusUnauthorized),
		"acct-o@example.com": usageJSON(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":1792000000}}}`),
	})
	return blobs
}

func credString(t *testing.T, home, label string) string {
	t.Helper()
	return string(readCred(t, home, codex.CredKey(label)))
}

func TestLoginCodex_StaleAndExpiredSignInOkAndAPIKeyDoNot(t *testing.T) {
	home := newTestHome(t)
	blobs := loginCodexFixture(t, home)
	calls := fakeCodexQueue(t, codexBlob("new-s", "s@example.com"), codexBlob("new-x", "x@example.com"))

	out, err := capture(t, func() error { return run(t, "login", "--provider", "codex") })
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if calls() != 2 {
		t.Errorf("sign-ins = %d, want 2", calls())
	}
	if !strings.Contains(credString(t, home, "s"), "new-s") || !strings.Contains(credString(t, home, "x"), "new-x") {
		t.Error("stale and expired profiles were not signed in again")
	}
	if credString(t, home, "o") != blobs["o"] || credString(t, home, "k") != blobs["k"] {
		t.Error("an ok or API-key profile changed")
	}
	if !strings.Contains(out, "Codex: 2 profiles signed in again") {
		t.Errorf("summary missing:\n%s", out)
	}
}

func TestLoginCodex_ForceSelectsEveryNonAPIKeyProfile(t *testing.T) {
	home := newTestHome(t)
	blobs := loginCodexFixture(t, home)
	calls := fakeCodexQueue(t, codexBlob("new-s", "s@example.com"), codexBlob("new-x", "x@example.com"), codexBlob("new-o", "o@example.com"))

	if err := run(t, "login", "--provider", "codex", "--force"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if calls() != 3 {
		t.Errorf("sign-ins = %d, want 3", calls())
	}
	if !strings.Contains(credString(t, home, "o"), "new-o") {
		t.Error("the ok profile was not signed in again")
	}
	if credString(t, home, "k") != blobs["k"] {
		t.Error("the API-key profile changed")
	}
}

func TestLoginCodex_OnlyMatchesLabelOrEmail(t *testing.T) {
	for _, only := range []string{"s", "S@EXAMPLE.COM"} {
		t.Run(only, func(t *testing.T) {
			home := newTestHome(t)
			loginCodexFixture(t, home)
			calls := fakeCodexQueue(t, codexBlob("new-s", "s@example.com"))

			if err := run(t, "login", "--only", only); err != nil {
				t.Fatalf("login: %v", err)
			}
			if calls() != 1 || !strings.Contains(credString(t, home, "s"), "new-s") {
				t.Errorf("sign-ins = %d, s = %q", calls(), credString(t, home, "s"))
			}
		})
	}
}

func TestLoginCodex_OnlyMatchingNothingFails(t *testing.T) {
	home := newTestHome(t)
	loginCodexFixture(t, home)
	if err := run(t, "login", "--only", "nobody"); err == nil {
		t.Fatal("want an error when --only matches no Claude account and no Codex profile")
	}
}

func TestLoginCodex_RejectsUnknownProvider(t *testing.T) {
	newTestHome(t)
	if err := run(t, "login", "--provider", "gemini"); err == nil {
		t.Fatal("want an error for an unknown provider")
	}
}

func TestLoginCodex_ProviderSelectsOnePass(t *testing.T) {
	setup := func(t *testing.T) (home, marker string, calls func() int) {
		home = newTestHome(t)
		loginCodexFixture(t, home)
		alice := "alice@example.com"
		seedClaudeJSON(t, home, alice)
		seqWith(t, account.HashEmail(alice), alice)
		bins := withFakeBins(t)
		marker = filepath.Join(t.TempDir(), "claude-ran")
		fakeBin(t, bins, "claude", "touch '"+marker+"'\n"+
			fakeClaudeWriting(`{"claudeAiOauth":{"accessToken":"AT","refreshToken":"RT","expiresAt":99999999999999}}`))
		calls = fakeCodexQueue(t, codexBlob("new-s", "s@example.com"), codexBlob("new-x", "x@example.com"))
		return home, marker, calls
	}
	t.Run("codex skips Claude", func(t *testing.T) {
		_, marker, calls := setup(t)
		if err := run(t, "login", "--provider", "codex"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("the Claude pass ran")
		}
		if calls() != 2 {
			t.Errorf("sign-ins = %d, want 2", calls())
		}
	})
	t.Run("claude skips Codex", func(t *testing.T) {
		_, marker, calls := setup(t)
		if err := run(t, "login", "--provider", "claude"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Error("the Claude pass did not run")
		}
		if calls() != 0 {
			t.Errorf("sign-ins = %d, want 0", calls())
		}
	})
}

func TestLoginCodex_OnlyMatchingCodexLeavesClaudeAlone(t *testing.T) {
	home := newTestHome(t)
	loginCodexFixture(t, home)
	alice := "alice@example.com"
	seedClaudeJSON(t, home, alice)
	seqWith(t, account.HashEmail(alice), alice)
	fakeCodexQueue(t, codexBlob("new-s", "s@example.com"))

	if err := run(t, "login", "--only", "s"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(credString(t, home, "s"), "new-s") {
		t.Error("the Codex profile was not signed in again")
	}
}

func TestLoginCodex_LiveProfileGetsNewLoginInAuthJSON(t *testing.T) {
	home := newTestHome(t)
	dead := usageBlob("s@example.com", time.Now().Add(-time.Hour), "2026-10-02T10:00:00Z")
	seedUsageProfiles(t, home, map[string]string{"s": dead}, []string{"s"})
	codexHome := newCodexHome(t, dead)
	fresh := codexBlob("new-s", "s@example.com")
	fakeCodexQueue(t, fresh)

	if err := run(t, "login", "--provider", "codex"); err != nil {
		t.Fatalf("login: %v", err)
	}
	live, _ := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if string(live) != fresh {
		t.Error("the live auth.json does not hold the new login")
	}
	if credString(t, home, "s") != fresh {
		t.Error("the stored profile is not the new login")
	}
	if reg := readCodexRegistry(t); reg.Active != "s" {
		t.Errorf("Active = %q, want s", reg.Active)
	}
}

func TestLoginCodex_MismatchSavesNothingAndFails(t *testing.T) {
	home := newTestHome(t)
	blobs := loginCodexFixture(t, home)
	calls := fakeCodexQueue(t, codexBlob("other-rt", "other@example.com"), codexBlob("new-x", "x@example.com"))

	out, err := capture(t, func() error { return run(t, "login", "--provider", "codex") })
	if err == nil {
		t.Fatal("want a non-zero exit when a sign-in failed")
	}
	if calls() != 2 {
		t.Errorf("sign-ins = %d, want 2 (a failure must not stop the next profile)", calls())
	}
	if credString(t, home, "s") != blobs["s"] {
		t.Error("a mismatched login was saved")
	}
	if !strings.Contains(credString(t, home, "x"), "new-x") {
		t.Error("the next profile was not signed in again")
	}
	if !strings.Contains(out, "other@example.com") {
		t.Errorf("output does not name the failure:\n%s", out)
	}
}

func TestLoginCodex_UnavailableIsSkippedWithANote(t *testing.T) {
	home := newTestHome(t)
	future := time.Now().Add(time.Hour)
	blobs := map[string]string{"u": usageBlob("u@example.com", future, "2026-10-20T10:00:00Z")}
	seedUsageProfiles(t, home, blobs, []string{"u"})
	newCodexHome(t, "")
	usageServer(t, map[string]func(http.ResponseWriter){"acct-u@example.com": usageStatus(http.StatusInternalServerError)})
	calls := fakeCodexQueue(t)

	out, err := capture(t, func() error { return run(t, "login", "--provider", "codex") })
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSI(out)
	if calls() != 0 || !strings.Contains(out, "u (unavailable)") || !strings.Contains(out, "1 skipped (unavailable)") {
		t.Errorf("sign-ins = %d, output:\n%s", calls(), out)
	}
}

func TestLoginCodex_NoProfilesPrintsNothing(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, "")
	out, err := capture(t, func() error { return run(t, "login", "--provider", "codex") })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Codex") {
		t.Errorf("unexpected Codex output:\n%s", out)
	}
}

func TestLoginCodex_ManualPrintsAddressAndOpensNoChrome(t *testing.T) {
	home := newTestHome(t)
	loginCodexFixture(t, home)
	fakeCodexQueue(t, codexBlob("new-s", "s@example.com"))
	// A Chrome profile signed in as s@example.com would get the address without --manual.
	dir := t.TempDir()
	chromeArgs := filepath.Join(dir, "chrome-args")
	chrome := filepath.Join(dir, "chrome")
	if err := os.WriteFile(chrome, []byte("#!/bin/sh\necho \"$@\" > '"+chromeArgs+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCSWITCH_CHROME_BINARY", chrome)
	state := filepath.Join(dir, "Local State")
	if err := os.WriteFile(state, []byte(`{"profile":{"info_cache":{"Profile 1":{"name":"W","user_name":"s@example.com"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCSWITCH_CHROME_LOCAL_STATE", state)

	out, err := capture(t, func() error { return run(t, "login", "--provider", "codex", "--only", "s", "--manual") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "https://auth.example.test/codex/device") {
		t.Errorf("address not printed:\n%s", out)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(chromeArgs); err == nil {
		t.Error("Chrome was opened with --manual")
	}
}
