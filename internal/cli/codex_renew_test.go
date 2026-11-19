package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/codex"
)

// crSeedClaude makes refresh-all find one healthy Claude account.
func crSeedClaude(t *testing.T, home string) {
	t.Helper()
	alice := "alice@example.com"
	seedClaudeJSON(t, home, alice)
	seqWith(t, account.HashEmail(alice), alice)
	seedCred(t, home, account.ActiveCredKey,
		[]byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":99999999999999}}`))
}

// crProfile builds a profile credential that expires after left.
func crProfile(email, tag string, left time.Duration, refresh string) string {
	return ctSession(email, ctAccess(tag, time.Now().Add(left)), refresh)
}

func TestRefreshAll_RenewsStoredCodexProfilesThatAreNotLive(t *testing.T) {
	home := newTestHome(t)
	crSeedClaude(t, home)
	blobs := map[string]string{
		"live": crProfile("live@example.com", "live", time.Hour, "live-rt"),
		"near": crProfile("near@example.com", "near", time.Hour, "near-rt"),
		"far":  crProfile("far@example.com", "far", 5*24*time.Hour, "far-rt"),
		"key":  `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`,
	}
	seedUsageProfiles(t, home, blobs, []string{"live", "near", "far", "key"})
	newCodexHome(t, blobs["live"])
	srv := ctEndpoints(t, 200)

	if err := run(t, "refresh-all"); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&srv.refreshes); n != 1 {
		t.Errorf("%d refresh requests, want 1", n)
	}
	for label, want := range map[string]string{"near": "new-rt", "live": "live-rt", "far": "far-rt"} {
		if _, rt := ctAuth(t, readCred(t, home, codex.CredKey(label))); rt != want {
			t.Errorf("profile %s refresh token = %q, want %q", label, rt, want)
		}
	}
	if got := string(readCred(t, home, codex.CredKey("key"))); got != blobs["key"] {
		t.Errorf("API-key profile changed: %q", got)
	}
}

func TestRefreshAll_CopiesTheLiveCodexLoginIntoItsProfile(t *testing.T) {
	home := newTestHome(t)
	crSeedClaude(t, home)
	stored := crProfile("live@example.com", "old", 5*24*time.Hour, "old-rt")
	live := crProfile("live@example.com", "rotated", 6*24*time.Hour, "rotated-rt")
	seedUsageProfiles(t, home, map[string]string{"live": stored}, []string{"live"})
	newCodexHome(t, live)
	ctEndpoints(t, 200)

	if err := run(t, "refresh-all"); err != nil {
		t.Fatal(err)
	}
	if got := string(readCred(t, home, codex.CredKey("live"))); got != live {
		t.Errorf("profile was not copied from the live login: %q", got)
	}
}

func TestCodexUsage_RenewsExpiredNonLiveProfileAndSkipsLive(t *testing.T) {
	home := newTestHome(t)
	blobs := map[string]string{
		"a": crProfile("a@example.com", "a", time.Hour, "a-rt"),
		"b": crProfile("b@example.com", "b", -time.Hour, "b-rt"),
	}
	seedUsageProfiles(t, home, blobs, []string{"a", "b"})
	newCodexHome(t, blobs["a"])
	srv := ctEndpoints(t, 200)
	body := `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}}`
	usageServer(t, map[string]func(http.ResponseWriter){
		"acct-a@example.com": usageJSON(body),
		"acct-b@example.com": usageJSON(body),
	})

	out, err := runOut(t, "codex", "usage", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []codexUsageRowJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, r := range rows {
		if r.Status != usageOK {
			t.Errorf("profile %s status = %q, want ok", r.Label, r.Status)
		}
	}
	if n := atomic.LoadInt32(&srv.refreshes); n != 1 {
		t.Errorf("%d refresh requests, want 1 (the live profile never refreshes)", n)
	}
	if _, rt := ctAuth(t, readCred(t, home, codex.CredKey("b"))); rt != "new-rt" {
		t.Errorf("profile b refresh token = %q, want new-rt", rt)
	}
	if _, rt := ctAuth(t, readCred(t, home, codex.CredKey("a"))); rt != "a-rt" {
		t.Errorf("live profile changed: %q", rt)
	}
}

func TestCodexRemove_RevokesAndDeletesNonLiveProfile(t *testing.T) {
	home := newTestHome(t)
	blobs := map[string]string{
		"a": crProfile("a@example.com", "a", 5*24*time.Hour, "a-rt"),
		"b": crProfile("b@example.com", "b", 5*24*time.Hour, "b-rt"),
	}
	seedUsageProfiles(t, home, blobs, []string{"a", "b"})
	newCodexHome(t, blobs["a"])
	srv := ctEndpoints(t, 200)

	if err := run(t, "codex", "remove", "b"); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.revokeBody.Load().(map[string]string)
	if atomic.LoadInt32(&srv.revokes) != 1 || body["token"] != "b-rt" {
		t.Errorf("revoke calls = %d, body = %v", srv.revokes, body)
	}
	if got := readCred(t, home, codex.CredKey("b")); got != nil {
		t.Errorf("credential still stored: %q", got)
	}
}

func TestCodexRemove_DoesNotRevokeTheLiveLogin(t *testing.T) {
	home := newTestHome(t)
	blobs := map[string]string{"a": crProfile("a@example.com", "a", 5*24*time.Hour, "a-rt")}
	seedUsageProfiles(t, home, blobs, []string{"a"})
	newCodexHome(t, blobs["a"])
	srv := ctEndpoints(t, 200)

	if err := run(t, "codex", "remove", "a"); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&srv.revokes); n != 0 {
		t.Errorf("%d revoke requests, want 0 for the live login", n)
	}
	if got := readCred(t, home, codex.CredKey("a")); got != nil {
		t.Errorf("credential still stored: %q", got)
	}
}

func TestCodexUse_WaitsForTheAutoswitchLock(t *testing.T) {
	home := newTestHome(t)
	blobs := map[string]string{
		"a": crProfile("a@example.com", "a", 5*24*time.Hour, "a-rt"),
		"b": crProfile("b@example.com", "b", 5*24*time.Hour, "b-rt"),
	}
	seedUsageProfiles(t, home, blobs, []string{"a", "b"})
	newCodexHome(t, blobs["a"])
	old := codexUseLockWait
	codexUseLockWait = 100 * time.Millisecond
	t.Cleanup(func() { codexUseLockWait = old })

	unlock, locked, err := lockFile("codex-autoswitch.lock")
	if err != nil || !locked {
		t.Fatalf("lock: %v, %v", locked, err)
	}
	if err := run(t, "codex", "use", "b"); err == nil || !strings.Contains(err.Error(), "codex-autoswitch.lock") {
		t.Errorf("codex use with the lock held: err = %v", err)
	}
	unlock()
	if err := run(t, "codex", "use", "b"); err != nil {
		t.Errorf("codex use after the lock is free: %v", err)
	}
}

// An unreadable live login could be any profile. refresh-all must then renew
// no profile, so it cannot rotate the tokens that Codex uses.
func TestRefreshAll_UnreadableLiveLoginRenewsNoProfile(t *testing.T) {
	home := newTestHome(t)
	crSeedClaude(t, home)
	blobs := map[string]string{"near": crProfile("near@example.com", "near", time.Hour, "near-rt")}
	seedUsageProfiles(t, home, blobs, []string{"near"})
	codexHome := newCodexHome(t, blobs["near"])
	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.Chmod(authPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(authPath, 0o600) })
	srv := ctEndpoints(t, 200)

	if err := run(t, "refresh-all"); err == nil {
		t.Error("want an error for an unreadable live login")
	}
	if n := atomic.LoadInt32(&srv.refreshes); n != 0 {
		t.Errorf("%d refresh requests, want 0", n)
	}
}
