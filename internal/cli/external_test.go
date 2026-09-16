package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
)

// ─── fake external binaries ─────────────────────────────────────────────────
//
// The commands below shell out to `claude`, `ccusage`, and `op`. Every call
// site resolves the binary by name through PATH, so a temp dir of fake
// executables prepended to PATH lets the conformance suite exercise these
// commands without the real tools, the network, or a browser login.

// withFakeBins prepends a fresh temp dir to PATH and returns it.
func withFakeBins(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// fakeBin writes an executable shell script named name into dir.
func fakeBin(t *testing.T, dir, name, script string) {
	t.Helper()
	body := "#!/usr/bin/env bash\nset -e\n" + script
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeClaudeWriting returns a fake `claude` that writes credBody to the
// credential file inside the isolated CLAUDE_CONFIG_DIR — what a real
// browser login would produce. It serves both `claude auth login`
// (RefreshOne) and bare `claude` (LoginRotate); both set CLAUDE_CONFIG_DIR.
func fakeClaudeWriting(credBody string) string {
	return "cat > \"$CLAUDE_CONFIG_DIR/.credentials.json\" <<'CRED'\n" + credBody + "\nCRED\n"
}

// ─── usage (mocks ccusage) ──────────────────────────────────────────────────

func TestUsage_RendersCcusageOutput(t *testing.T) {
	home := newTestHome(t)
	bins := withFakeBins(t)
	seedClaudeJSON(t, home, "alice@example.com")
	seqWith(t, account.HashEmail("alice@example.com"), "alice@example.com")

	fakeBin(t, bins, "ccusage", `
case "$1" in
  blocks) echo '{"blocks":[{"totalTokens":166400000,"costUSD":1.5,"burnRate":{"costPerHour":0.3},"tokenLimitStatus":{"limit":837800000,"percentUsed":57},"startTime":"2020-01-01T00:00:00Z","endTime":"2020-01-01T05:00:00Z"}]}' ;;
  weekly) echo '{"weekly":[{"week":"2020-W01","totalTokens":3000000000,"totalCost":5.95,"modelsUsed":["claude-opus-4-7-20251101"]}]}' ;;
esac
`)

	out, err := capture(t, func() error { return run(t, "usage") })
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	for _, want := range []string{"5-Hour Block", "57%", "166.4M", "Weekly Usage"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage output missing %q:\n%s", want, out)
		}
	}
}

func TestUsage_NoCcusageInstalled(t *testing.T) {
	newTestHome(t)
	// A PATH with no ccusage anywhere on it — exec.LookPath must fail.
	t.Setenv("PATH", t.TempDir())
	if err := run(t, "usage"); err == nil {
		t.Fatal("expected an error when ccusage is not installed")
	}
}

// ─── refresh-all / login (mock claude) ──────────────────────────────────────

// fakeTokenEndpoint stands up an OAuth token endpoint and points ccswitch at
// it via CLAUDE_CODE_CUSTOM_OAUTH_URL — the same override a staging user
// would set. httptest serves on loopback, which the transport check allows.
func fakeTokenEndpoint(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oauth/token" {
			t.Errorf("token request to %s, want /v1/oauth/token", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLAUDE_CODE_CUSTOM_OAUTH_URL", srv.URL)
}

func TestRefreshAll_RefreshesExpiredAccount(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)

	fakeTokenEndpoint(t, 200, `{"access_token":"fresh-AT","refresh_token":"fresh-RT","expires_in":3600}`)

	// The stored blob carries an unmodeled field and account-independent MCP
	// state — proving refresh splices into the raw bytes rather than
	// rebuilding them from the typed struct.
	expired := []byte(`{"mcpOAuth":{"srv":"tok"},"claudeAiOauth":{"accessToken":"old","refreshToken":"old-RT","expiresAt":1,"futureField":"preserved"}}`)
	seedCred(t, home, account.BackupCredKey(aliceID, alice), expired)

	if err := run(t, "refresh-all"); err != nil {
		t.Fatalf("refresh-all: %v", err)
	}
	got := string(readCred(t, home, account.BackupCredKey(aliceID, alice)))
	if !strings.Contains(got, "fresh-AT") {
		t.Fatalf("account was not refreshed:\n%s", got)
	}
	if !strings.Contains(got, "futureField") {
		t.Fatalf("refresh dropped an unmodeled field — data loss:\n%s", got)
	}
	if !strings.Contains(got, "mcpOAuth") {
		t.Fatalf("refresh dropped account-independent MCP state:\n%s", got)
	}
}

// refresh-all must work with no `claude` binary on PATH at all: it is the
// unattended path (cron, launchd, the daemon) and speaks OAuth directly.
func TestRefreshAll_NeedsNoClaudeBinary(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)
	t.Setenv("PATH", t.TempDir()) // empty PATH: no claude anywhere

	fakeTokenEndpoint(t, 200, `{"access_token":"fresh-AT","expires_in":3600}`)
	seedCred(t, home, account.BackupCredKey(aliceID, alice),
		[]byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"old-RT","expiresAt":1}}`))

	if err := run(t, "refresh-all"); err != nil {
		t.Fatalf("refresh-all: %v", err)
	}
	if got := string(readCred(t, home, account.BackupCredKey(aliceID, alice))); !strings.Contains(got, "fresh-AT") {
		t.Fatalf("account was not refreshed without a claude binary:\n%s", got)
	}
}

func TestRefreshAll_ReportsFailureExitCode(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)

	// A dead refresh token — the endpoint rejects the grant.
	fakeTokenEndpoint(t, 400, `{"error":"invalid_grant"}`)
	seedCred(t, home, account.BackupCredKey(aliceID, alice),
		[]byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"old-RT","expiresAt":1}}`))

	err := run(t, "refresh-all")
	if err == nil {
		t.Fatal("refresh-all must return an error when an account fails to refresh")
	}
	if !strings.Contains(err.Error(), "failed to refresh") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLogin_AuthenticatesAccountMissingCreds(t *testing.T) {
	home := newTestHome(t)
	bins := withFakeBins(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)
	// No backup credential seeded → alice needs an interactive login.

	fakeBin(t, bins, "claude", fakeClaudeWriting(
		`{"claudeAiOauth":{"accessToken":"logged-in-AT","refreshToken":"li-RT","expiresAt":99999999999999}}`))

	if _, err := capture(t, func() error { return run(t, "login") }); err != nil {
		t.Fatalf("login: %v", err)
	}
	got := string(readCred(t, home, account.BackupCredKey(aliceID, alice)))
	if !strings.Contains(got, "logged-in-AT") {
		t.Fatalf("login did not store the captured credentials:\n%s", got)
	}
}

// ─── usage-all (mocks the OAuth usage HTTP endpoint) ────────────────────────

func TestUsageAll_QueriesOAuthEndpoint(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)

	// A non-expired credential, so usage-all does not attempt a refresh.
	future := time.Now().Add(2 * time.Hour).UnixMilli()
	seedCred(t, home, account.ActiveCredKey,
		[]byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT","refreshToken":"RT","expiresAt":%d}}`, future)))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer AT" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w,
			`{"five_hour":{"utilization":42,"resets_at":""},"seven_day":{"utilization":77,"resets_at":""}}`)
	}))
	defer srv.Close()

	prev := anthropicUsageURL
	anthropicUsageURL = srv.URL
	defer func() { anthropicUsageURL = prev }()

	out, err := capture(t, func() error { return run(t, "usage-all", "--json") })
	if err != nil {
		t.Fatalf("usage-all: %v", err)
	}
	if !strings.Contains(out, `"utilization":42`) || !strings.Contains(out, `"utilization":77`) {
		t.Fatalf("usage-all --json did not include the queried utilization:\n%s", out)
	}
}

// ─── use-zai (mocks op) ─────────────────────────────────────────────────────

func TestUseZai_WritesEnvBlock(t *testing.T) {
	newTestHome(t)
	bins := withFakeBins(t)
	// The keychain cache helper does not exist in the temp HOME, so
	// fetchZaiToken falls through to `op read`.
	fakeBin(t, bins, "op", `echo "zai-secret-token"`)

	if err := run(t, "use-zai"); err != nil {
		t.Fatalf("use-zai: %v", err)
	}
	m, ok, err := readSettings(settingsPath())
	if err != nil || !ok {
		t.Fatalf("settings.json not written: ok=%v err=%v", ok, err)
	}
	env, _ := m["env"].(map[string]any)
	if env["ANTHROPIC_AUTH_TOKEN"] != "zai-secret-token" {
		t.Errorf("z.ai token not written into the env block: %#v", env)
	}
	if env["ANTHROPIC_BASE_URL"] != zaiBaseURL {
		t.Errorf("base URL = %v, want %v", env["ANTHROPIC_BASE_URL"], zaiBaseURL)
	}
}

// ─── setup-op-connect's TOML patching ───────────────────────────────────────
//
// The full setup-op-connect flow is interactive and writes to the Keychain,
// so it is not exercised end-to-end here; its URL validation is unit-tested
// (TestValidateConnectHost) and its Keychain write goes through the
// already-tested keychain backend. This covers the remaining piece: the
// config.toml patching, round-tripped through the real config loader.

func TestPatchTOMLConnectHost(t *testing.T) {
	home := newTestHome(t)
	// Neutralize the harness's backend env overrides so config.Load reflects
	// purely what patchTOMLConnectHost wrote (env wins over TOML otherwise).
	t.Setenv("CCSWITCH_BACKEND", "")
	t.Setenv("CCSWITCH_LOCAL_BACKEND", "")
	tomlPath := filepath.Join(home, ".config", "ccswitch", "config.toml")

	if err := patchTOMLConnectHost(tomlPath, "https://op.example.com", "svc-token", "acct"); err != nil {
		t.Fatalf("patchTOMLConnectHost: %v", err)
	}

	cfg, err := config.Load(tomlPath)
	if err != nil {
		t.Fatalf("config.Load of the patched TOML: %v", err)
	}
	if cfg.Backend != "1password" {
		t.Errorf("backend type = %q, want 1password", cfg.Backend)
	}
	if cfg.OnePassword.ConnectHost != "https://op.example.com" {
		t.Errorf("connect host = %q, want https://op.example.com", cfg.OnePassword.ConnectHost)
	}
}

// ─── login / refresh-all: claude 2.x keychain capture ──────────────────────
//
// On macOS, claude 2.x writes credentials to the OS keychain regardless of
// CLAUDE_CONFIG_DIR. The harness's CCSWITCH_LOCAL_BACKEND=file points the
// "local active slot" at the file backend, so a fake `claude` that writes
// to the active-slot file simulates the keychain behavior.

// activeSlotFile returns the file-backend path of the active credential
// slot under home. Mirrors file.Backend.keyToPath for the active key.
func activeSlotFile(home string) string {
	return filepath.Join(home, ".claude-switch-backup", "credentials",
		".Claude Code-credentials.json")
}

// fakeClaudeWritingToActiveSlot returns a fake `claude` script that writes
// credBody to the local active slot — the file the file backend uses for
// the ActiveCredKey — instead of $CLAUDE_CONFIG_DIR/.credentials.json.
// This exercises the macOS-keychain-equivalent code path in
// captureClaudeCredential.
func fakeClaudeWritingToActiveSlot(credBody string) string {
	return `mkdir -p "$HOME/.claude-switch-backup/credentials"
cat > "$HOME/.claude-switch-backup/credentials/.Claude Code-credentials.json" <<'CRED'
` + credBody + `
CRED
`
}

func TestLogin_CapturesFromLocalActiveSlot(t *testing.T) {
	home := newTestHome(t)
	bins := withFakeBins(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)
	// No backup credential — alice needs login.

	// Simulate claude 2.x on macOS: writes to the keychain (= local active
	// slot under CCSWITCH_LOCAL_BACKEND=file), not to $CLAUDE_CONFIG_DIR.
	fakeBin(t, bins, "claude", fakeClaudeWritingToActiveSlot(
		`{"claudeAiOauth":{"accessToken":"keychain-style-AT","refreshToken":"li-RT","expiresAt":99999999999999}}`))

	if _, err := capture(t, func() error { return run(t, "login") }); err != nil {
		t.Fatalf("login: %v", err)
	}
	got := string(readCred(t, home, account.BackupCredKey(aliceID, alice)))
	if !strings.Contains(got, "keychain-style-AT") {
		t.Fatalf("login did not capture from the local active slot:\n%s", got)
	}
}

func TestLogin_NoCredsWhenClaudeWritesNothing(t *testing.T) {
	home := newTestHome(t)
	bins := withFakeBins(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)

	// fake claude is a no-op — simulates the user cancelling the browser
	// flow before authorizing. The local active slot is unchanged and the
	// legacy file path is empty: capture must return nothing.
	fakeBin(t, bins, "claude", `:`)

	if _, err := capture(t, func() error { return run(t, "login") }); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := readCred(t, home, account.BackupCredKey(aliceID, alice)); len(got) != 0 {
		t.Fatalf("login stored creds despite claude writing nothing:\n%s", got)
	}
}
