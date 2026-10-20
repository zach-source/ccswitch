package codex

import (
	"regexp"
	"strings"
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
