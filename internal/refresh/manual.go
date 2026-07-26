package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/browser"
)

// claudeManualURLPrefix is the exact line prefix `claude auth login` prints
// when it cannot (or, with browser.InstallNoOpener, deliberately does not)
// open a local browser: "If the browser didn't open, visit: <url>". Every
// call site that needs the URL scans for this prefix rather than parsing
// or reconstructing the URL itself — see ManualLoginStart's doc comment
// for why.
const claudeManualURLPrefix = "If the browser didn't open, visit: "

// manualURLTimeout bounds how long ManualLoginStart waits for claude to
// print the authorize URL before giving up. In practice claude prints it
// within a second or two; this only guards against claude hanging on
// something else (a broken CLAUDE_CONFIG_DIR, a stuck network call) before
// ever reaching that point. A var (not a const) solely so the conformance
// suite can shorten it instead of a real test waiting out the default.
var manualURLTimeout = 20 * time.Second

// manualResponseDeliveryTimeout bounds how long ManualLoginFinish waits for
// a reader to be present on the response FIFO. The child opened its end
// (read-write, so it never blocks) back in ManualLoginStart and stays
// blocked in read() until Finish writes to it, so this should return
// immediately in every real case; it only guards against a login process
// that died between Start and Finish without Finish noticing.
var manualResponseDeliveryTimeout = 10 * time.Second

// manualExitTimeout bounds how long ManualLoginFinish waits for the login
// process to exit after delivering the response — the token exchange is a
// single fast network call once claude has the code.
var manualExitTimeout = 30 * time.Second

// manualLoginMeta is the on-disk record ManualLoginStart leaves for a later,
// independent ManualLoginFinish call — potentially in a different process —
// to resume the login it began.
type manualLoginMeta struct {
	Email          string `json:"email"`
	BeforeActive   []byte `json:"before_active,omitempty"`
	SinceUnixMilli int64  `json:"since_unix_milli"`
}

func manualStateConfigDir(stateDir string) string  { return filepath.Join(stateDir, "config") }
func manualStateFifoPath(stateDir string) string   { return filepath.Join(stateDir, "stdin.fifo") }
func manualStateStdoutLog(stateDir string) string  { return filepath.Join(stateDir, "stdout.log") }
func manualStateStderrLog(stateDir string) string  { return filepath.Join(stateDir, "stderr.log") }
func manualStateMetaPath(stateDir string) string   { return filepath.Join(stateDir, "meta.json") }
func manualStateDonePath(stateDir string) string   { return filepath.Join(stateDir, "done") }
func manualStateRunnerPath(stateDir string) string { return filepath.Join(stateDir, "run.sh") }

// manualRunnerScript wraps the real login command so completion can be
// detected without relying on the OS process table: exec.Cmd.Start() never
// calls Wait() here (the whole point is that claude outlives this call), so
// the child would otherwise sit as a zombie indefinitely from this
// process's point of view, and a completely separate later process (the
// Finish half) has no standing to wait() on it at all. A sentinel *file*,
// written by this wrapper once "$@" (claude) actually exits, sidesteps
// process-table semantics entirely — plain file checks work identically
// whether Start and Finish run in the same process or different ones.
const manualRunnerScript = "#!/bin/sh\n" +
	"\"$@\"\n" +
	"echo $? > \"$(dirname \"$0\")/done\"\n"

// ManualLoginStart begins a browser-free login for one account and returns
// as soon as the authorize URL is known — it does not wait for the
// operator's response. That is ManualLoginFinish's job, in a later, separate
// call that can come from an entirely different process (a second `ccswitch`
// invocation), which is the whole point of the split: the operator needs
// time to open the URL on another device and approve it, and nothing should
// have to stay attached to a live terminal in the meantime.
//
// claude already implements exactly this fallback: whenever it cannot open
// a local browser, it prints the authorize URL as plain text and then waits
// on stdin for the operator to paste back the response (the same
// "<code>#<state>" string a real browser redirect would otherwise deliver).
// ManualLoginStart/Finish do not reimplement any part of that OAuth exchange
// — Claude Code's client ID, scopes, redirect URI, and PKCE handling all
// stay inside the `claude` binary, so ccswitch never has to track them or go
// stale when Anthropic changes them. This code only:
//
//  1. Guarantees claude's manual fallback always fires, by shadowing
//     `open`/`xdg-open` on PATH with a shim that always fails
//     (browser.InstallNoOpener) — otherwise claude would try to launch a
//     real local browser window whenever one happens to be available.
//  2. Launches claude detached (its own session, stdio redirected to files
//     and a named pipe under stateDir) so it outlives this call — and this
//     whole process — and resumes it later from the state left behind.
//
// Any unfinished attempt previously started at stateDir is replaced: its
// state is removed first, so starting again is always safe to just retry.
//
// ponytail: a replaced attempt's claude process is not killed, just
// abandoned — it sits blocked reading a now-orphaned fifo until the
// session ends. Cheap and harmless (no CPU use, no credentials at risk),
// and avoids the signal-propagation plumbing a supervised kill would need
// now that the process is invoked through manualRunnerScript rather than
// directly. Upgrade path: have the runner script trap and forward TERM if
// abandoned attempts ever prove to matter in practice.
//
// Scope note: like LoginRotate, this re-authenticates an account ccswitch
// already manages (one already present in sequence.json). It does not
// bootstrap a brand-new account from nothing — that still needs a live
// `claude` identity for `add-account` to capture first.
func ManualLoginStart(ctx context.Context, email, stateDir string, local backend.Backend) (string, error) {
	cleanupPendingLogin(stateDir)

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", fmt.Errorf("manual login: create state dir: %w", err)
	}
	tmpConfig := manualStateConfigDir(stateDir)
	if err := os.MkdirAll(tmpConfig, 0o700); err != nil {
		return "", fmt.Errorf("manual login: create config dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpConfig, ".claude.json"), []byte(seedJSON), 0o600); err != nil {
		return "", fmt.Errorf("manual login: seed claude.json: %w", err)
	}
	noOpenerDir := filepath.Join(stateDir, "bin")
	if err := browser.InstallNoOpener(noOpenerDir); err != nil {
		return "", fmt.Errorf("manual login: install no-browser shim: %w", err)
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("manual login: claude CLI not found in PATH: %w", err)
	}
	runnerPath := manualStateRunnerPath(stateDir)
	if err := os.WriteFile(runnerPath, []byte(manualRunnerScript), 0o700); err != nil {
		return "", fmt.Errorf("manual login: write runner script: %w", err)
	}

	fifoPath := manualStateFifoPath(stateDir)
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		return "", fmt.Errorf("manual login: create response fifo: %w", err)
	}
	// O_RDWR so opening it here never blocks waiting for a writer — the
	// writer (ManualLoginFinish) may not show up for minutes, in a
	// different process entirely. The child's read() blocks normally on
	// its own (dup'd) copy of this same fd regardless of how our end was
	// opened; this is the standard trick for holding a FIFO reader ready
	// with no peer connected yet.
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("manual login: open response fifo: %w", err)
	}
	defer fifo.Close()

	stdoutFile, err := os.Create(manualStateStdoutLog(stateDir))
	if err != nil {
		return "", fmt.Errorf("manual login: create stdout log: %w", err)
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(manualStateStderrLog(stateDir))
	if err != nil {
		return "", fmt.Errorf("manual login: create stderr log: %w", err)
	}
	defer stderrFile.Close()

	args := []string{claudePath, "auth", "login"}
	if email != "" {
		args = append(args, "--email", email)
	}
	// Deliberately exec.Command, not exec.CommandContext: this child must
	// outlive ctx — and this entire process — by design. Run through
	// manualRunnerScript (not claudePath directly) so completion can be
	// detected via a sentinel file rather than the process table — see its
	// doc comment for why. Its stdio is fully redirected to files/the fifo
	// (never a terminal), and Setsid detaches it into its own session so a
	// closing terminal can't SIGHUP it either.
	cmd := exec.Command(runnerPath, args...)
	cmd.Dir = stateDir
	cmd.Env = prependPath(append(filteredEnv(), "CLAUDE_CONFIG_DIR="+tmpConfig), noOpenerDir)
	cmd.Stdin = fifo
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	var beforeActive []byte
	if local != nil {
		beforeActive, _ = local.Read(ctx, account.ActiveCredKey)
	}
	since := time.Now()

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("manual login: start claude: %w", err)
	}
	// No cmd.Wait() here — see the doc comment above.

	url, err := waitForURLLine(ctx, manualStateStdoutLog(stateDir), claudeManualURLPrefix, manualURLTimeout)
	if err != nil {
		_ = os.RemoveAll(stateDir)
		return "", err
	}

	metaBytes, err := json.Marshal(manualLoginMeta{
		Email:          email,
		BeforeActive:   beforeActive,
		SinceUnixMilli: since.UnixMilli(),
	})
	if err != nil {
		_ = os.RemoveAll(stateDir)
		return "", fmt.Errorf("manual login: marshal state: %w", err)
	}
	if err := os.WriteFile(manualStateMetaPath(stateDir), metaBytes, 0o600); err != nil {
		_ = os.RemoveAll(stateDir)
		return "", fmt.Errorf("manual login: persist state: %w", err)
	}
	return url, nil
}

// ManualLoginFinish delivers the operator's response ("<code>#<state>",
// exactly what claude's own success page shows) to the login process a
// prior ManualLoginStart began at stateDir, waits for the exchange to
// complete, and returns the captured credential bytes exactly as claude
// wrote them — callers persist these the same way LoginRotate/RefreshOne
// do (never re-marshal). stateDir is cleaned up on both success and
// failure once the underlying process has actually exited; it is left in
// place if delivery or the exit wait times out, so the caller can inspect
// stdout.log/stderr.log or simply retry.
func ManualLoginFinish(ctx context.Context, stateDir, code string, local backend.Backend) ([]byte, error) {
	metaBytes, err := os.ReadFile(manualStateMetaPath(stateDir))
	if err != nil {
		return nil, fmt.Errorf("manual login: no login in progress at %s — run --manual (without --code) first: %w", stateDir, err)
	}
	var meta manualLoginMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("manual login: corrupt state at %s: %w", stateDir, err)
	}

	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("manual login: empty code")
	}

	if err := deliverResponse(ctx, manualStateFifoPath(stateDir), code, manualResponseDeliveryTimeout); err != nil {
		return nil, fmt.Errorf("manual login: %w", err)
	}

	if err := waitForSentinel(ctx, manualStateDonePath(stateDir), manualExitTimeout); err != nil {
		return nil, fmt.Errorf("manual login: %w", err)
	}

	since := time.UnixMilli(meta.SinceUnixMilli)
	newData, hashedSvc := captureClaudeCredential(ctx, manualStateConfigDir(stateDir), nil, local, meta.BeforeActive, since)
	if len(newData) == 0 {
		stderrBytes, _ := os.ReadFile(manualStateStderrLog(stateDir))
		msg := strings.TrimSpace(string(stderrBytes))
		_ = os.RemoveAll(stateDir)
		if msg != "" {
			return nil, fmt.Errorf("manual login: %s", msg)
		}
		return nil, fmt.Errorf("manual login: claude exited but no credentials were captured")
	}
	if hashedSvc != "" && local != nil {
		_ = local.Delete(ctx, hashedSvc)
	}
	_ = os.RemoveAll(stateDir)
	return newData, nil
}

// cleanupPendingLogin removes any state a previous ManualLoginStart left at
// stateDir, so starting again is always safe to just retry. It does not try
// to kill that attempt's claude process — see ManualLoginStart's doc
// comment for why that's an acceptable, deliberate tradeoff.
func cleanupPendingLogin(stateDir string) {
	_ = os.RemoveAll(stateDir)
}

// deliverResponse writes response to the fifo at path, in a goroutine so a
// wedged open (no reader present at all — the login process died
// unexpectedly) times out instead of hanging forever.
func deliverResponse(ctx context.Context, path, response string, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			done <- err
			return
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, response)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("deliver response: %w", err)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timed out delivering the response — is the login process still running?")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitForSentinel polls for path to appear — manualRunnerScript creates it
// only after the wrapped login process has actually exited. Deliberately
// not a process-table check (kill(pid, 0) or Wait()): this process did not
// start that child (a separate ManualLoginStart invocation, possibly a
// separate OS process, did), so it has no standing to wait() on it, and a
// zombie still answers kill(pid, 0) as "alive" until someone reaps it.
func waitForSentinel(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for the login process to finish")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// waitForURLLine polls logPath for a line with the given prefix, returning
// the remainder of that line. It re-reads from the last known offset each
// poll rather than holding a single open handle, so it tolerates the log
// file not existing yet at the very first poll.
func waitForURLLine(ctx context.Context, logPath, prefix string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var offset int64
	var pending string
	for {
		if data, err := readFrom(logPath, offset); err == nil {
			offset += int64(len(data))
			pending += string(data)
			for {
				i := strings.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := pending[:i]
				pending = pending[i+1:]
				if rest, ok := strings.CutPrefix(line, prefix); ok {
					return rest, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for claude to print the authorize URL")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func readFrom(path string, offset int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
