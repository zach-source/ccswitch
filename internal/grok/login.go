package grok

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/zach-source/ccswitch/internal/provider"
)

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// codePattern is an XXXX-XXXX style one-time code.
	codePattern = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)
	urlPattern  = regexp.MustCompile(`https://[^\s"'<>)]+`)
)

// deviceLogin collects the address and code that `grok login --device-auth`
// prints. The exact text is not known, so it takes the first https address
// and the first XXXX-XXXX code on any line. When the text changes, it stays
// not ready, and the caller reports that instead of guessing.
type deviceLogin struct{ url, code string }

func (d *deviceLogin) feed(line string) {
	line = ansiEscape.ReplaceAllString(line, "")
	if d.url == "" {
		d.url = strings.TrimRight(urlPattern.FindString(line), ".,;")
	}
	if d.code == "" {
		d.code = codePattern.FindString(line)
	}
}

func (d *deviceLogin) ready() bool { return d.url != "" && d.code != "" }

// SignIn runs `grok login --device-auth` in a new temporary GROK_HOME and
// returns the auth.json it wrote. The live login is never touched and the
// temporary directory is always deleted. onReady runs once, when the address
// and the code are known.
func SignIn(ctx context.Context, onReady func(provider.LoginPrompt)) ([]byte, error) {
	home, err := os.MkdirTemp("", "ccswitch-grok-")
	if err != nil {
		return nil, fmt.Errorf("create temporary GROK_HOME: %w", err)
	}
	defer os.RemoveAll(home)
	if err := os.Chmod(home, 0o700); err != nil {
		return nil, fmt.Errorf("protect temporary GROK_HOME: %w", err)
	}

	cmd := exec.CommandContext(ctx, "grok", "login", "--device-auth")
	cmd.Env = append(os.Environ(), "GROK_HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start grok: %w", err)
	}

	var (
		mu     sync.Mutex
		login  deviceLogin
		called bool
		wg     sync.WaitGroup
	)
	feed := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			mu.Lock()
			login.feed(sc.Text())
			if !called && login.ready() {
				called = true
				onReady(provider.LoginPrompt{URL: login.url, Code: login.code})
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
		return nil, fmt.Errorf("grok login failed: %w", err)
	}
	blob, err := os.ReadFile(filepath.Join(home, AuthFile))
	if err != nil {
		return nil, fmt.Errorf("grok login wrote no auth.json: %w", err)
	}
	return blob, nil
}
