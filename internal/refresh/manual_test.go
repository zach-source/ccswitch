package refresh

import (
	"bytes"
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
// line ManualLogin scans for, a prompt with no trailing newline, then
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

func TestManualLogin_SuccessWithStdinResponse(t *testing.T) {
	const authURL = "https://claude.com/cai/oauth/authorize?state=abc"
	const response = "the-pasted-code#the-state"
	const cred = `{"claudeAiOauth":{"accessToken":"manual-AT","refreshToken":"manual-RT","expiresAt":99999999999999}}`
	fakeManualClaude(t, authURL, response, cred)

	var gotURL string
	local := inmem.New()
	data, err := ManualLogin(context.Background(), "alice@example.com", "", strings.NewReader(response+"\n"), local,
		func(url string) { gotURL = url })
	if err != nil {
		t.Fatalf("ManualLogin: %v", err)
	}
	if gotURL != authURL {
		t.Errorf("onURL got %q, want %q", gotURL, authURL)
	}
	if !strings.Contains(string(data), "manual-AT") {
		t.Fatalf("captured credentials missing expected token:\n%s", data)
	}
}

func TestManualLogin_NonInteractiveCodeFlag(t *testing.T) {
	const authURL = "https://claude.com/cai/oauth/authorize?state=xyz"
	const response = "code-from-flag#state-from-flag"
	const cred = `{"claudeAiOauth":{"accessToken":"flag-AT","refreshToken":"flag-RT","expiresAt":99999999999999}}`
	fakeManualClaude(t, authURL, response, cred)

	// stdin is a reader that errors if ever touched — --code must bypass it.
	local := inmem.New()
	data, err := ManualLogin(context.Background(), "", response, errorReader{}, local, nil)
	if err != nil {
		t.Fatalf("ManualLogin: %v", err)
	}
	if !strings.Contains(string(data), "flag-AT") {
		t.Fatalf("captured credentials missing expected token:\n%s", data)
	}
}

func TestManualLogin_RejectedCodeSurfacesClaudesError(t *testing.T) {
	const authURL = "https://claude.com/cai/oauth/authorize?state=abc"
	fakeManualClaude(t, authURL, "the-real-code#the-real-state", `{"claudeAiOauth":{}}`)

	local := inmem.New()
	_, err := ManualLogin(context.Background(), "", "wrong-code#wrong-state", nil, local, nil)
	if err == nil {
		t.Fatal("expected an error for a rejected code")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should surface claude's own message, got: %v", err)
	}
}

func TestManualLogin_URLTimeout(t *testing.T) {
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

	_, err := ManualLogin(context.Background(), "", "irrelevant", nil, inmem.New(), nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got: %v", err)
	}
}

// errorReader always fails — used to prove --code bypasses stdin entirely.
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, bytes.ErrTooLarge }
