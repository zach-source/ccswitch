package cli

import (
	"regexp"
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
