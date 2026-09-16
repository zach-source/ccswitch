package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The OAuth client Claude Code itself uses. Values verified against the
// claude 2.1.273 binary's production config block:
//
//	TOKEN_URL:"https://platform.claude.com/v1/oauth/token"
//	CLIENT_ID:"9d1c250a-e61b-44d9-88ed-5944d1962f5e"
//
// Both are overridable by the same environment variables claude honours, so
// a staging or self-hosted console can be pointed at without a rebuild.
const (
	defaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	defaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

// tokenURL is a package var solely so tests can point it at an httptest server.
var tokenURL = defaultTokenURL

// refreshScopes are the scopes claude requests on a refresh_token grant. The
// server echoes back what it actually granted; we do not re-check, matching
// claude's own behaviour of storing the returned scope list verbatim.
var refreshScopes = []string{
	"user:profile",
	"user:inference",
	"user:sessions:claude_code",
	"user:mcp_servers",
	"user:file_upload",
	"user:plugins",
}

// tokenRequest is the refresh_token grant body. claude posts JSON (not
// form-encoded) to the token endpoint.
type tokenRequest struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
	Scope        string `json:"scope"`
}

// tokenResponse is the subset of the token endpoint's reply that ccswitch
// persists. An omitted refresh_token means "keep using the one you sent".
type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Scope                 string `json:"scope"`
}

// exchangeRefreshToken performs the OAuth2 refresh_token grant.
//
// Refresh tokens rotate: a successful call may invalidate the token that was
// sent, and replaying a superseded token can trip reuse detection and revoke
// the whole family. Callers must persist the result before the token is used
// again, and must not retry a failed exchange with the same token.
func exchangeRefreshToken(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	body, err := json.Marshal(tokenRequest{
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		ClientID:     clientID(),
		Scope:        strings.Join(refreshScopes, " "),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()

	// Cap the read so a misbehaving endpoint cannot OOM the process.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The body carries the OAuth error code ("invalid_grant" when the
		// refresh token is dead) — surface it, it is what tells the user to
		// re-login rather than retry. It contains no secret.
		return nil, fmt.Errorf("token refresh failed: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token refresh returned no access_token")
	}
	if tr.RefreshToken == "" {
		tr.RefreshToken = refreshToken
	}
	return &tr, nil
}

// applyToken splices a refreshed token into an existing credential blob,
// returning new raw bytes.
//
// Every key it does not explicitly set — inside claudeAiOauth and beside it —
// is carried over untouched, so neither the account's unmodelled fields nor
// the account-independent mcpOAuth are disturbed by a refresh.
func applyToken(blob []byte, tr *tokenResponse, now time.Time) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(blob, &top); err != nil {
		return nil, fmt.Errorf("parse credential blob: %w", err)
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(top["claudeAiOauth"], &oauth); err != nil {
		return nil, fmt.Errorf("parse claudeAiOauth: %w", err)
	}
	if oauth == nil {
		oauth = map[string]json.RawMessage{}
	}

	set := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", key, err)
		}
		oauth[key] = b
		return nil
	}
	if err := set("accessToken", tr.AccessToken); err != nil {
		return nil, err
	}
	if err := set("refreshToken", tr.RefreshToken); err != nil {
		return nil, err
	}
	if err := set("expiresAt", now.Add(time.Duration(tr.ExpiresIn)*time.Second).UnixMilli()); err != nil {
		return nil, err
	}
	if tr.RefreshTokenExpiresIn > 0 {
		exp := now.Add(time.Duration(tr.RefreshTokenExpiresIn) * time.Second).UnixMilli()
		if err := set("refreshTokenExpiresAt", exp); err != nil {
			return nil, err
		}
	}
	if tr.Scope != "" {
		if err := set("scopes", strings.Fields(tr.Scope)); err != nil {
			return nil, err
		}
	}

	merged, err := json.Marshal(oauth)
	if err != nil {
		return nil, fmt.Errorf("marshal claudeAiOauth: %w", err)
	}
	top["claudeAiOauth"] = merged
	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("marshal credential blob: %w", err)
	}
	return out, nil
}

// endpoint returns the token URL, honouring claude's own override so a
// staging or self-hosted console works without a rebuild.
//
// An override is accepted only over HTTPS, or over plain HTTP when it points
// at loopback — the same rule claude applies. The request body carries a live
// refresh token, so an http:// override to any other host would hand the
// account to whoever is on the wire. An override that fails the check is
// ignored (with a warning) rather than honoured.
func endpoint() string {
	base := strings.TrimRight(os.Getenv("CLAUDE_CODE_CUSTOM_OAUTH_URL"), "/")
	if base == "" {
		return tokenURL
	}
	if err := checkTransport(base); err != nil {
		fmt.Fprintf(os.Stderr, "warning: ignoring CLAUDE_CODE_CUSTOM_OAUTH_URL: %v\n", err)
		return tokenURL
	}
	return base + "/v1/oauth/token"
}

// checkTransport rejects a token endpoint that would send the refresh token
// in the clear to a non-loopback host.
func checkTransport(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := strings.Trim(u.Hostname(), "[]")
	if u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return fmt.Errorf("refusing to send a refresh token to %q over %s", raw, u.Scheme)
}

// clientID returns the OAuth client id, honouring claude's own override.
func clientID() string {
	if id := os.Getenv("CLAUDE_CODE_OAUTH_CLIENT_ID"); id != "" {
		return id
	}
	return defaultClientID
}
