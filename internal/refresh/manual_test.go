package refresh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/backend/inmem"
)

// fakeManualClaude writes a `claude` on PATH that reproduces the exact
// stdout/stdin protocol the real `claude auth login` uses for its
// no-browser fallback (observed directly from the installed CLI): it
// prints a preamble line, the "If the browser didn't open, visit: <url>"
// line ManualLoginStart scans for, a prompt with no trailing newline, then
// blocks reading one line from stdin. If that line equals wantResponse it
// writes credBody to $CLAUDE_CONFIG_DIR/.credentials.json and exits 0;
// otherwise it exits 1 with a message on stderr, mirroring claude's own
// "Login failed: ..." behavior on a rejected code.
func fakeManualClaude(t *testing.T, authURL, wantResponse, credBody string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/usr/bin/env bash\n" +
		"set -e\n" +
		"echo \"Opening browser to sign in\xe2\x80\xa6\"\n" +
		"echo \"If the browser didn't open, visit: " + authURL + "\"\n" +
		"printf 'Paste code here if prompted > '\n" +
		"read -r response\n" +
		"if [[ \"$response\" != " + shellQuote(wantResponse) + " ]]; then\n" +
		"  echo \"Login failed: Request failed with status code 400\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"cat > \"$CLAUDE_CONFIG_DIR/.credentials.json\" <<'CRED'\n" +
		credBody + "\n" +
		"CRED\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestManualLogin_StartThenFinish_Success(t *testing.T) {
	const authURL = "https://claude.com/cai/oauth/authorize?state=abc"
	const response = "the-pasted-code#the-state"
	const cred = `{"claudeAiOauth":{"accessToken":"manual-AT","refreshToken":"manual-RT","expiresAt":99999999999999}}`
	fakeManualClaude(t, authURL, response, cred)

	stateDir := filepath.Join(t.TempDir(), "pending-login-alice")
	local := inmem.New()

	gotURL, err := ManualLoginStart(context.Background(), "alice@example.com", stateDir, local)
	if err != nil {
		t.Fatalf("ManualLoginStart: %v", err)
	}
	if gotURL != authURL {
		t.Errorf("Start returned %q, want %q", gotURL, authURL)
	}
	if _, err := os.Stat(manualStateMetaPath(stateDir)); err != nil {
		t.Fatalf("Start did not persist state: %v", err)
	}

	data, err := ManualLoginFinish(context.Background(), stateDir, response, local)
	if err != nil {
		t.Fatalf("ManualLoginFinish: %v", err)
	}
	if !strings.Contains(string(data), "manual-AT") {
		t.Fatalf("captured credentials missing expected token:\n%s", data)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Errorf("Finish should clean up stateDir on success, got err=%v", err)
	}
}

func TestManualLogin_FinishWithoutStart(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "never-started")
	if _, err := ManualLoginFinish(context.Background(), stateDir, "any#code", inmem.New()); err == nil {
		t.Fatal("expected an error finishing a login that was never started")
	}
}

func TestManualLogin_RejectedCodeSurfacesClaudesError(t *testing.T) {
	const authURL = "https://claude.com/cai/oauth/authorize?state=abc"
	fakeManualClaude(t, authURL, "the-real-code#the-real-state", `{"claudeAiOauth":{}}`)

	stateDir := filepath.Join(t.TempDir(), "pending-login-bob")
	local := inmem.New()
	if _, err := ManualLoginStart(context.Background(), "bob@example.com", stateDir, local); err != nil {
		t.Fatalf("ManualLoginStart: %v", err)
	}

	_, err := ManualLoginFinish(context.Background(), stateDir, "wrong-code#wrong-state", local)
	if err == nil {
		t.Fatal("expected an error for a rejected code")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should surface claude's own message, got: %v", err)
	}
}

func TestManualLogin_RestartingReplacesAPendingAttempt(t *testing.T) {
	const cred = `{"claudeAiOauth":{"accessToken":"second-AT","refreshToken":"RT","expiresAt":99999999999999}}`
	stateDir := filepath.Join(t.TempDir(), "pending-login-carol")
	local := inmem.New()

	// First attempt: a claude that would hang forever reading stdin,
	// simulating an abandoned login the operator never finished.
	fakeManualClaude(t, "https://claude.com/cai/oauth/authorize?state=first", "never-delivered", cred)
	if _, err := ManualLoginStart(context.Background(), "carol@example.com", stateDir, local); err != nil {
		t.Fatalf("first ManualLoginStart: %v", err)
	}

	// Second attempt at the same stateDir must not hang or error out
	// because of the still-running first process; it replaces it.
	const secondURL = "https://claude.com/cai/oauth/authorize?state=second"
	const secondResponse = "second-code#second-state"
	fakeManualClaude(t, secondURL, secondResponse, cred)
	gotURL, err := ManualLoginStart(context.Background(), "carol@example.com", stateDir, local)
	if err != nil {
		t.Fatalf("second ManualLoginStart: %v", err)
	}
	if gotURL != secondURL {
		t.Fatalf("second Start returned %q, want the second attempt's URL %q", gotURL, secondURL)
	}

	data, err := ManualLoginFinish(context.Background(), stateDir, secondResponse, local)
	if err != nil {
		t.Fatalf("ManualLoginFinish: %v", err)
	}
	if !strings.Contains(string(data), "second-AT") {
		t.Fatalf("captured credentials missing expected token:\n%s", data)
	}
}

func TestManualLoginStart_URLTimeout(t *testing.T) {
	orig := manualURLTimeout
	manualURLTimeout = 200 * time.Millisecond
	defer func() { manualURLTimeout = orig }()

	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A claude that never prints the URL line at all (simulates a stuck
	// startup before reaching the OAuth step).
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/usr/bin/env bash\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(t.TempDir(), "pending-login-dave")
	_, err := ManualLoginStart(context.Background(), "dave@example.com", stateDir, inmem.New())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got: %v", err)
	}
	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Errorf("a timed-out Start should clean up stateDir, got err=%v", statErr)
	}
}
