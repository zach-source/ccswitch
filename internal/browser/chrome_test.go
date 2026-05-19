package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleLocalState = `{
  "profile": {
    "info_cache": {
      "Default":   {"name": "Personal", "user_name": "alice@example.com"},
      "Profile 1": {"name": "Work",     "user_name": "BOB@example.com"},
      "Profile 5": {"name": "Side",     "user_name": "carol@stigen.ai"}
    }
  },
  "other": {"unrelated": true}
}`

// writeSample puts a synthetic Local State file in a temp dir and points
// CCSWITCH_CHROME_LOCAL_STATE at it for the duration of the test.
func writeSample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Local State")
	if err := os.WriteFile(path, []byte(sampleLocalState), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCSWITCH_CHROME_LOCAL_STATE", path)
	return path
}

func TestProfiles_ParsesLocalState(t *testing.T) {
	writeSample(t)
	got, err := Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 profiles, got %d", len(got))
	}
	dirs := map[string]Profile{}
	for _, p := range got {
		dirs[p.Directory] = p
	}
	if dirs["Default"].UserName != "alice@example.com" {
		t.Errorf("Default user_name = %q, want alice@example.com", dirs["Default"].UserName)
	}
	if dirs["Profile 5"].Name != "Side" {
		t.Errorf("Profile 5 name = %q, want Side", dirs["Profile 5"].Name)
	}
}

func TestFindByEmail_CaseInsensitiveMatch(t *testing.T) {
	writeSample(t)
	p, ok, err := FindByEmail("ALICE@EXAMPLE.COM")
	if err != nil || !ok {
		t.Fatalf("expected match for alice (case-insensitive): ok=%v err=%v", ok, err)
	}
	if p.Directory != "Default" {
		t.Errorf("directory = %q, want Default", p.Directory)
	}
	// The sample stores Bob with mixed case; the search must still match a
	// lowercase query.
	p, ok, _ = FindByEmail("bob@example.com")
	if !ok || p.Directory != "Profile 1" {
		t.Errorf("Bob lookup mismatch: ok=%v dir=%q", ok, p.Directory)
	}
}

func TestFindByEmail_NoMatch(t *testing.T) {
	writeSample(t)
	if _, ok, err := FindByEmail("nobody@example.com"); ok || err != nil {
		t.Errorf("want (false, nil) for unknown email, got ok=%v err=%v", ok, err)
	}
}

func TestProfiles_MissingFile(t *testing.T) {
	t.Setenv("CCSWITCH_CHROME_LOCAL_STATE", filepath.Join(t.TempDir(), "absent"))
	if _, err := Profiles(); err == nil {
		t.Fatal("expected an error when Local State is missing")
	}
}

func TestRenderOpenerScript_RoutesURLsToChrome(t *testing.T) {
	script := renderOpenerScript("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"Profile 1", "/usr/bin/open")
	// Chrome path + profile flag must appear in the URL branch.
	if !strings.Contains(script, "--profile-directory=\"Profile 1\"") {
		t.Errorf("script missing --profile-directory flag:\n%s", script)
	}
	if !strings.Contains(script, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome") {
		t.Errorf("script missing Chrome binary path:\n%s", script)
	}
	// Non-URL args must fall through to the system opener.
	if !strings.Contains(script, "exec /usr/bin/open \"$@\"") {
		t.Errorf("script missing system-opener fallback:\n%s", script)
	}
	// Both URL prefixes must appear in the case alternation, in either
	// order. The shell pattern looks like "http://*|https://*)".
	if !strings.Contains(script, "http://*") || !strings.Contains(script, "https://*") {
		t.Errorf("script missing one of the URL case patterns:\n%s", script)
	}
}
