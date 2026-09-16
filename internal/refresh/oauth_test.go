package refresh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// storedBlob is the shape of a real stored credential: the account's tokens
// plus account-independent MCP state, plus a field Credentials does not model.
const storedBlob = `{"mcpOAuth":{"linear":"keep-me"},` +
	`"claudeAiOauth":{"accessToken":"old","refreshToken":"old-refresh","expiresAt":1,` +
	`"subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`

// serveToken stands in for the Anthropic token endpoint, recording the request
// body it received so the test can assert the grant is well-formed.
func serveToken(t *testing.T, status int, body string) (*httptest.Server, *tokenRequest) {
	t.Helper()
	var got tokenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	prev := tokenURL
	tokenURL = srv.URL
	t.Cleanup(func() { tokenURL = prev })
	return srv, &got
}

func TestRefreshOneRoundTrip(t *testing.T) {
	_, got := serveToken(t, http.StatusOK, `{"access_token":"fresh","refresh_token":"rotated",`+
		`"expires_in":3600,"refresh_token_expires_in":7200,"scope":"user:profile user:inference"}`)

	before := time.Now()
	raw, cred, err := RefreshOne(context.Background(), []byte(storedBlob))
	if err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}

	// The grant we sent.
	if got.GrantType != "refresh_token" {
		t.Errorf("grant_type = %q", got.GrantType)
	}
	if got.RefreshToken != "old-refresh" {
		t.Errorf("refresh_token = %q, want the stored one", got.RefreshToken)
	}
	if got.ClientID != defaultClientID {
		t.Errorf("client_id = %q, want %q", got.ClientID, defaultClientID)
	}
	if !strings.Contains(got.Scope, "user:inference") {
		t.Errorf("scope = %q, missing user:inference", got.Scope)
	}

	// The credential we stored.
	if cred.ClaudeAIOAuth.AccessToken != "fresh" {
		t.Errorf("accessToken = %q, want fresh", cred.ClaudeAIOAuth.AccessToken)
	}
	if cred.ClaudeAIOAuth.RefreshToken != "rotated" {
		t.Errorf("refreshToken = %q, want the rotated one", cred.ClaudeAIOAuth.RefreshToken)
	}
	if exp := cred.ExpiresAt(); exp.Before(before.Add(59*time.Minute)) || exp.After(before.Add(61*time.Minute)) {
		t.Errorf("expiresAt = %v, want ~1h out", exp)
	}
	if cred.ClaudeAIOAuth.SubscriptionType != "max" {
		t.Error("subscriptionType was dropped by the refresh")
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("parse refreshed blob: %v", err)
	}
	if string(top["mcpOAuth"]) != `{"linear":"keep-me"}` {
		t.Errorf("mcpOAuth not preserved: %s", top["mcpOAuth"])
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(top["claudeAiOauth"], &oauth); err != nil {
		t.Fatal(err)
	}
	if _, ok := oauth["refreshTokenExpiresAt"]; !ok {
		t.Error("refreshTokenExpiresAt not recorded from refresh_token_expires_in")
	}
}

// An omitted refresh_token means "keep using the one you sent" — dropping it
// would strand the account on the next refresh.
func TestRefreshOneKeepsRefreshTokenWhenNotRotated(t *testing.T) {
	serveToken(t, http.StatusOK, `{"access_token":"fresh","expires_in":3600}`)

	_, cred, err := RefreshOne(context.Background(), []byte(storedBlob))
	if err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}
	if cred.ClaudeAIOAuth.RefreshToken != "old-refresh" {
		t.Errorf("refreshToken = %q, want the original to be retained", cred.ClaudeAIOAuth.RefreshToken)
	}
}

// A dead refresh token must surface the server's OAuth error code — that is
// what tells the caller to re-login rather than retry.
func TestRefreshOneSurfacesInvalidGrant(t *testing.T) {
	serveToken(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)

	_, _, err := RefreshOne(context.Background(), []byte(storedBlob))
	if err == nil {
		t.Fatal("expected an error on HTTP 400")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should carry the OAuth code, got: %v", err)
	}
}

func TestRefreshOneRejectsBlobWithoutRefreshToken(t *testing.T) {
	_, _, err := RefreshOne(context.Background(), []byte(`{"claudeAiOauth":{"accessToken":"x"}}`))
	if err == nil {
		t.Fatal("expected an error when there is no refresh token to exchange")
	}
}

func TestEndpointHonoursCustomOAuthURL(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CUSTOM_OAUTH_URL", "https://example.test/")
	if got, want := endpoint(), "https://example.test/v1/oauth/token"; got != want {
		t.Errorf("endpoint() = %q, want %q", got, want)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_CLIENT_ID", "custom-id")
	if got := clientID(); got != "custom-id" {
		t.Errorf("clientID() = %q, want custom-id", got)
	}
}
