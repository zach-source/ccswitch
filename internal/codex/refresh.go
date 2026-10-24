package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// clientID is the OAuth client of the Codex CLI.
	clientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	refreshURL = "https://auth.openai.com/oauth/token"
	revokeURL  = "https://auth.openai.com/oauth/revoke"
)

// ErrSessionEnded means the server will never accept this refresh token
// again. The session must be created again.
var ErrSessionEnded = errors.New("the session has ended")

var httpClient = &http.Client{Timeout: 10 * time.Second}

// endedCodes are the error codes that end a session for good. Codex treats
// them the same way (login/src/auth/manager.rs, rust-v0.164.0).
var endedCodes = []string{"refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated"}

// session is an auth.json that keeps every field it does not know.
type session struct {
	top    map[string]json.RawMessage
	tokens map[string]json.RawMessage
}

func parseSession(blob []byte) (*session, error) {
	s := &session{}
	if err := json.Unmarshal(blob, &s.top); err != nil {
		return nil, errors.New("the credential is not valid JSON")
	}
	if err := json.Unmarshal(s.top["tokens"], &s.tokens); err != nil || s.tokens == nil {
		return nil, errors.New("the credential has no tokens")
	}
	return s, nil
}

func (s *session) get(name string) string {
	var v string
	_ = json.Unmarshal(s.tokens[name], &v)
	return v
}

func (s *session) set(name, value string) {
	s.tokens[name], _ = json.Marshal(value)
}

func (s *session) bytes() ([]byte, error) {
	var err error
	if s.top["tokens"], err = json.Marshal(s.tokens); err != nil {
		return nil, err
	}
	return json.MarshalIndent(s.top, "", "  ")
}

func endpoint(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// post sends a JSON body. It returns the status and the body (at most 1 MiB).
// Errors never hold the body or the request: both can hold tokens.
func post(ctx context.Context, url string, body map[string]string) (int, []byte, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, errors.New("build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ccswitch")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request to %s failed", req.URL.Host)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, nil
}

// errorCode reads the error code of a token endpoint reply. It sits in
// "error" (a string), in "error.code" or in "code".
func errorCode(body []byte) string {
	var b struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(b.Error, &s) == nil && s != "" {
		return s
	}
	var e struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(b.Error, &e) == nil && e.Code != "" {
		return e.Code
	}
	return b.Code
}

// sessionEnded reports whether a failed reply ends the session for good.
func sessionEnded(status int, body []byte) bool {
	code := strings.ToLower(errorCode(body))
	if status == http.StatusUnauthorized || status == http.StatusBadRequest && code == "invalid_grant" {
		return true
	}
	for _, c := range endedCodes {
		if code == c {
			return true
		}
	}
	return false
}

// Refresh renews the tokens of an auth.json with its refresh token and
// returns the new file. A field the server leaves out keeps its old value.
// A permanent failure returns ErrSessionEnded; any other failure is transient.
func Refresh(ctx context.Context, blob []byte) ([]byte, error) {
	s, err := parseSession(blob)
	if err != nil {
		return nil, err
	}
	rt := s.get("refresh_token")
	if rt == "" {
		return nil, errors.New("the credential has no refresh token")
	}
	status, body, err := post(ctx, endpoint("CCSWITCH_CODEX_REFRESH_URL", refreshURL), map[string]string{
		"grant_type": "refresh_token", "client_id": clientID, "refresh_token": rt,
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		if sessionEnded(status, body) {
			return nil, ErrSessionEnded
		}
		return nil, fmt.Errorf("token refresh failed: HTTP %d", status)
	}
	var resp struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil, errors.New("token refresh failed: unreadable reply")
	}
	for name, v := range map[string]string{
		"id_token": resp.IDToken, "access_token": resp.AccessToken, "refresh_token": resp.RefreshToken,
	} {
		if v != "" {
			s.set(name, v)
		}
	}
	s.top["last_refresh"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339))
	return s.bytes()
}

// Revoke tells the server to drop the refresh token of an auth.json.
func Revoke(ctx context.Context, blob []byte) error {
	s, err := parseSession(blob)
	if err != nil {
		return err
	}
	rt := s.get("refresh_token")
	if rt == "" {
		return errors.New("the credential has no refresh token")
	}
	status, _, err := post(ctx, endpoint("CCSWITCH_CODEX_REVOKE_URL", revokeURL), map[string]string{
		"token": rt, "token_type_hint": "refresh_token", "client_id": clientID,
	})
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("token revoke failed: HTTP %d", status)
	}
	return nil
}

// WithoutRefreshToken returns the auth.json with an empty refresh token.
// Codex needs the field to load the file (TokenData.refresh_token has no
// serde default), so it is set to "" and not removed. Codex then fails only
// when it tries to refresh.
func WithoutRefreshToken(blob []byte) ([]byte, error) {
	s, err := parseSession(blob)
	if err != nil {
		return nil, err
	}
	s.set("refresh_token", "")
	return s.bytes()
}
