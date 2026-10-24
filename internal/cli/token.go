package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const tokenPrefix = "sk-ant-oat01-"

var (
	ansiRe     = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][A-Za-z0-9]|\x1b[=>]`)
	tokenRunRe = regexp.MustCompile(`sk-ant-oat01-[A-Za-z0-9_\-]*(?:[ \t]*\n[A-Za-z0-9_\-]+)*`)
)

// parseSetupToken finds the token in the typescript of `claude setup-token`.
// It removes ANSI codes and carriage returns, then joins a token that the TUI
// wrapped over lines. A blank line ends the token. It returns the longest
// token found, or "" when there is none.
func parseSetupToken(out string) string {
	clean := strings.ReplaceAll(ansiRe.ReplaceAllString(out, ""), "\r", "")
	best := ""
	for _, run := range tokenRunRe.FindAllString(clean, -1) {
		joined := strings.Join(strings.Fields(run), "")
		// A redraw can put two tokens on adjacent lines; split them again.
		for _, part := range strings.Split(joined, tokenPrefix)[1:] {
			if tok := tokenPrefix + part; len(tok) > len(best) {
				best = tok
			}
		}
	}
	return best
}

// scriptArgs builds the script(1) arguments that run `claude setup-token`
// under a pty and record the session in typescript. BSD (macOS) and
// util-linux script take different forms.
func scriptArgs(goos, typescript string) []string {
	if goos == "linux" {
		return []string{"-q", "-c", "claude setup-token", typescript}
	}
	return []string{"-q", typescript, "claude", "setup-token"}
}

// runSetupToken runs `claude setup-token` under script(1), so the TUI gets a
// real pty and the user can see it and answer it. shimDir, when set, goes
// first on PATH so the sign-in opens in the right Chrome profile. The
// recording sits in a private temp dir that is always removed.
func runSetupToken(ctx context.Context, shimDir string) (string, error) {
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		return "", errors.New("script(1) is not on PATH; it gives claude setup-token a terminal")
	}
	tmp, err := os.MkdirTemp("", "ccswitch-token-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	typescript := filepath.Join(tmp, "typescript")

	cmd := exec.CommandContext(ctx, scriptPath, scriptArgs(runtime.GOOS, typescript)...)
	cmd.Env = os.Environ()
	if shimDir != "" {
		cmd.Env = append(cmd.Env, "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()

	data, _ := os.ReadFile(typescript)
	if tok := parseSetupToken(string(data)); tok != "" {
		return tok, nil
	}
	if runErr != nil {
		return "", fmt.Errorf("claude setup-token failed: %w", runErr)
	}
	return "", errors.New("claude setup-token printed no token")
}
