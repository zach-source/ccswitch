package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/codex"
)

// addCodexProfile captures a login as a profile, leaving it live.
func addCodexProfile(t *testing.T, label, token, email string) {
	t.Helper()
	if err := codex.WriteLive([]byte(codexBlob(token, email))); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "codex", "add", label); err != nil {
		t.Fatalf("codex add %s: %v", label, err)
	}
}

// A running Codex can write its own account over the live file after a swap,
// so the live login may belong to a profile other than the active one.
func TestCodexUse_SnapshotsIntoTheProfileTheLiveLoginBelongsTo(t *testing.T) {
	for _, target := range []string{"c", "a"} {
		t.Run("use "+target, func(t *testing.T) {
			home := newTestHome(t)
			newCodexHome(t, "")
			addCodexProfile(t, "a", "a-rt", "a@example.com")
			addCodexProfile(t, "b", "b-rt", "b@example.com")
			addCodexProfile(t, "c", "c-rt", "c@example.com")

			// "a" is active, but a stale session wrote account B's rotated login.
			reg := readCodexRegistry(t)
			reg.Active = "a"
			if err := reg.Save(codexRegistryPath()); err != nil {
				t.Fatal(err)
			}
			liveB := codexBlob("b-rt-rotated", "b@example.com")
			if err := codex.WriteLive([]byte(liveB)); err != nil {
				t.Fatal(err)
			}
			aBefore := readCred(t, home, codex.CredKey("a"))

			if err := run(t, "codex", "use", target); err != nil {
				t.Fatalf("codex use %s: %v", target, err)
			}

			if got := readCred(t, home, codex.CredKey("b")); string(got) != liveB {
				t.Errorf("profile b did not receive the live bytes: %s", got)
			}
			if got := readCred(t, home, codex.CredKey("a")); !bytes.Equal(got, aBefore) {
				t.Errorf("profile a changed: %s", got)
			}
		})
	}
}

func TestCodexUse_UnmatchedLiveLoginStopsAndChangesNothing(t *testing.T) {
	home := newTestHome(t)
	newCodexHome(t, "")
	addCodexProfile(t, "a", "a-rt", "a@example.com")
	addCodexProfile(t, "b", "b-rt", "b@example.com")
	aBefore := readCred(t, home, codex.CredKey("a"))
	bBefore := readCred(t, home, codex.CredKey("b"))

	stranger := codexBlob("x-rt", "stranger@example.com")
	if err := codex.WriteLive([]byte(stranger)); err != nil {
		t.Fatal(err)
	}

	err := run(t, "codex", "use", "a")
	if err == nil {
		t.Fatal("an unmatched live login must stop the switch")
	}
	if !strings.Contains(err.Error(), "ccswitch codex add") {
		t.Errorf("error should point at `ccswitch codex add <label>`: %v", err)
	}
	live, _ := codex.ReadLive()
	if string(live) != stranger {
		t.Errorf("live file changed: %s", live)
	}
	if got := readCred(t, home, codex.CredKey("a")); !bytes.Equal(got, aBefore) {
		t.Errorf("profile a changed")
	}
	if got := readCred(t, home, codex.CredKey("b")); !bytes.Equal(got, bBefore) {
		t.Errorf("profile b changed")
	}
}

// API keys do not rotate, so there is nothing to save.
func TestCodexUse_APIKeyLiveLoginNeedsNoSnapshot(t *testing.T) {
	for name, live := range map[string]string{
		"auth_mode":      `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-fake"}`,
		"key only":       `{"OPENAI_API_KEY":"sk-fake"}`,
		"mode, no token": `{"auth_mode":"apikey"}`,
	} {
		t.Run(name, func(t *testing.T) {
			newTestHome(t)
			newCodexHome(t, "")
			addCodexProfile(t, "a", "a-rt", "a@example.com")
			if err := codex.WriteLive([]byte(live)); err != nil {
				t.Fatal(err)
			}
			if err := run(t, "codex", "use", "a"); err != nil {
				t.Fatalf("codex use: %v", err)
			}
			got, _ := codex.ReadLive()
			if !strings.Contains(string(got), "a-rt") {
				t.Errorf("live login was not swapped: %s", got)
			}
		})
	}
}

func TestCodexUse_NoLiveLoginProceeds(t *testing.T) {
	newTestHome(t)
	newCodexHome(t, "")
	addCodexProfile(t, "a", "a-rt", "a@example.com")
	if err := os.Remove(codex.AuthPath()); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "codex", "use", "a"); err != nil {
		t.Fatalf("codex use: %v", err)
	}
	if _, err := codex.ReadLive(); err != nil {
		t.Errorf("live login was not restored: %v", err)
	}
}
