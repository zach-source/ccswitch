package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/codex"
)

// usageBlob builds a credential with an access token that expires at exp.
// The access token text is the secret the tests check never reaches output.
func usageBlob(email string, exp time.Time, lastRefresh string) string {
	enc := base64.RawURLEncoding.EncodeToString
	access := enc([]byte(`{"alg":"none"}`)) + "." +
		enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
	return `{"auth_mode":"chatgpt","last_refresh":"` + lastRefresh + `","tokens":{"access_token":"` + access +
		`","id_token":"` + codexIDToken(email) + `"}}`
}

// usageServer answers /usage by the account header and counts requests.
func usageServer(t *testing.T, byAccount map[string]func(w http.ResponseWriter)) *int32 {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		h, ok := byAccount[r.Header.Get("ChatGPT-Account-Id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h(w)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CCSWITCH_CODEX_USAGE_URL", srv.URL)
	return &n
}

func usageJSON(body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = w.Write([]byte(body)) }
}

func usageStatus(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(code) }
}

// runOut runs a command and returns its stdout.
func runOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := Root()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs(normalizeLegacyArgs(args, root))
	err := root.Execute()
	return buf.String(), err
}

// seedUsageProfiles saves profiles into the store and registry and makes
// the first one live.
func seedUsageProfiles(t *testing.T, home string, blobs map[string]string, order []string) {
	t.Helper()
	reg := &codex.Registry{Version: codex.SchemaVersion, Profiles: map[string]codex.Profile{}, Priority: order}
	for label, blob := range blobs {
		id := codex.Describe([]byte(blob))
		reg.Profiles[label] = codex.Profile{Label: label, Mode: id.Mode, Email: id.Email, Plan: id.Plan, AccountID: id.AccountID}
		seedCred(t, home, codex.CredKey(label), []byte(blob))
	}
	if err := reg.Save(codexRegistryPath()); err != nil {
		t.Fatal(err)
	}
}

func TestCodexUsage_StatusesAndText(t *testing.T) {
	home := newTestHome(t)
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	reset := time.Now().Add(2 * time.Hour).Unix()
	blobs := map[string]string{
		"a": usageBlob("a@example.com", future, "2026-10-20T10:00:00Z"),
		"b": usageBlob("b@example.com", future, "2026-10-20T10:00:00Z"),
		"c": usageBlob("c@example.com", future, "2026-10-20T10:00:00Z"),
		"d": usageBlob("d@example.com", future, "2026-10-20T10:00:00Z"),
		"e": usageBlob("e@example.com", past, "2026-10-02T10:00:00Z"),
	}
	seedUsageProfiles(t, home, blobs, []string{"a", "b", "c", "d", "e"})
	newCodexHome(t, blobs["a"])
	n := usageServer(t, map[string]func(http.ResponseWriter){
		"acct-a@example.com": usageJSON(fmt.Sprintf(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":41,"limit_window_seconds":604800,"reset_at":%d},"secondary_window":null}}`, reset)),
		"acct-b@example.com": usageJSON(fmt.Sprintf(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":30,"limit_window_seconds":604800,"reset_at":%d}}}`, reset, reset)),
		"acct-c@example.com": usageStatus(http.StatusUnauthorized),
		"acct-d@example.com": usageStatus(http.StatusInternalServerError),
	})

	out, err := runOut(t, "codex", "usage")
	if err != nil {
		t.Fatalf("codex usage: %v", err)
	}
	if got := atomic.LoadInt32(n); got != 4 {
		t.Errorf("requests = %d, want 4 (stale profile makes none)", got)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5:\n%s", len(lines), out)
	}
	want := []string{
		"* a  pro   weekly 41% (resets",
		"  b  plus  5h 12% (resets",
		"  c  ",
		"  d  ",
		"  e  ",
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], w)
		}
	}
	for _, sub := range []string{"weekly 30% (resets", "expired", "unavailable", "stale (last used Oct 02)"} {
		if !strings.Contains(out, sub) {
			t.Errorf("output lacks %q:\n%s", sub, out)
		}
	}
	if strings.Contains(out, ".sig") {
		t.Errorf("output leaks token text:\n%s", out)
	}
}

func TestCodexUsage_JSON(t *testing.T) {
	home := newTestHome(t)
	future := time.Now().Add(time.Hour)
	blobs := map[string]string{
		"a": usageBlob("a@example.com", future, "2026-10-20T10:00:00Z"),
		"b": usageBlob("b@example.com", time.Now().Add(-time.Hour), "2026-10-20T10:00:00Z"),
	}
	seedUsageProfiles(t, home, blobs, []string{"a", "b"})
	newCodexHome(t, blobs["a"])
	usageServer(t, map[string]func(http.ResponseWriter){
		"acct-a@example.com": usageJSON(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":41,"limit_window_seconds":604800,"reset_at":1792000000}}}`),
	})

	out, err := runOut(t, "codex", "usage", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Label, Email, Status, Plan string
		Live                       bool
		Windows                    []struct {
			Seconds     int
			UsedPercent float64
			ResetAt     string
		}
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	a, b := rows[0], rows[1]
	if a.Label != "a" || a.Email != "a@example.com" || !a.Live || a.Status != "ok" || a.Plan != "pro" {
		t.Errorf("row a = %+v", a)
	}
	if len(a.Windows) != 1 || a.Windows[0].Seconds != 604800 || a.Windows[0].UsedPercent != 41 || a.Windows[0].ResetAt == "" {
		t.Errorf("row a windows = %+v", a.Windows)
	}
	if b.Live || b.Status != "stale" || b.Windows == nil {
		t.Errorf("row b = %+v (windows must be an empty array, not null)", b)
	}
}
