package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/codex"
)

// autoswitchSetup seeds profiles a (live) and b, with a at loadA percent and
// b at 10 percent. It returns the blobs by label.
func autoswitchSetup(t *testing.T, loadA string) map[string]string {
	t.Helper()
	home := newTestHome(t)
	future := time.Now().Add(time.Hour)
	blobs := map[string]string{
		"a": usageBlob("a@example.com", future, "2026-10-20T10:00:00Z"),
		"b": usageBlob("b@example.com", future, "2026-10-20T11:00:00Z"),
	}
	seedUsageProfiles(t, home, blobs, []string{"a", "b"})
	newCodexHome(t, blobs["a"])
	usageServer(t, map[string]func(http.ResponseWriter){
		"acct-a@example.com": usageJSON(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":` + loadA + `,"limit_window_seconds":18000,"reset_at":1792000000}}}`),
		"acct-b@example.com": usageJSON(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000,"reset_at":1792000000}}}`),
	})
	return blobs
}

func liveCodex(t *testing.T) string {
	t.Helper()
	b, err := codex.ReadLive()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCodexAutoswitch_SwitchesWhenLiveOverThreshold(t *testing.T) {
	blobs := autoswitchSetup(t, "90")
	out, err := runOut(t, "codex", "autoswitch", "--once")
	if err != nil {
		t.Fatal(err)
	}
	if want := "autoswitch: a 90% -> b 10%\n"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if liveCodex(t) != blobs["b"] {
		t.Error("live login is not b's credential")
	}
	reg := readCodexRegistry(t)
	if reg.Active != "b" {
		t.Errorf("active = %q, want b", reg.Active)
	}
	if time.Since(reg.LastSwitch) > time.Minute {
		t.Errorf("lastSwitch = %v, want now", reg.LastSwitch)
	}
}

func TestCodexAutoswitch_CooldownActiveChangesNothing(t *testing.T) {
	blobs := autoswitchSetup(t, "90")
	reg := readCodexRegistry(t)
	reg.LastSwitch = time.Now().Add(-time.Minute)
	if err := reg.Save(codexRegistryPath()); err != nil {
		t.Fatal(err)
	}
	out, err := runOut(t, "codex", "autoswitch", "--once")
	if err != nil {
		t.Fatal(err)
	}
	if want := "no action: cooldown active\n"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if liveCodex(t) != blobs["a"] {
		t.Error("live login changed")
	}
}

func TestCodexAutoswitch_LiveUnderThresholdAndFirstChangesNothing(t *testing.T) {
	blobs := autoswitchSetup(t, "20")
	out, err := runOut(t, "codex", "autoswitch", "--once")
	if err != nil {
		t.Fatal(err)
	}
	if want := "no action: live profile is the best choice\n"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if liveCodex(t) != blobs["a"] {
		t.Error("live login changed")
	}
	if !readCodexRegistry(t).LastSwitch.IsZero() {
		t.Error("lastSwitch set without a switch")
	}
}

func TestCodexAutoswitch_LockHeldExitsZeroAndChangesNothing(t *testing.T) {
	blobs := autoswitchSetup(t, "90")
	if err := os.MkdirAll(backupDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(backupDir(), "codex-autoswitch.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	out, err := runOut(t, "codex", "autoswitch", "--once")
	if err != nil {
		t.Fatal(err)
	}
	if want := "no action: another codex autoswitch is running\n"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if liveCodex(t) != blobs["a"] {
		t.Error("live login changed")
	}
	if strings.Contains(out, ".sig") {
		t.Error("output leaks token text")
	}
}
