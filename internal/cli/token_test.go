package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// useSetupToken makes `token create` get tok from a fake sign-in.
func useSetupToken(t *testing.T, tok string) {
	t.Helper()
	old := setupTokenRunner
	setupTokenRunner = func(context.Context, string) (string, error) { return tok, nil }
	t.Cleanup(func() { setupTokenRunner = old })
}

func TestTokenCreate_SavesAndHidesToken(t *testing.T) {
	home := newTestHome(t)
	seqWith(t, "id1", "a@example.com", "id2", "b@example.com")
	useSetupToken(t, testToken)

	out, err := capture(t, func() error { return run(t, "token", "create", "2") })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, testToken) || !strings.Contains(out, "Saved a long-lived token for b@example.com (expires about ") {
		t.Errorf("unexpected output: %q", out)
	}
	var st savedToken
	if err := json.Unmarshal(readCred(t, home, "Claude-Token-b@example.com"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Token != testToken || st.CreatedAt == "" {
		t.Errorf("bad saved token: %+v", st)
	}
}

func TestTokenCreate_RunnerFailureSavesNothing(t *testing.T) {
	home := newTestHome(t)
	seqWith(t, "id1", "a@example.com")
	old := setupTokenRunner
	setupTokenRunner = func(context.Context, string) (string, error) { return "", errors.New("cancelled") }
	t.Cleanup(func() { setupTokenRunner = old })

	if err := run(t, "token", "create", "a@example.com"); err == nil {
		t.Fatal("want an error")
	}
	if got := readCred(t, home, "Claude-Token-a@example.com"); got != nil {
		t.Errorf("saved %q", got)
	}
}

func TestTokenGet_PrintsOnlyTheToken(t *testing.T) {
	home := newTestHome(t)
	seqWith(t, "id1", "a@example.com")
	seedCred(t, home, "Claude-Token-a@example.com", []byte(`{"token":"`+testToken+`","createdAt":"2026-01-01T00:00:00Z"}`))

	out, err := capture(t, func() error { return run(t, "token", "get", "a@example.com") })
	if err != nil {
		t.Fatal(err)
	}
	if out != testToken+"\n" {
		t.Errorf("got %q", out)
	}
}

func TestTokenGet_MissingIsAnError(t *testing.T) {
	newTestHome(t)
	seqWith(t, "id1", "a@example.com")
	if err := run(t, "token", "get", "a@example.com"); err == nil {
		t.Fatal("want an error")
	}
	if err := run(t, "token", "get", "nobody@example.com"); err == nil {
		t.Fatal("want an error for an unknown account")
	}
}

func TestTokenList_ShowsAgeNotToken(t *testing.T) {
	home := newTestHome(t)
	seqWith(t, "id1", "a@example.com", "id2", "b@example.com")
	seedCred(t, home, "Claude-Token-a@example.com", []byte(`{"token":"`+testToken+`","createdAt":"2026-01-01T00:00:00Z"}`))

	out, err := capture(t, func() error { return run(t, "token", "list") })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sk-ant") || !strings.Contains(out, "a@example.com  created 2026-01-01  age ") || strings.Contains(out, "b@example.com") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestTokenRemove_Deletes(t *testing.T) {
	home := newTestHome(t)
	seqWith(t, "id1", "a@example.com")
	seedCred(t, home, "Claude-Token-a@example.com", []byte(`{"token":"`+testToken+`","createdAt":"2026-01-01T00:00:00Z"}`))

	if err := run(t, "token", "remove", "1"); err != nil {
		t.Fatal(err)
	}
	if got := readCred(t, home, "Claude-Token-a@example.com"); got != nil {
		t.Errorf("token still stored: %q", got)
	}
}
