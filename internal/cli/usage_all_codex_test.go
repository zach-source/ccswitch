package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
)

// seedUsageAllClaude sets up one Claude account and a fake OAuth usage API.
func seedUsageAllClaude(t *testing.T, home string) {
	t.Helper()
	email := "alice@example.com"
	seedClaudeJSON(t, home, email)
	seqWith(t, account.HashEmail(email), email)
	future := time.Now().Add(2 * time.Hour).UnixMilli()
	seedCred(t, home, account.ActiveCredKey,
		[]byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT","refreshToken":"RT","expiresAt":%d}}`, future)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"five_hour":{"utilization":42,"resets_at":""},"seven_day":{"utilization":77,"resets_at":""}}`)
	}))
	t.Cleanup(srv.Close)
	prev := anthropicUsageURL
	anthropicUsageURL = srv.URL
	t.Cleanup(func() { anthropicUsageURL = prev })
}

func seedUsageAllCodex(t *testing.T, home string) {
	t.Helper()
	blobs := map[string]string{"a": usageBlob("a@example.com", time.Now().Add(time.Hour), "2026-10-20T10:00:00Z")}
	seedUsageProfiles(t, home, blobs, []string{"a"})
	newCodexHome(t, blobs["a"])
	usageServer(t, map[string]func(http.ResponseWriter){
		"acct-a@example.com": usageJSON(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_at":1792000000}}}`),
	})
}

func TestUsageAll_PrintsCodexSection(t *testing.T) {
	home := newTestHome(t)
	seedUsageAllClaude(t, home)
	seedUsageAllCodex(t, home)

	out, err := capture(t, func() error { return run(t, "usage-all") })
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSI(out)
	ci := strings.Index(out, "Codex Accounts - Usage")
	if ci < 0 || strings.Index(out, "All Accounts - Usage") > ci {
		t.Fatalf("Codex section missing or before the Claude section:\n%s", out)
	}
	if !strings.Contains(out, "◉ a [pro]") || !strings.Contains(out, "5h:") {
		t.Errorf("Codex rows missing:\n%s", out)
	}
	if strings.Contains(out, "resets in \n") || strings.Contains(out, "resets in\n") {
		t.Errorf("empty reset text:\n%s", out)
	}
}

func TestUsageAll_NoCodexProfiles(t *testing.T) {
	home := newTestHome(t)
	seedUsageAllClaude(t, home)
	newCodexHome(t, "")

	out, err := capture(t, func() error { return run(t, "usage-all") })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Codex") {
		t.Errorf("unexpected Codex output:\n%s", out)
	}
}

func TestUsageAll_BrokenCodexKeepsClaudeOutput(t *testing.T) {
	home := newTestHome(t)
	seedUsageAllClaude(t, home)
	newCodexHome(t, "")
	path := codexRegistryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := capture(t, func() error { return run(t, "usage-all") })
	if err != nil {
		t.Fatalf("a Codex error must not fail usage-all: %v", err)
	}
	out = stripANSI(out)
	if !strings.Contains(out, "alice@example.com") || !strings.Contains(out, "42%") {
		t.Errorf("Claude output lost:\n%s", out)
	}
	if !strings.Contains(out, "Codex usage unavailable") {
		t.Errorf("warning missing:\n%s", out)
	}
	if _, err := capture(t, func() error { return run(t, "usage-all", "--json") }); err != nil {
		t.Errorf("--json must also succeed: %v", err)
	}
}

func TestUsageAll_JSONHasCodexField(t *testing.T) {
	home := newTestHome(t)
	seedUsageAllClaude(t, home)
	seedUsageAllCodex(t, home)

	out, err := capture(t, func() error { return run(t, "usage-all", "--json") })
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Accounts []json.RawMessage
		Codex    []struct{ Label, Status string }
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if len(got.Accounts) != 1 || len(got.Codex) != 1 || got.Codex[0].Label != "a" || got.Codex[0].Status != "ok" {
		t.Errorf("got %+v", got)
	}
}
