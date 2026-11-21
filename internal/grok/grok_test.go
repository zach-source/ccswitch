package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
)

const entryKey = "https://auth.x.ai::client-1"

// fixture builds an auth.json with unknown fields at both levels.
func fixture(issuer string) []byte {
	b, _ := json.Marshal(map[string]any{
		"other_top": map[string]any{"keep": true},
		entryKey: map[string]any{
			"key": "old-access", "refresh_token": "old-refresh",
			"expires_at": "2026-01-01T00:00:00.000000Z", "email": "a@example.com",
			"user_id": "u1", "auth_mode": "oidc", "oidc_issuer": issuer,
			"oidc_client_id": "client-1", "team_id": "t1", "future_field": 42,
		},
	})
	return b
}

func TestDescribeAndMatch(t *testing.T) {
	blob := fixture("https://auth.x.ai")
	id := Describe(blob)
	if id.Email != "a@example.com" || id.Mode != "oidc" || id.Plan != "" {
		t.Fatalf("identity = %+v", id)
	}
	reg := &provider.Registry{Profiles: map[string]provider.Profile{
		"work":  {Label: "work", Email: "A@Example.com"},
		"other": {Label: "other", Email: "b@example.com"},
	}}
	if l, ok := MatchProfile(reg, blob); !ok || l != "work" {
		t.Fatalf("match = %q %v", l, ok)
	}
	reg.Profiles["dup"] = provider.Profile{Label: "dup", Email: "a@example.com"}
	if _, ok := MatchProfile(reg, blob); ok {
		t.Fatal("two candidates must be no match")
	}
}

func TestExpiry(t *testing.T) {
	blob := fixture("https://auth.x.ai")
	exp, ok := Expiry(blob)
	if !ok || !exp.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry = %v %v", exp, ok)
	}
	if _, ok := Expiry([]byte("{}")); ok {
		t.Fatal("no entry must give no expiry")
	}
}

func TestIssuerAllowlist(t *testing.T) {
	for issuer, want := range map[string]bool{
		"https://auth.x.ai":          true,
		"https://x.ai":               true,
		"https://evil.example.com":   false,
		"https://x.ai.evil.com":      false,
		"https://evilx.ai":           false,
		"http://auth.x.ai":           false,
		"https://auth.x.ai@evil.com": false,
		"https://user@auth.x.ai":     false,
	} {
		if got := allowedIssuer(issuer) == nil; got != want {
			t.Errorf("allowedIssuer(%q) = %v, want %v", issuer, got, want)
		}
	}
	// A crafted file never reaches the network.
	if _, err := Refresh(context.Background(), fixture("https://evil.example.com")); err == nil {
		t.Fatal("refresh accepted an unsafe issuer")
	}
	if err := Revoke(context.Background(), fixture("https://evil.example.com")); err == nil {
		t.Fatal("revoke accepted an unsafe issuer")
	}
}

func allowAny(t *testing.T) {
	t.Helper()
	old := checkIssuer
	checkIssuer = func(string) error { return nil }
	t.Cleanup(func() { checkIssuer = old })
}

func TestRefreshKeepsUnknownFieldsAndRotates(t *testing.T) {
	allowAny(t)
	var form map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		form = map[string]string{
			"grant_type": r.PostForm.Get("grant_type"), "client_id": r.PostForm.Get("client_id"),
			"refresh_token": r.PostForm.Get("refresh_token"),
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer srv.Close()

	out, err := Refresh(context.Background(), fixture(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if form["grant_type"] != "refresh_token" || form["client_id"] != "client-1" || form["refresh_token"] != "old-refresh" {
		t.Fatalf("form = %v", form)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(out, &top)
	if string(top["other_top"]) == "" {
		t.Fatal("unknown top-level key lost")
	}
	var e map[string]any
	_ = json.Unmarshal(top[entryKey], &e)
	if e["key"] != "new-access" || e["refresh_token"] != "new-refresh" {
		t.Fatalf("tokens not replaced: %v", e)
	}
	if e["future_field"] != float64(42) || e["team_id"] != "t1" || e["email"] != "a@example.com" {
		t.Fatalf("unknown fields lost: %v", e)
	}
	exp, ok := Expiry(out)
	if !ok || time.Until(exp) < 59*time.Minute || time.Until(exp) > 61*time.Minute {
		t.Fatalf("expires_at = %v", exp)
	}
}

func TestRefreshKeepsOldRefreshTokenWhenNotRotated(t *testing.T) {
	allowAny(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":60}`))
	}))
	defer srv.Close()
	out, err := Refresh(context.Background(), fixture(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := parseSession(out)
	if s.get("refresh_token") != "old-refresh" {
		t.Fatal("refresh token should stay")
	}
}

func TestRefreshFailures(t *testing.T) {
	allowAny(t)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ended  bool
	}{
		{"invalid_grant", 400, `{"error":"invalid_grant","secret":"SECRET-BODY"}`, true},
		{"unauthorized", 401, `SECRET-BODY`, true},
		{"server error", 500, `SECRET-BODY`, false},
		{"rate limit", 429, `SECRET-BODY`, false},
		{"other 400", 400, `{"error":"invalid_request"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := Refresh(context.Background(), fixture(srv.URL))
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, provider.ErrSessionEnded) != tc.ended {
				t.Fatalf("ended = %v, err = %v", !tc.ended, err)
			}
			if strings.Contains(err.Error(), "SECRET-BODY") {
				t.Fatal("error shows the response body")
			}
		})
	}
}

func TestRevoke(t *testing.T) {
	allowAny(t)
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/revoke" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		got = map[string]string{
			"token": r.PostForm.Get("token"), "hint": r.PostForm.Get("token_type_hint"), "client_id": r.PostForm.Get("client_id"),
		}
	}))
	defer srv.Close()
	if err := Revoke(context.Background(), fixture(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if got["token"] != "old-refresh" || got["hint"] != "refresh_token" || got["client_id"] != "client-1" {
		t.Fatalf("form = %v", got)
	}

	// No refresh token: nothing is sent.
	s, _ := parseSession(fixture(srv.URL))
	s.set("refresh_token", "")
	blob, _ := s.bytes()
	got = nil
	if err := Revoke(context.Background(), blob); err != nil || got != nil {
		t.Fatalf("err = %v, request = %v", err, got)
	}
}

// usageJSON is the confirmed live reply shape, with the tier in the token.
const usageJSON = `{"config":{"creditUsagePercent":100.0,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-11-15T02:02:03.337+00:00","end":"2026-11-22T02:02:03.337+00:00"},"prepaidBalance":{"val":6029},"isUnifiedBillingUser":true,"billingPeriodStart":"x","billingPeriodEnd":"y","productUsage":[{"product":"GrokBuild","usagePercent":81.0}]}}`

func jwtWithTier(tier string) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"ES256"}`) + "." + enc(`{"tier":"`+tier+`"}`) + ".sig"
}

func TestFetchUsage(t *testing.T) {
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		_, _ = w.Write([]byte(usageJSON))
	}))
	defer srv.Close()
	old := usageURL
	usageURL = srv.URL + "/v1/billing?format=credits"
	defer func() { usageURL = old }()

	token := jwtWithTier("SuperGrok Heavy")
	u, err := FetchUsage(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Get("Authorization") != "Bearer "+token || hdr.Get("x-grok-client-mode") != "cli" || hdr.Get("x-grok-client-version") != "1.0.77" {
		t.Fatalf("headers = %v", hdr)
	}
	if u.Plan != "SuperGrok Heavy" || len(u.Windows) != 1 {
		t.Fatalf("usage = %+v", u)
	}
	w := u.Windows[0]
	if w.Name() != "weekly" || w.UsedPercent != 100 || w.Seconds != 604800 ||
		!w.ResetAt.Equal(time.Date(2026, 11, 22, 2, 2, 3, 337000000, time.UTC)) {
		t.Fatalf("window = %+v", w)
	}
}

func TestFetchUsageTierFallbackAndLabels(t *testing.T) {
	r := usageResponse{SubscriptionTier: "Fallback"}
	if err := json.Unmarshal([]byte(`{"config":{"creditUsagePercent":5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY"}}}`), &r); err != nil {
		t.Fatal(err)
	}
	u := r.toUsage()
	if u.Plan != "Fallback" || u.Windows[0].Name() != "monthly" || u.Windows[0].Seconds != 0 {
		t.Fatalf("usage = %+v", u)
	}
	if jwtTier("not-a-jwt") != "" {
		t.Fatal("bad token must give no tier")
	}
}

func TestFetchUsageRejected(t *testing.T) {
	for _, code := range []int{401, 403} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		old := usageURL
		usageURL = srv.URL
		_, err := FetchUsage(context.Background(), "tok")
		usageURL = old
		srv.Close()
		if !errors.Is(err, provider.ErrUsageRejected) {
			t.Fatalf("HTTP %d: err = %v", code, err)
		}
	}
}

func TestWriteLive(t *testing.T) {
	home := filepath.Join(t.TempDir(), "grokhome")
	t.Setenv("GROK_HOME", home)
	if _, err := ReadLive(); !errors.Is(err, provider.ErrNoLiveLogin) {
		t.Fatalf("err = %v", err)
	}
	if err := WriteLive([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLive()
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("read = %q %v", got, err)
	}
	st, _ := os.Stat(AuthPath())
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if e.Name() != AuthFile && e.Name() != lockFile {
			t.Fatalf("leftover file %s", e.Name())
		}
	}
}

func TestWriteLiveWaitsForLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	lock, err := os.OpenFile(filepath.Join(home, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- WriteLive([]byte(`{}`)) }()
	select {
	case <-done:
		t.Fatal("WriteLive ignored the lock")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(AuthPath()); err == nil {
		t.Fatal("file written while locked")
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(AuthPath()); err != nil {
		t.Fatal(err)
	}
}

// fakeGrok puts a script named grok on PATH.
func fakeGrok(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grok"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSignIn(t *testing.T) {
	fakeGrok(t, `
printf '\033[1mOpen https://auth.x.ai/device to sign in\033[0m\n'
printf 'Enter code: ABCD-1234\n' >&2
printf '{"k":{"key":"x","email":"a@example.com"}}' > "$GROK_HOME/auth.json"
echo "$GROK_HOME" > "$CCSWITCH_TEST_HOME_FILE"
`)
	seen := filepath.Join(t.TempDir(), "home")
	t.Setenv("CCSWITCH_TEST_HOME_FILE", seen)
	var prompt provider.LoginPrompt
	calls := 0
	blob, err := SignIn(context.Background(), func(p provider.LoginPrompt) { prompt, calls = p, calls+1 })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || prompt.URL != "https://auth.x.ai/device" || prompt.Code != "ABCD-1234" {
		t.Fatalf("prompt = %+v calls = %d", prompt, calls)
	}
	if string(blob) != `{"k":{"key":"x","email":"a@example.com"}}` {
		t.Fatalf("blob = %q", blob)
	}
	home, _ := os.ReadFile(seen)
	if _, err := os.Stat(string(trimNL(home))); !os.IsNotExist(err) {
		t.Fatal("temporary home was not removed")
	}
}

func TestSignInRealOutput(t *testing.T) {
	fakeGrok(t, `printf '\nTo sign in, open this URL in your browser:\n\n  https://accounts.x.ai/oauth2/device?user_code=ABCD-1234\n\nConfirm this code in your browser:\n\n  ABCD-1234\n\n\033[90mOnly continue with a code you requested. Dont share it with anyone.\033[0m\n\nWaiting for authorization...\n'
printf '{"k":{"key":"x"}}' > "$GROK_HOME/auth.json"
`)
	var prompts []provider.LoginPrompt
	if _, err := SignIn(context.Background(), func(p provider.LoginPrompt) { prompts = append(prompts, p) }); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || prompts[0].URL != "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234" || prompts[0].Code != "ABCD-1234" {
		t.Fatalf("prompts = %+v", prompts)
	}
}

func TestSignInFailureRemovesHome(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "home")
	t.Setenv("CCSWITCH_TEST_HOME_FILE", seen)
	fakeGrok(t, `echo "$GROK_HOME" > "$CCSWITCH_TEST_HOME_FILE"
echo "token SECRET-OUTPUT"
exit 3
`)
	_, err := SignIn(context.Background(), func(provider.LoginPrompt) {})
	if err == nil || strings.Contains(err.Error(), "SECRET-OUTPUT") {
		t.Fatalf("err = %v", err)
	}
	home, _ := os.ReadFile(seen)
	if _, err := os.Stat(string(trimNL(home))); !os.IsNotExist(err) {
		t.Fatal("temporary home was not removed")
	}
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// redirectPair returns a server that 307-redirects every request to a second
// server, and a pointer that tells whether the second one got a request.
func redirectPair(t *testing.T) (first *httptest.Server, secondHit *bool) {
	t.Helper()
	hit := false
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"access_token":"x","expires_in":3600}`))
	}))
	t.Cleanup(second.Close)
	first = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)
	return first, &hit
}

func TestRefreshAndRevokeDoNotFollowRedirects(t *testing.T) {
	allowAny(t)
	srv, hit := redirectPair(t)
	if _, err := Refresh(context.Background(), fixture(srv.URL)); err == nil {
		t.Error("Refresh: want an error for a redirect")
	}
	if err := Revoke(context.Background(), fixture(srv.URL)); err == nil {
		t.Error("Revoke: want an error for a redirect")
	}
	if *hit {
		t.Fatal("the redirect target received the refresh token")
	}
}
