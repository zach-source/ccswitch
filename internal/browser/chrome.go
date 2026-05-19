// Package browser provides Chrome profile detection so ccswitch can launch
// OAuth login URLs in the Chrome profile already signed in to the target
// Anthropic account, eliminating manual "switch browser profile first"
// during multi-account login flows.
package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Profile is one entry in Chrome's profile registry.
type Profile struct {
	Directory string // the on-disk directory name ("Default", "Profile 1", …)
	Name      string // the human label Chrome shows in the avatar menu
	UserName  string // the signed-in Google account email, when present
}

// Profiles returns every Chrome profile the running OS knows about,
// derived from Chrome's "Local State" file.
func Profiles() ([]Profile, error) {
	path, err := localStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("browser: read Chrome Local State (%s): %w", path, err)
	}
	var state struct {
		Profile struct {
			InfoCache map[string]struct {
				Name     string `json:"name"`
				UserName string `json:"user_name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("browser: parse Chrome Local State: %w", err)
	}
	out := make([]Profile, 0, len(state.Profile.InfoCache))
	for dir, p := range state.Profile.InfoCache {
		out = append(out, Profile{Directory: dir, Name: p.Name, UserName: p.UserName})
	}
	return out, nil
}

// FindByEmail returns the first Chrome profile whose signed-in user_name
// matches email (case-insensitive). Returns (Profile{}, false, nil) when
// no profile matches; (Profile{}, false, err) on read/parse failure.
func FindByEmail(email string) (Profile, bool, error) {
	profiles, err := Profiles()
	if err != nil {
		return Profile{}, false, err
	}
	em := strings.ToLower(email)
	for _, p := range profiles {
		if strings.EqualFold(p.UserName, em) {
			return p, true, nil
		}
	}
	return Profile{}, false, nil
}

// InstallOpener writes shim scripts at dir/open (plus dir/xdg-open on
// Linux) that route URL arguments through Chrome with
// --profile-directory=profile. Non-URL arguments are forwarded to the
// system opener so the shim does not break anything else claude might
// invoke. Returns an error when Chrome is not installed on this OS.
func InstallOpener(dir, profile string) error {
	chrome, err := chromeBinary()
	if err != nil {
		return err
	}
	realOpen, names, err := openerNames()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	body := renderOpenerScript(chrome, profile, realOpen)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// renderOpenerScript is exported only via InstallOpener; lives as a separate
// function so tests can pin the script body without writing files.
func renderOpenerScript(chrome, profile, realOpen string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
# ccswitch Chrome-profile opener shim. URL args go to Chrome with the
# requested profile; everything else is forwarded to the system opener so
# claude's other uses (open a file, open a directory) are not disturbed.
case "$1" in
  http://*|https://*)
    exec %q --profile-directory=%q "$@"
    ;;
  *)
    exec %s "$@"
    ;;
esac
`, chrome, profile, realOpen)
}

// localStatePath returns the OS-specific path to Chrome's Local State file.
// CCSWITCH_CHROME_LOCAL_STATE overrides it — both an escape hatch for users
// with non-standard Chrome installs and the seam the conformance suite uses.
func localStatePath() (string, error) {
	if v := os.Getenv("CCSWITCH_CHROME_LOCAL_STATE"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Local State"), nil
	case "linux":
		return filepath.Join(home, ".config", "google-chrome", "Local State"), nil
	}
	return "", fmt.Errorf("browser: Chrome profile detection not supported on %s", runtime.GOOS)
}

// chromeBinary returns the OS-specific Chrome executable path. Returns
// ("", error) when Chrome is not installed.
func chromeBinary() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		p := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", errors.New("browser: Chrome is not installed at /Applications/Google Chrome.app")
	case "linux":
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium"} {
			if p, err := exec.LookPath(name); err == nil {
				return p, nil
			}
		}
		return "", errors.New("browser: no google-chrome/chromium binary on PATH")
	}
	return "", fmt.Errorf("browser: unsupported OS: %s", runtime.GOOS)
}

// openerNames returns the system opener path and the script names the shim
// should install — `open` on macOS, both `xdg-open` and `open` on Linux
// (some Node libraries call one or the other).
func openerNames() (realOpen string, names []string, err error) {
	switch runtime.GOOS {
	case "darwin":
		return "/usr/bin/open", []string{"open"}, nil
	case "linux":
		return "xdg-open", []string{"xdg-open", "open"}, nil
	}
	return "", nil, fmt.Errorf("browser: unsupported OS: %s", runtime.GOOS)
}
