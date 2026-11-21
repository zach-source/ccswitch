package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
	"github.com/zach-source/ccswitch/internal/refresh"
)

// noRedirect stops the client from following a redirect: a 307 would resend
// the refresh token in the body to the host it names.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

var httpClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: noRedirect}

// checkIssuer is the issuer rule. The issuer comes from the credential file,
// so a crafted file could send the refresh token to any host. Only https
// issuers on x.ai are allowed. Tests replace it to reach a local server.
var checkIssuer = allowedIssuer

func allowedIssuer(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return errors.New("the credential has an unsafe issuer")
	}
	host := strings.ToLower(u.Hostname())
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return errors.New("the credential has an unsafe issuer")
	}
	return nil
}

// endpoint returns the OAuth endpoint path under the issuer of the entry.
func (s *session) endpoint(path string) (string, error) {
	issuer := s.get("oidc_issuer")
	if issuer == "" {
		return "", errors.New("the credential has no issuer")
	}
	if err := checkIssuer(issuer); err != nil {
		return "", err
	}
	// Never send a token in the clear, even when a test replaced the rule.
	if err := refresh.CheckTransport(issuer); err != nil {
		return "", errors.New("the credential has an unsafe issuer")
	}
	return strings.TrimRight(issuer, "/") + path, nil
}

// postForm sends a form. It returns the status and the body (at most 1 MiB).
// Errors never hold the body or the request: both can hold tokens.
func postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, errors.New("build request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "ccswitch")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request to %s failed", req.URL.Host)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, nil
}

// sessionEnded reports whether a failed reply ends the session for good.
func sessionEnded(status int, body []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	var b struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	code := strings.ToLower(b.Error)
	return status == http.StatusBadRequest && (code == "invalid_grant" || code == "invalid_token")
}

// Refresh renews the access token with the refresh token and returns the new
// file. The server may rotate the refresh token; a new one replaces the old.
// A permanent failure returns provider.ErrSessionEnded; any other is transient.
func Refresh(ctx context.Context, blob []byte) ([]byte, error) {
	s, err := parseSession(blob)
	if err != nil {
		return nil, err
	}
	rt := s.get("refresh_token")
	if rt == "" {
		return nil, errors.New("the credential has no refresh token")
	}
	endpoint, err := s.endpoint("/oauth2/token")
	if err != nil {
		return nil, err
	}
	status, body, err := postForm(ctx, endpoint, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {s.get("oidc_client_id")}, "refresh_token": {rt},
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		if sessionEnded(status, body) {
			return nil, provider.ErrSessionEnded
		}
		return nil, fmt.Errorf("token refresh failed: HTTP %d", status)
	}
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.AccessToken == "" || resp.ExpiresIn <= 0 {
		return nil, errors.New("token refresh failed: unreadable reply")
	}
	s.set("key", resp.AccessToken)
	if resp.RefreshToken != "" {
		s.set("refresh_token", resp.RefreshToken)
	}
	exp := time.Now().UTC().Add(time.Duration(resp.ExpiresIn) * time.Second)
	s.set("expires_at", exp.Format("2006-01-02T15:04:05.000000Z"))
	return s.bytes()
}

// Revoke tells the server to drop the refresh token. It does nothing for a
// login with no refresh token.
func Revoke(ctx context.Context, blob []byte) error {
	s, err := parseSession(blob)
	if err != nil {
		return err
	}
	rt := s.get("refresh_token")
	if rt == "" {
		return nil
	}
	endpoint, err := s.endpoint("/oauth2/revoke")
	if err != nil {
		return err
	}
	status, _, err := postForm(ctx, endpoint, url.Values{
		"token": {rt}, "token_type_hint": {"refresh_token"}, "client_id": {s.get("oidc_client_id")},
	})
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("token revoke failed: HTTP %d", status)
	}
	return nil
}
