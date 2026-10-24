package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

// DeviceLogin collects what `codex login --device-auth` prints. Codex 0.160.1
// prints, with ANSI color codes around the values:
//
//  1. Open this link in your browser and sign in to your account
//     https://auth.openai.com/codex/device
//
//  2. Enter this one-time code (expires in 15 minutes)
//     ABCD-12345
//
// When device auth is not available, Codex falls back to its browser login.
// It opens the default browser by itself and prints the sign-in address:
//
//	Starting local login server on http://localhost:1455.
//	If your browser did not open, navigate to this URL to authenticate:
//
//	https://auth.openai.com/oauth/authorize?...
//
// ccswitch parses this text only to show or open the address. A format change
// upstream leaves Ready false, and the caller reports that instead of guessing.
type DeviceLogin struct {
	URL             string // the verification address
	Code            string // the one-time code
	BrowserFallback bool   // Codex fell back to the browser login (it opens the default browser itself)

	wantURL  bool // an "Open this link" or fallback line came, and no URL yet
	wantCode bool // the "one-time code" line came, and no code yet
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// stripANSI removes color and style escape sequences. Codex colors the URL and
// code, and a pipe does not turn that off.
func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// Feed reads one output line (stdout or stderr). Each line stands alone, so
// the caller can split the output at any line break the real output has.
func (d *DeviceLogin) Feed(line string) {
	line = strings.TrimSpace(stripANSI(line))
	if line == "" {
		return
	}
	switch {
	case strings.Contains(line, "Starting local login server on"):
		d.BrowserFallback = true
		d.wantURL = d.URL == ""
		return
	case strings.Contains(line, "Open this link"):
		d.wantURL = d.URL == ""
	case strings.Contains(line, "one-time code"):
		d.wantCode = d.Code == ""
		d.takeInlineCode(line)
		return
	}
	if d.wantURL {
		if u := firstHTTPS(line); u != "" {
			d.URL, d.wantURL = u, false
		}
		return
	}
	if d.wantCode {
		// The code is the first word of the line after the step line.
		d.Code, d.wantCode = strings.Fields(line)[0], false
	}
}

// takeInlineCode handles a format that puts the code on the step line, as in
// "Enter this one-time code ABCD-12345". The "(expires …)" note is not a code.
func (d *DeviceLogin) takeInlineCode(line string) {
	_, rest, _ := strings.Cut(line, "one-time code")
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.HasPrefix(rest, "(") {
		return
	}
	d.Code, d.wantCode = strings.Fields(rest)[0], false
}

// Ready reports whether both the URL and the code are known.
func (d *DeviceLogin) Ready() bool { return d.URL != "" && d.Code != "" }

// firstHTTPS returns the first https:// URL in line, or "".
func firstHTTPS(line string) string {
	_, rest, found := strings.Cut(line, "https://")
	if !found {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return "https://" + fields[0]
}

// SignIn runs `codex login --device-auth` in a new temporary CODEX_HOME and
// returns the auth.json it wrote. The live login is never touched and the
// temporary directory is always deleted. onReady runs once, when the address
// and the code are known (or when Codex fell back to the browser login).
//
// lockPath is a flock file. The kernel releases it when the process stops, so
// a killed sign-in does not block the next one. Codex listens on port 1455 for the browser
// login, so two sign-ins cannot run together.
func SignIn(ctx context.Context, lockPath string, onReady func(DeviceLogin)) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	unlock, err := takeSignInLock(lockPath)
	if err != nil {
		return nil, err
	}
	defer unlock()

	home, err := os.MkdirTemp("", "ccswitch-codex-")
	if err != nil {
		return nil, fmt.Errorf("create temporary CODEX_HOME: %w", err)
	}
	defer os.RemoveAll(home)
	if err := os.Chmod(home, 0o700); err != nil {
		return nil, fmt.Errorf("protect temporary CODEX_HOME: %w", err)
	}

	cmd := exec.CommandContext(ctx, "codex", "login", "--device-auth")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	var (
		mu     sync.Mutex
		login  DeviceLogin
		called bool
		wg     sync.WaitGroup
	)
	feed := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			mu.Lock()
			login.Feed(sc.Text())
			if !called && (login.Ready() || login.URL != "" && login.BrowserFallback) {
				called = true
				onReady(login)
			}
			mu.Unlock()
		}
	}
	wg.Add(2)
	go feed(stdout)
	go feed(stderr)
	wg.Wait()
	// The output is never put in the error: it can hold a code or a token.
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("codex login failed: %w", err)
	}

	blob, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return nil, fmt.Errorf("codex login wrote no auth.json: %w", err)
	}
	return blob, nil
}

// takeSignInLock takes a non-blocking exclusive flock on path. It fails when
// another process holds the lock.
func takeSignInLock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("take sign-in lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another Codex sign-in is running; wait for it to finish")
		}
		return nil, fmt.Errorf("take sign-in lock: %w", err)
	}
	return func() { f.Close() }, nil
}
