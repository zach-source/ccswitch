package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const testToken = "sk-ant-oat01-AbCdEf0123456789_-AbCdEf0123456789_-AbCdEf0123456789_-AbCdEf0123456789_-AbCdEf0123456789_-AAAA"

func TestParseSetupToken(t *testing.T) {
	cut := 60
	tests := []struct {
		name, in, want string
	}{
		{
			"plain",
			"Your OAuth token (valid for 1 year):\n\n" + testToken + "\n\nStore this token securely.\n",
			testToken,
		},
		{
			"ansi and carriage returns",
			"\x1b[2K\x1b[1A\x1b[32m✓\x1b[39m Long-lived token created\r\n\r\n\x1b[1m" + testToken + "\x1b[22m\r\n\r\nStore it\r\n",
			testToken,
		},
		{
			"wrapped across lines",
			"token:\r\n\r\n" + testToken[:cut] + "\r\n" + testToken[cut:] + "\r\n\r\nStore this token securely.\r\n",
			testToken,
		},
		{
			"wrapped with ansi at the break",
			testToken[:cut] + "\x1b[0m\r\n\x1b[1m" + testToken[cut:] + "\r\n\r\nUse it as CLAUDE_CODE_OAUTH_TOKEN\r\n",
			testToken,
		},
		{
			"redrawn twice keeps the longest",
			"sk-ant-oat01-short\r\n" + testToken + "\r\n",
			testToken,
		},
		{"none", "Browser didn't open? Use the url below\r\nhttps://claude.ai/oauth\r\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSetupToken(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeClaude puts a `claude` script on PATH that prints TUI-like output with
// a wrapped token when run as `claude setup-token`.
func fakeClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\n" +
		"[ \"$1\" = setup-token ] || exit 2\n" +
		"printf '\\033[2K\\033[1AOpening browser...\\r\\n\\033[32m✓\\033[39m Long-lived token created\\r\\n\\r\\n'\n" +
		"printf '%s\\r\\n%s\\r\\n\\r\\nStore this token securely.\\r\\n' '" + testToken[:60] + "' '" + testToken[60:] + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunSetupToken_ReadsTokenFromPty(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1) on this machine")
	}
	fakeClaude(t)
	got, err := runSetupToken(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != testToken {
		t.Errorf("got %q, want %q", got, testToken)
	}
}

func TestRunSetupToken_NoTokenIsAnError(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1) on this machine")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\necho cancelled\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := runSetupToken(context.Background(), ""); err == nil {
		t.Fatal("want an error when claude prints no token")
	}
}
