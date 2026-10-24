package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sessionBlob(t *testing.T, access, refresh string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": "old-id", "access_token": access,
			"refresh_token": refresh, "account_id": "acct-1", "extra": 7,
		},
		"last_refresh": "2020-01-01T00:00:00Z",
		"future_field": map[string]any{"a": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tokensOf(t *testing.T, blob []byte) map[string]any {
	t.Helper()
	var b struct {
		Tokens map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(blob, &b); err != nil {
		t.Fatal(err)
	}
	return b.Tokens
}

func tokenServer(t *testing.T, status int, body string, got *map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if got != nil {
			*got = in
			(*got)["ua"] = r.UserAgent()
			(*got)["ct"] = r.Header.Get("Content-Type")
			(*got)["method"] = r.Method
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CCSWITCH_CODEX_REFRESH_URL", srv.URL)
	t.Setenv("CCSWITCH_CODEX_REVOKE_URL", srv.URL)
}

func TestRefresh_UpdatesTokensAndKeepsTheRest(t *testing.T) {
	var got map[string]string
	tokenServer(t, 200, `{"id_token":"new-id","access_token":"new-at","refresh_token":"new-rt"}`, &got)

	out, err := Refresh(context.Background(), sessionBlob(t, "old-at", "old-rt"))
	if err != nil {
		t.Fatal(err)
	}
	if got["grant_type"] != "refresh_token" || got["refresh_token"] != "old-rt" ||
		got["client_id"] != "app_EMoamEEZ73f0CkXaXp7hrann" || got["ua"] != "ccswitch" ||
		got["method"] != "POST" || !strings.HasPrefix(got["ct"], "application/json") {
		t.Errorf("request = %v", got)
	}
	tk := tokensOf(t, out)
	if tk["id_token"] != "new-id" || tk["access_token"] != "new-at" || tk["refresh_token"] != "new-rt" {
		t.Errorf("tokens = %v", tk)
	}
	if tk["account_id"] != "acct-1" || tk["extra"] != float64(7) {
		t.Errorf("other token fields lost: %v", tk)
	}
	if !strings.Contains(string(out), `"future_field"`) {
		t.Error("unknown top-level field lost")
	}
	at, ok := LastRefresh(out)
	if !ok || time.Since(at) > time.Minute {
		t.Errorf("last_refresh = %v", at)
	}
}

func TestRefresh_KeepsOldValueForMissingFields(t *testing.T) {
	tokenServer(t, 200, `{"access_token":"new-at"}`, nil)
	out, err := Refresh(context.Background(), sessionBlob(t, "old-at", "old-rt"))
	if err != nil {
		t.Fatal(err)
	}
	tk := tokensOf(t, out)
	if tk["access_token"] != "new-at" || tk["refresh_token"] != "old-rt" || tk["id_token"] != "old-id" {
		t.Errorf("tokens = %v", tk)
	}
}

func TestRefresh_PermanentFailures(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"401":                {401, ``},
		"invalid_grant":      {400, `{"error":"invalid_grant"}`},
		"nested reused":      {403, `{"error":{"code":"Refresh_Token_Reused"}}`},
		"expired":            {400, `{"error":"refresh_token_expired"}`},
		"invalidated":        {400, `{"error":{"code":"refresh_token_invalidated"}}`},
		"top-level code":     {400, `{"code":"refresh_token_expired"}`},
		"invalid_grant 400u": {400, `{"error":{"code":"INVALID_GRANT"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			tokenServer(t, c.status, c.body, nil)
			_, err := Refresh(context.Background(), sessionBlob(t, "old-at", "old-rt"))
			if !errors.Is(err, ErrSessionEnded) {
				t.Errorf("err = %v, want ErrSessionEnded", err)
			}
		})
	}
}

func TestRefresh_TransientFailuresHideTheBody(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"500":                  {500, `{"refresh_token":"leak-rt"}`},
		"400 other":            {400, `{"error":"slow_down","refresh_token":"leak-rt"}`},
		"invalid_grant on 500": {500, `{"error":"invalid_grant"}`},
		"bad json on success":  {200, `not json leak-rt`},
		"empty success":        {200, ``},
	} {
		t.Run(name, func(t *testing.T) {
			tokenServer(t, c.status, c.body, nil)
			_, err := Refresh(context.Background(), sessionBlob(t, "old-at", "old-rt"))
			if err == nil || errors.Is(err, ErrSessionEnded) {
				t.Fatalf("err = %v, want a transient error", err)
			}
			for _, secret := range []string{"leak-rt", "old-rt", "old-at"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error holds %q: %v", secret, err)
				}
			}
		})
	}
}

func TestRefresh_NeedsARefreshToken(t *testing.T) {
	if _, err := Refresh(context.Background(), sessionBlob(t, "old-at", "")); err == nil {
		t.Error("want an error")
	}
	if _, err := Refresh(context.Background(), []byte("garbage")); err == nil {
		t.Error("want an error")
	}
}

func TestRevoke_SendsTheRefreshToken(t *testing.T) {
	var got map[string]string
	tokenServer(t, 200, ``, &got)
	if err := Revoke(context.Background(), sessionBlob(t, "old-at", "old-rt")); err != nil {
		t.Fatal(err)
	}
	if got["token"] != "old-rt" || got["token_type_hint"] != "refresh_token" ||
		got["client_id"] != "app_EMoamEEZ73f0CkXaXp7hrann" || got["ua"] != "ccswitch" {
		t.Errorf("request = %v", got)
	}
}

func TestRevoke_FailureHidesTheBody(t *testing.T) {
	tokenServer(t, 500, `leak-rt`, nil)
	err := Revoke(context.Background(), sessionBlob(t, "old-at", "old-rt"))
	if err == nil || strings.Contains(err.Error(), "leak-rt") || strings.Contains(err.Error(), "old-rt") {
		t.Errorf("err = %v", err)
	}
}

func TestWithoutRefreshToken(t *testing.T) {
	out, err := WithoutRefreshToken(sessionBlob(t, "old-at", "old-rt"))
	if err != nil {
		t.Fatal(err)
	}
	tk := tokensOf(t, out)
	if v, ok := tk["refresh_token"]; !ok || v != "" {
		t.Errorf("refresh_token = %v (present %v), want empty string", v, ok)
	}
	if tk["access_token"] != "old-at" || strings.Contains(string(out), "old-rt") {
		t.Errorf("out = %s", out)
	}
	if _, ok := LastRefresh(out); !ok {
		t.Error("last_refresh lost")
	}
}
