package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/file"
	"github.com/zach-source/ccswitch/internal/codex"
)

const ctEmail = "me@example.com"

// ctAccess builds an access token that expires at exp.
func ctAccess(tag string, exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." +
		enc([]byte(fmt.Sprintf(`{"exp":%d,"tag":"%s"}`, exp.Unix(), tag))) + ".sig"
}

// ctSession builds a session auth.json.
func ctSession(email, access, refresh string) string {
	return `{"auth_mode":"chatgpt","last_refresh":"2026-01-01T00:00:00Z","tokens":{"access_token":"` + access +
		`","refresh_token":"` + refresh + `","id_token":"` + codexIDToken(email) + `","account_id":"acct-1"}}`
}

func ctSeed(t *testing.T, home string, exp time.Time, refresh string) string {
	t.Helper()
	access := ctAccess("old", exp)
	seedCred(t, home, codexTokenKey(ctEmail), []byte(ctSession(ctEmail, access, refresh)))
	return access
}

type ctServer struct {
	refreshes, revokes int32
	revokeBody         atomic.Value
}

// ctEndpoints stands up refresh and revoke endpoints. A refresh answers with
// status and, on 200, a new access token and refresh token.
func ctEndpoints(t *testing.T, status int) *ctServer {
	t.Helper()
	s := &ctServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.refreshes, 1)
		w.WriteHeader(status)
		if status == 200 {
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"new-rt"}`, ctAccess("new", time.Now().Add(240*time.Hour)))
		}
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.revokes, 1)
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.revokeBody.Store(in)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CCSWITCH_CODEX_REFRESH_URL", srv.URL+"/token")
	t.Setenv("CCSWITCH_CODEX_REVOKE_URL", srv.URL+"/revoke")
	return s
}

func ctAuth(t *testing.T, data []byte) (access, refresh string) {
	t.Helper()
	var b struct {
		Tokens struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, data)
	}
	return b.Tokens.Access, b.Tokens.Refresh
}

func TestTokenCodexCreate_MatchSavesSession(t *testing.T) {
	home := newTestHome(t)
	live := codexBlob("live-rt", "live@example.com")
	codexHome := newCodexHome(t, live)
	blob := ctSession("ME@Example.com", ctAccess("a", time.Now().Add(240*time.Hour)), "rt-secret")
	fakeLoginEnv(t, blob, 0, ctEmail)

	out, err := capture(t, func() error { return run(t, "token", "create", "--codex", ctEmail) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Saved a Codex token session for me@example.com (access token valid until ") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, "rt-secret") {
		t.Error("output holds a token")
	}
	if got := readCred(t, home, "Codex-Token-me@example.com"); string(got) != blob {
		t.Errorf("stored = %q", got)
	}
	if _, err := os.Stat(codexRegistryPath()); err == nil {
		t.Error("the registry was written")
	}
	if after, _ := os.ReadFile(filepath.Join(codexHome, "auth.json")); string(after) != live {
		t.Error("the live login changed")
	}
}

func TestTokenCodexCreate_MismatchSavesNothing(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, "")
	fakeLoginEnv(t, ctSession("other@example.com", ctAccess("a", time.Now().Add(time.Hour)), "rt"), 0, ctEmail)

	err := run(t, "token", "create", "--codex", ctEmail)
	if err == nil || !strings.Contains(err.Error(), "other@example.com") {
		t.Fatalf("err = %v", err)
	}
	if got := readCred(t, home, codexTokenKey(ctEmail)); got != nil {
		t.Errorf("saved %q", got)
	}
}

func TestTokenCodexGet_FreshTokenMakesNoRequest(t *testing.T) {
	home := newTestHome(t)
	srv := ctEndpoints(t, 200)
	access := ctSeed(t, home, time.Now().Add(5*24*time.Hour), "rt-secret")

	out, err := capture(t, func() error { return run(t, "token", "get", "--codex", ctEmail) })
	if err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&srv.refreshes); n != 0 {
		t.Errorf("%d refresh requests, want 0", n)
	}
	gotAccess, gotRefresh := ctAuth(t, []byte(out))
	if gotAccess != access || gotRefresh != "" || strings.Contains(out, "rt-secret") {
		t.Errorf("output = %s", out)
	}
}

func TestTokenCodexGet_RenewsNearExpiryOnce(t *testing.T) {
	home := newTestHome(t)
	srv := ctEndpoints(t, 200)
	ctSeed(t, home, time.Now().Add(2*time.Hour), "rt-secret")

	out, err := capture(t, func() error { return run(t, "token", "get", "--codex", ctEmail) })
	if err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&srv.refreshes); n != 1 {
		t.Errorf("%d refresh requests, want 1", n)
	}
	gotAccess, gotRefresh := ctAuth(t, []byte(out))
	if !strings.Contains(gotAccess, ".") || gotRefresh != "" || strings.Contains(out, "new-rt") {
		t.Errorf("output = %s", out)
	}
	if exp, _ := codex.AccessTokenExpiry([]byte(out)); time.Until(exp) < 9*24*time.Hour {
		t.Errorf("output holds the old access token")
	}
	_, storedRT := ctAuth(t, readCred(t, home, codexTokenKey(ctEmail)))
	if storedRT != "new-rt" {
		t.Errorf("stored refresh token = %q, want new-rt", storedRT)
	}
}

func TestTokenCodexGet_PermanentErrorAsksForCreate(t *testing.T) {
	home := newTestHome(t)
	ctEndpoints(t, 401)
	ctSeed(t, home, time.Now().Add(time.Hour), "rt-secret")

	err := run(t, "token", "get", "--codex", ctEmail)
	want := "the token session for me@example.com has ended; run `ccswitch token create --codex me@example.com`"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestTokenCodexGet_TransientErrorKeepsStoredSession(t *testing.T) {
	home := newTestHome(t)
	ctEndpoints(t, 500)
	ctSeed(t, home, time.Now().Add(time.Hour), "rt-secret")
	before := readCred(t, home, codexTokenKey(ctEmail))

	err := run(t, "token", "get", "--codex", ctEmail)
	if err == nil || errors.Is(err, codex.ErrSessionEnded) || strings.Contains(err.Error(), "rt-secret") {
		t.Fatalf("err = %v", err)
	}
	if after := readCred(t, home, codexTokenKey(ctEmail)); string(after) != string(before) {
		t.Error("the stored session changed")
	}
}

// A temporary renewal failure must not stop a CI job while the current
// access token still has hours left.
func TestTokenCodexGet_TransientErrorUsesValidToken(t *testing.T) {
	home := newTestHome(t)
	ctEndpoints(t, 500)
	access := ctSeed(t, home, time.Now().Add(20*time.Hour), "rt-secret")

	out, err := capture(t, func() error { return run(t, "token", "get", "--codex", ctEmail) })
	if err != nil {
		t.Fatalf("get with a valid token and a 500 renewal: %v", err)
	}
	if gotAccess, _ := ctAuth(t, []byte(out)); gotAccess != access {
		t.Errorf("output does not hold the current access token")
	}
}

// ctFailingWrites is a backend whose Write fails.
type ctFailingWrites struct{ backend.Backend }

func (ctFailingWrites) Write(context.Context, string, []byte) error { return errors.New("store down") }

func TestTokenCodexGet_StoreWriteFailureSavesLocalCopy(t *testing.T) {
	home := newTestHome(t)
	ctEndpoints(t, 200)
	store := file.New(filepath.Join(t.TempDir(), "store"))
	if err := store.Write(context.Background(), codexTokenKey(ctEmail),
		[]byte(ctSession(ctEmail, ctAccess("old", time.Now().Add(time.Hour)), "rt-secret"))); err != nil {
		t.Fatal(err)
	}
	old := codexStoreFunc
	codexStoreFunc = func() (backend.Backend, error) { return ctFailingWrites{store}, nil }
	t.Cleanup(func() { codexStoreFunc = old })

	if _, err := capture(t, func() error { return run(t, "token", "get", "--codex", ctEmail) }); err != nil {
		t.Fatal(err)
	}
	if _, rt := ctAuth(t, readCred(t, home, codexTokenKey(ctEmail))); rt != "new-rt" {
		t.Errorf("local copy refresh token = %q, want new-rt", rt)
	}
}

func TestTokenCodexGet_ConcurrentGetsRefreshOnce(t *testing.T) {
	home := newTestHome(t)
	srv := ctEndpoints(t, 200)
	ctSeed(t, home, time.Now().Add(time.Hour), "rt-secret")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir := filepath.Join(t.TempDir(), "h")
			root := Root()
			root.SetArgs([]string{"token", "get", "--codex", ctEmail, "--codex-home", dir})
			errs[i] = root.Execute()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := atomic.LoadInt32(&srv.refreshes); n != 1 {
		t.Errorf("%d refresh requests, want 1", n)
	}
}

func TestTokenCodexGet_CodexHomeWritesPrivateFile(t *testing.T) {
	home := newTestHome(t)
	ctEndpoints(t, 200)
	ctSeed(t, home, time.Now().Add(5*24*time.Hour), "rt-secret")
	dir := filepath.Join(t.TempDir(), "ci", "codex")

	out, err := capture(t, func() error { return run(t, "token", "get", "--codex", ctEmail, "--codex-home", dir) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	fi, err := os.Stat(filepath.Join(dir, "auth.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json: %v %v", fi, err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", di.Mode().Perm())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "auth.json"))
	if _, rt := ctAuth(t, data); rt != "" {
		t.Error("the file holds a refresh token")
	}
}

func TestTokenCodexGet_CodexHomeNeedsCodexFlag(t *testing.T) {
	newTestHome(t)
	seqWith(t, "id1", ctEmail)
	if err := run(t, "token", "get", ctEmail, "--codex-home", t.TempDir()); err == nil {
		t.Error("want an error")
	}
}

func TestTokenList_ShowsBothKindsWithoutTokenText(t *testing.T) {
	home := newTestHome(t)
	seqWith(t, "id1", ctEmail)
	seedCred(t, home, "Claude-Token-"+ctEmail, []byte(`{"token":"`+testToken+`","createdAt":"2026-01-01T00:00:00Z"}`))
	seedUsageProfiles(t, home, map[string]string{"me": codexBlob("x", ctEmail)}, []string{"me"})
	access := ctSeed(t, home, time.Now().Add(5*24*time.Hour), "rt-secret")

	out, err := capture(t, func() error { return run(t, "token", "list") })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ctEmail + "  created 2026-01-01", "codex  " + ctEmail + "  access token valid until ", "last renewed 2026-01-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	for _, secret := range []string{testToken, "rt-secret", access} {
		if strings.Contains(out, secret) {
			t.Errorf("output holds token text %q", secret)
		}
	}
}

func TestTokenCodexRemove_RevokesAndDeletes(t *testing.T) {
	home := newTestHome(t)
	srv := ctEndpoints(t, 200)
	ctSeed(t, home, time.Now().Add(5*24*time.Hour), "rt-secret")

	if err := run(t, "token", "remove", "--codex", ctEmail); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.revokeBody.Load().(map[string]string)
	if atomic.LoadInt32(&srv.revokes) != 1 || body["token"] != "rt-secret" {
		t.Errorf("revoke calls = %d, body = %v", srv.revokes, body)
	}
	if got := readCred(t, home, codexTokenKey(ctEmail)); got != nil {
		t.Errorf("session still stored: %q", got)
	}
}

func TestRefreshAll_RenewsNearExpiryCodexSessionsOnly(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)
	seedCred(t, home, account.ActiveCredKey,
		[]byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":99999999999999}}`))
	seedUsageProfiles(t, home, map[string]string{"me": codexBlob("x", ctEmail), "fresh": codexBlob("y", "fresh@example.com")}, []string{"me", "fresh"})
	srv := ctEndpoints(t, 200)
	ctSeed(t, home, time.Now().Add(time.Hour), "rt-secret")
	seedCred(t, home, codexTokenKey("fresh@example.com"),
		[]byte(ctSession("fresh@example.com", ctAccess("f", time.Now().Add(5*24*time.Hour)), "fresh-rt")))

	if err := run(t, "refresh-all"); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&srv.refreshes); n != 1 {
		t.Errorf("%d refresh requests, want 1", n)
	}
	if _, rt := ctAuth(t, readCred(t, home, codexTokenKey(ctEmail))); rt != "new-rt" {
		t.Errorf("refresh token = %q, want new-rt", rt)
	}
	if _, rt := ctAuth(t, readCred(t, home, codexTokenKey("fresh@example.com"))); rt != "fresh-rt" {
		t.Errorf("fresh session changed: %q", rt)
	}
}

func TestRefreshAll_CodexFailureFailsTheRun(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	seedClaudeJSON(t, home, alice)
	seqWith(t, account.HashEmail(alice), alice)
	seedCred(t, home, account.ActiveCredKey,
		[]byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":99999999999999}}`))
	seedUsageProfiles(t, home, map[string]string{"me": codexBlob("x", ctEmail)}, []string{"me"})
	ctEndpoints(t, 500)
	ctSeed(t, home, time.Now().Add(time.Hour), "rt-secret")

	if err := run(t, "refresh-all"); err == nil {
		t.Error("want an error")
	}
}
