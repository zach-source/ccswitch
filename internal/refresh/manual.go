package refresh

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/browser"
)

// claudeManualURLPrefix is the exact line prefix `claude auth login` prints
// when it cannot (or, with browser.InstallNoOpener, deliberately does not)
// open a local browser: "If the browser didn't open, visit: <url>". Every
// call site that needs the URL scans for this prefix rather than parsing
// or reconstructing the URL itself — see ManualLogin's doc comment for why.
const claudeManualURLPrefix = "If the browser didn't open, visit: "

// manualURLTimeout bounds how long ManualLogin waits for claude to print
// the authorize URL before giving up. In practice claude prints it within
// a second or two; this only guards against claude hanging on something
// else (a broken CLAUDE_CONFIG_DIR, a stuck network call) before ever
// reaching that point. A var (not a const) solely so the conformance suite
// can shorten it instead of a real test waiting out the default.
var manualURLTimeout = 20 * time.Second

// ManualLogin drives `claude auth login` for one account with a real local
// browser deliberately disabled, so the OAuth challenge can be completed on
// any device — a phone, a different machine's browser, anything with a
// screen — while the machine actually running ccswitch (and claude) stays
// headless: no display, no signed-in browser, over SSH, under launchd,
// whatever.
//
// claude already implements exactly this fallback: whenever it cannot open
// a local browser, it prints the authorize URL as plain text and then waits
// on stdin for the operator to paste back the response (the same
// "<code>#<state>" string a real browser redirect would otherwise deliver).
// ManualLogin does not reimplement any part of that OAuth exchange — Claude
// Code's client ID, scopes, redirect URI, and PKCE handling stay entirely
// inside the `claude` binary, so ccswitch never has to track them or go
// stale when Anthropic changes them. ManualLogin only does two things:
//
//  1. Guarantees claude's manual fallback always fires, by shadowing
//     `open`/`xdg-open` on PATH with a shim that always fails
//     (browser.InstallNoOpener) — otherwise claude would try to launch a
//     real local browser window whenever one happens to be available.
//  2. Wires the two halves of that fallback to CLI-friendly hooks: onURL
//     receives the authorize URL as soon as claude prints it, and the
//     operator's response is delivered however the caller has it —
//     supplied directly (non-interactive/scripted use) or read live from
//     an io.Reader (a real terminal, or anything piping the response in).
//
// onURL is called exactly once, synchronously, before a response is
// needed — the interactive CLI path prints it to the terminal; a scripted
// caller can relay it anywhere (a log line, a chat message, ...). code
// supplies the operator's response ready-made; when code is "", ManualLogin
// instead reads one line from stdin, which is what makes both interactive
// (a human typing) and non-interactive (`echo "$CODE" | ccswitch login
// --manual ...`) use work through the same code path.
//
// Returns the raw credential bytes exactly as claude wrote them — callers
// persist these the same way LoginRotate/RefreshOne do (never re-marshal).
//
// Scope note: like LoginRotate, ManualLogin re-authenticates an account
// ccswitch already manages (one already present in sequence.json). It does
// not bootstrap a brand-new account from nothing — that still needs a live
// `claude` identity for `add-account` to capture first.
func ManualLogin(
	ctx context.Context,
	email string,
	code string,
	stdin io.Reader,
	local backend.Backend,
	onURL func(url string),
) ([]byte, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("manual login: claude CLI not found in PATH: %w", err)
	}

	tmpConfig, err := os.MkdirTemp("", "ccswitch-manual-login-config-*")
	if err != nil {
		return nil, fmt.Errorf("manual login: create config tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpConfig)
	tmpWork, err := os.MkdirTemp("", "ccswitch-manual-login-work-*")
	if err != nil {
		return nil, fmt.Errorf("manual login: create work tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpWork)
	if err := os.WriteFile(filepath.Join(tmpConfig, ".claude.json"), []byte(seedJSON), 0o600); err != nil {
		return nil, fmt.Errorf("manual login: seed claude.json: %w", err)
	}

	noOpenerDir := filepath.Join(tmpWork, "bin")
	if err := browser.InstallNoOpener(noOpenerDir); err != nil {
		return nil, fmt.Errorf("manual login: install no-browser shim: %w", err)
	}

	args := []string{"auth", "login"}
	if email != "" {
		args = append(args, "--email", email)
	}
	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = tmpWork
	cmd.Env = prependPath(append(filteredEnv(), "CLAUDE_CONFIG_DIR="+tmpConfig), noOpenerDir)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("manual login: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("manual login: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	// Snapshot AND the wall-clock so a post-run change can be attributed to
	// this run, matching RefreshOne/LoginRotate's capture strategy.
	var beforeActive []byte
	if local != nil {
		beforeActive, _ = local.Read(ctx, account.ActiveCredKey)
	}
	since := time.Now()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("manual login: start claude: %w", err)
	}

	// Scan claude's stdout in the background for the authorize-URL line.
	// The scanner keeps draining stdout for the lifetime of the process —
	// including the prompt text after it, which has no trailing newline
	// and is never itself observed here — so claude can never block on a
	// full stdout pipe.
	urlCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			if rest, ok := strings.CutPrefix(scanner.Text(), claudeManualURLPrefix); ok {
				select {
				case urlCh <- rest:
				default:
				}
			}
		}
	}()

	var authURL string
	select {
	case authURL = <-urlCh:
	case <-time.After(manualURLTimeout):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("manual login: timed out waiting for claude to print the authorize URL")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}
	if onURL != nil {
		onURL(authURL)
	}

	response := code
	if response == "" {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && line == "" {
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("manual login: read response: %w", err)
		}
		response = line
	}
	response = strings.TrimSpace(response)
	if response == "" {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("manual login: empty response")
	}
	if _, err := fmt.Fprintln(stdinPipe, response); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("manual login: write response: %w", err)
	}
	_ = stdinPipe.Close()

	if waitErr := cmd.Wait(); waitErr != nil {
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return nil, fmt.Errorf("manual login: %s", msg)
	}

	newData, hashedSvc := captureClaudeCredential(ctx, tmpConfig, nil, local, beforeActive, since)
	if len(newData) == 0 {
		return nil, fmt.Errorf("manual login: claude exited successfully but no credentials were captured")
	}
	if hashedSvc != "" && local != nil {
		_ = local.Delete(ctx, hashedSvc)
	}
	return newData, nil
}
