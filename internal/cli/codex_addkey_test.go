package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/codex"
)

// fakeCodexAPIKey puts a `codex` on PATH that stores its stdin as the key.
func fakeCodexAPIKey(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const fakeCodexStoresKey = `key=$(cat)
printf '{"auth_mode":"apikey","OPENAI_API_KEY":"%s"}' "$key" > "$CODEX_HOME/auth.json"`

func TestCodexAddKey_SavesProfileAndLeavesLiveLoginAlone(t *testing.T) {
	home := newTestHome(t)
	live := codexBlob("live-rt", "live@example.com")
	newCodexHome(t, live)
	addCodexProfile(t, "work", "work-rt", "work@example.com")
	if err := codex.WriteLive([]byte(live)); err != nil {
		t.Fatal(err)
	}
	fakeCodexAPIKey(t, fakeCodexStoresKey)

	if err := runWithStdin(t, "  sk-ci-key\n", "codex", "add-key", "ci"); err != nil {
		t.Fatalf("codex add-key: %v", err)
	}

	got := readCred(t, home, codex.CredKey("ci"))
	if string(got) != `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-ci-key"}` {
		t.Errorf("stored credential = %s", got)
	}
	reg := readCodexRegistry(t)
	p := reg.Profiles["ci"]
	if p.Label != "ci" || p.Mode != "apikey" || p.Email != "" || p.Plan != "" || p.AccountID != "" {
		t.Errorf("profile = %+v", p)
	}
	if last := reg.Priority[len(reg.Priority)-1]; last != "ci" {
		t.Errorf("priority = %v", reg.Priority)
	}
	if b, _ := os.ReadFile(codex.AuthPath()); string(b) != live {
		t.Errorf("live login changed: %s", b)
	}
}

func TestCodexAddKey_RejectsEmptyKey(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, "")
	fakeCodexAPIKey(t, fakeCodexStoresKey)

	if err := runWithStdin(t, " \n", "codex", "add-key", "ci"); err == nil {
		t.Fatal("want an error for an empty key")
	}
	if _, ok := readCodexRegistry(t).Profiles["ci"]; ok {
		t.Error("profile was saved")
	}
}

func TestCodexAddKey_CodexFailureDoesNotLeakKey(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, "")
	fakeCodexAPIKey(t, `cat >&2; exit 3`)

	err := runWithStdin(t, "sk-secret-789\n", "codex", "add-key", "ci")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "sk-secret-789") {
		t.Errorf("error leaks key: %v", err)
	}
	if _, ok := readCodexRegistry(t).Profiles["ci"]; ok {
		t.Error("profile was saved")
	}
}

func TestCodexAddKey_RejectsNonAPIKeyAuth(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, "")
	fakeCodexAPIKey(t, `cat >/dev/null; printf '{"auth_mode":"chatgpt"}' > "$CODEX_HOME/auth.json"`)

	if err := runWithStdin(t, "sk-x\n", "codex", "add-key", "ci"); err == nil {
		t.Fatal("want an error when codex writes a non-apikey login")
	}
}
