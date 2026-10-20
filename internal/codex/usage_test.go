package codex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// usageServer answers every request with status and body, and hands the
// request to check (when set) so a test can inspect the headers.
func usageServer(t *testing.T, status int, body string, check func(*http.Request)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CCSWITCH_CODEX_USAGE_URL", srv.URL)
}

const weeklyOnly = `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,
"primary_window":{"used_percent":3,"limit_window_seconds":604800,"reset_after_seconds":123,"reset_at":1793082993},
"secondary_window":null},"credits":{"has_credits":false}}`

const fiveHourAndWeekly = `{"plan_type":"plus","rate_limit":{
"primary_window":{"used_percent":42.5,"limit_window_seconds":18000,"reset_after_seconds":60,"reset_at":1793000000},
"secondary_window":{"used_percent":71,"limit_window_seconds":604800,"reset_after_seconds":600,"reset_at":1793082993}}}`

func TestFetchUsage_WeeklyOnly(t *testing.T) {
	usageServer(t, 200, weeklyOnly, nil)
	u, err := FetchUsage(context.Background(), "fake-token", "acct")
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if u.Plan != "pro" {
		t.Errorf("Plan = %q", u.Plan)
	}
	if len(u.Windows) != 1 {
		t.Fatalf("Windows = %+v, want 1", u.Windows)
	}
	w := u.Windows[0]
	if w.Name() != "weekly" || w.UsedPercent != 3 || w.Seconds != 604800 {
		t.Errorf("window = %+v", w)
	}
	if !w.ResetAt.Equal(time.Unix(1793082993, 0)) {
		t.Errorf("ResetAt = %v", w.ResetAt)
	}
	if u.Load() != 3 {
		t.Errorf("Load = %v", u.Load())
	}
}

func TestFetchUsage_FiveHourAndWeekly(t *testing.T) {
	usageServer(t, 200, fiveHourAndWeekly, nil)
	u, err := FetchUsage(context.Background(), "fake-token", "")
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if len(u.Windows) != 2 {
		t.Fatalf("Windows = %+v, want 2", u.Windows)
	}
	if u.Windows[0].Name() != "5h" || u.Windows[0].UsedPercent != 42.5 {
		t.Errorf("first = %+v", u.Windows[0])
	}
	if u.Windows[1].Name() != "weekly" || u.Windows[1].UsedPercent != 71 {
		t.Errorf("second = %+v", u.Windows[1])
	}
	if u.Load() != 71 {
		t.Errorf("Load = %v, want 71", u.Load())
	}
}

func TestFetchUsage_BothWindowsNull(t *testing.T) {
	usageServer(t, 200, `{"plan_type":"free","rate_limit":{"primary_window":null,"secondary_window":null}}`, nil)
	u, err := FetchUsage(context.Background(), "fake-token", "")
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if len(u.Windows) != 0 || u.Load() != 0 || u.Plan != "free" {
		t.Errorf("usage = %+v", u)
	}
}

func TestFetchUsage_NoRateLimit(t *testing.T) {
	usageServer(t, 200, `{"plan_type":"free"}`, nil)
	u, err := FetchUsage(context.Background(), "fake-token", "")
	if err != nil || len(u.Windows) != 0 {
		t.Fatalf("usage = %+v, err = %v", u, err)
	}
}

func TestFetchUsage_Headers(t *testing.T) {
	tests := []struct {
		name      string
		accountID string
		wantAcct  string
		wantSet   bool
	}{
		{"account id present", "acct-123", "acct-123", true},
		{"account id absent", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got *http.Request
			usageServer(t, 200, weeklyOnly, func(r *http.Request) { got = r.Clone(r.Context()) })
			if _, err := FetchUsage(context.Background(), "fake-token", tc.accountID); err != nil {
				t.Fatal(err)
			}
			if got.Method != http.MethodGet {
				t.Errorf("method = %s", got.Method)
			}
			if a := got.Header.Get("Authorization"); a != "Bearer fake-token" {
				t.Errorf("Authorization = %q", a)
			}
			if ua := got.Header.Get("User-Agent"); ua != "ccswitch" {
				t.Errorf("User-Agent = %q", ua)
			}
			vals, set := got.Header["Chatgpt-Account-Id"]
			if set != tc.wantSet {
				t.Fatalf("ChatGPT-Account-Id present = %v, want %v", set, tc.wantSet)
			}
			if set && vals[0] != tc.wantAcct {
				t.Errorf("ChatGPT-Account-Id = %q", vals[0])
			}
		})
	}
}

func TestFetchUsage_UsedPercentIntegerOrFloat(t *testing.T) {
	for name, body := range map[string]string{
		"integer": `{"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000}}}`,
		"float":   `{"rate_limit":{"primary_window":{"used_percent":7.0,"limit_window_seconds":18000}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			usageServer(t, 200, body, nil)
			u, err := FetchUsage(context.Background(), "fake-token", "")
			if err != nil || len(u.Windows) != 1 || u.Windows[0].UsedPercent != 7 {
				t.Fatalf("usage = %+v, err = %v", u, err)
			}
		})
	}
}

func TestFetchUsage_Errors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantRejected bool
	}{
		{"401", 401, `{"error":"secret-body-text"}`, true},
		{"403", 403, `{"error":"secret-body-text"}`, true},
		{"429", 429, `{"error":"secret-body-text"}`, false},
		{"500", 500, `{"error":"secret-body-text"}`, false},
		{"malformed JSON", 200, `{"plan_type":`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			usageServer(t, tc.status, tc.body, nil)
			_, err := FetchUsage(context.Background(), "fake-token", "acct")
			if err == nil {
				t.Fatal("want error")
			}
			if got := errors.Is(err, ErrUsageRejected); got != tc.wantRejected {
				t.Errorf("errors.Is(ErrUsageRejected) = %v, want %v (err: %v)", got, tc.wantRejected, err)
			}
			for _, secret := range []string{"fake-token", "secret-body-text"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks %q: %v", secret, err)
				}
			}
		})
	}
}

func TestFetchUsage_NetworkError(t *testing.T) {
	t.Setenv("CCSWITCH_CODEX_USAGE_URL", "http://127.0.0.1:1")
	_, err := FetchUsage(context.Background(), "fake-token", "")
	if err == nil || errors.Is(err, ErrUsageRejected) {
		t.Fatalf("err = %v, want a plain error", err)
	}
}

func TestFetchUsage_DefaultURL(t *testing.T) {
	if defaultUsageURL != "https://chatgpt.com/backend-api/wham/usage" {
		t.Errorf("defaultUsageURL = %q", defaultUsageURL)
	}
}

func TestWindow_Name(t *testing.T) {
	tests := []struct {
		seconds int
		want    string
	}{
		{18000, "5h"},
		{604800, "weekly"},
		{10800, "3h"},
		{86400, "24h"},
		{3600, "1h"},
		{1800, "30m"},
		{60, "1m"},
	}
	for _, tc := range tests {
		if got := (Window{Seconds: tc.seconds}).Name(); got != tc.want {
			t.Errorf("Name(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestUsage_Load(t *testing.T) {
	tests := []struct {
		name string
		u    Usage
		want float64
	}{
		{"no windows", Usage{}, 0},
		{"one", Usage{Windows: []Window{{UsedPercent: 3}}}, 3},
		{"highest wins", Usage{Windows: []Window{{UsedPercent: 90}, {UsedPercent: 12.5}}}, 90},
	}
	for _, tc := range tests {
		if got := tc.u.Load(); got != tc.want {
			t.Errorf("%s: Load = %v, want %v", tc.name, got, tc.want)
		}
	}
}
