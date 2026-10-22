package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures copy the text that Codex CLI 0.160.1 prints. The device prompt
// comes from device_code_prompt in codex-rs/login/src/device_code_auth.rs and
// the browser fallback from print_login_server_start in codex-rs/cli/src/
// login.rs. The codes and URLs are fakes.
const (
	ansiBlue  = "\x1b[94m"
	ansiGray  = "\x1b[90m"
	ansiReset = "\x1b[0m"
)

const devicePromptANSI = "\nWelcome to Codex [v" + ansiGray + "0.160.1" + ansiReset + "]\n" +
	ansiGray + "OpenAI's command-line coding agent" + ansiReset + "\n" +
	"\nFollow these steps to sign in with ChatGPT using device code authorization:\n" +
	"\n1. Open this link in your browser and sign in to your account\n   " +
	ansiBlue + "https://auth.example.test/codex/device" + ansiReset + "\n" +
	"\n2. Enter this one-time code " + ansiGray + "(expires in 15 minutes)" + ansiReset + "\n   " +
	ansiBlue + "ABCD-12345" + ansiReset + "\n" +
	"\n" + ansiGray + "Continue only if you started this login in Codex. If a website or another person gave you this code, cancel." + ansiReset + "\n"

const browserFallback = "Starting local login server on http://localhost:1455.\n" +
	"If your browser did not open, navigate to this URL to authenticate:\n\n" +
	"https://auth.example.test/oauth/authorize?client_id=app_fake&state=xyz\n\n" +
	"On a remote or headless machine? Use `codex login --device-auth` instead.\n"

func feedAll(d *DeviceLogin, text string) {
	for _, line := range strings.Split(text, "\n") {
		d.Feed(line)
	}
}

func TestDeviceLogin_DeviceFormat(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"with ANSI", devicePromptANSI},
		{"plain", stripANSI(devicePromptANSI)},
		{"CRLF line ends", strings.ReplaceAll(stripANSI(devicePromptANSI), "\n", "\r\n")},
		{"inline code", "1. Open this link in your browser and sign in to your account\n" +
			"   https://auth.example.test/codex/device\n" +
			"2. Enter this one-time code ABCD-12345 (expires in 15 minutes)\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d DeviceLogin
			feedAll(&d, tc.text)
			if !d.Ready() {
				t.Fatalf("not ready: %+v", d)
			}
			if d.URL != "https://auth.example.test/codex/device" {
				t.Errorf("URL = %q", d.URL)
			}
			if d.Code != "ABCD-12345" {
				t.Errorf("Code = %q", d.Code)
			}
			if d.BrowserFallback {
				t.Error("BrowserFallback set for the device format")
			}
		})
	}
}

func TestDeviceLogin_NotReadyUntilBoth(t *testing.T) {
	var d DeviceLogin
	d.Feed("1. Open this link in your browser and sign in to your account")
	d.Feed("   " + ansiBlue + "https://auth.example.test/codex/device" + ansiReset)
	if d.Ready() {
		t.Fatal("ready with the URL only")
	}
	d.Feed("2. Enter this one-time code " + ansiGray + "(expires in 15 minutes)" + ansiReset)
	if d.Ready() {
		t.Fatal("ready before the code line")
	}
	d.Feed("   " + ansiBlue + "ABCD-12345" + ansiReset)
	if !d.Ready() {
		t.Fatal("not ready with both")
	}
}

func TestDeviceLogin_BrowserFallback(t *testing.T) {
	var d DeviceLogin
	feedAll(&d, browserFallback)
	if !d.BrowserFallback {
		t.Fatal("BrowserFallback not set")
	}
	if want := "https://auth.example.test/oauth/authorize?client_id=app_fake&state=xyz"; d.URL != want {
		t.Errorf("URL = %q, want %q", d.URL, want)
	}
	if d.Code != "" {
		t.Errorf("Code = %q, want empty", d.Code)
	}
	if d.Ready() {
		t.Error("Ready true in the browser fallback")
	}
}

func TestDeviceLogin_IgnoresUnrelatedLines(t *testing.T) {
	var d DeviceLogin
	for _, l := range []string{
		"",
		"Welcome to Codex [v" + ansiGray + "0.160.1" + ansiReset + "]",
		"See https://example.test/docs for help",
		"Follow these steps to sign in with ChatGPT using device code authorization:",
		"Successfully logged in",
	} {
		d.Feed(l)
	}
	if d.URL != "" || d.Code != "" || d.BrowserFallback || d.Ready() {
		t.Fatalf("unrelated lines changed state: %+v", d)
	}
}

func TestDeviceLogin_KeepsFirstURL(t *testing.T) {
	var d DeviceLogin
	d.Feed("1. Open this link in your browser and sign in to your account")
	d.Feed("   https://auth.example.test/codex/device")
	d.Feed("   https://other.example.test/")
	if d.URL != "https://auth.example.test/codex/device" {
		t.Errorf("URL = %q", d.URL)
	}
}

func TestStripANSI(t *testing.T) {
	if got := stripANSI(ansiBlue + "x" + ansiReset + ansiGray + "y"); got != "xy" {
		t.Errorf("stripANSI = %q", got)
	}
}

// fakeCodex puts a `codex` script first on PATH. The script records its
// CODEX_HOME in homeFile, prints the device prompt, then runs body.
func fakeCodex(t *testing.T, body string) (homeFile string) {
	t.Helper()
	dir := t.TempDir()
	homeFile = filepath.Join(dir, "home")
	script := "#!/bin/sh\n" +
		"printf %s \"$CODEX_HOME\" > '" + homeFile + "'\n" +
		"printf '   \\033[94mhttps://auth.example.test/codex/device\\033[0m\\n' >&2\n" +
		"printf '1. Open this link in your browser and sign in to your account\\n'\n" +
		"printf '   \\033[94mhttps://auth.example.test/codex/device\\033[0m\\n'\n" +
		"printf '2. Enter this one-time code (expires in 15 minutes)\\n'\n" +
		"printf '   \\033[94mABCD-12345\\033[0m\\n'\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return homeFile
}

func TestSignIn_ReturnsAuthAndRemovesTempHome(t *testing.T) {
	homeFile := fakeCodex(t, `printf '{"auth_mode":"chatgpt"}' > "$CODEX_HOME/auth.json"`)
	lock := filepath.Join(t.TempDir(), "login.lock")
	var got DeviceLogin
	calls := 0
	blob, err := SignIn(context.Background(), lock, func(d DeviceLogin) { got, calls = d, calls+1 })
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if string(blob) != `{"auth_mode":"chatgpt"}` {
		t.Errorf("blob = %q", blob)
	}
	if calls != 1 || got.URL != "https://auth.example.test/codex/device" || got.Code != "ABCD-12345" {
		t.Errorf("callback calls=%d login=%+v", calls, got)
	}
	home, _ := os.ReadFile(homeFile)
	if _, err := os.Stat(string(home)); !os.IsNotExist(err) {
		t.Errorf("temp CODEX_HOME %q still exists", home)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("lock was not released")
	}
}

func TestSignIn_FailureRemovesTempHome(t *testing.T) {
	homeFile := fakeCodex(t, `printf 'secret-token' > "$CODEX_HOME/auth.json"; exit 3`)
	_, err := SignIn(context.Background(), filepath.Join(t.TempDir(), "login.lock"), func(DeviceLogin) {})
	if err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks credential text: %v", err)
	}
	home, _ := os.ReadFile(homeFile)
	if _, err := os.Stat(string(home)); !os.IsNotExist(err) {
		t.Errorf("temp CODEX_HOME %q still exists", home)
	}
}

func TestSignIn_RefusesWhenLockHeld(t *testing.T) {
	fakeCodex(t, "exit 0")
	lock := filepath.Join(t.TempDir(), "login.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := SignIn(context.Background(), lock, func(DeviceLogin) {})
	if err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("err = %v, want an error about another sign-in", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Error("a held lock must stay in place")
	}
}
