package antigravity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/backend/keychain"
	"github.com/zach-source/ccswitch/internal/provider"
)

// fakeSecurity keeps the agy item in $HOME/.fakekc/item, like the real tool
// picks its keychain from $HOME. It logs every delete-keychain call.
const fakeSecurity = `#!/bin/sh
store="$HOME/.fakekc"
mkdir -p "$store"
case "$1" in
create-keychain) mkdir -p "$(dirname "$4")"; : > "$4" ;;
delete-keychain) echo "delete-keychain $HOME" >> "$FAKE_LOG" ;;
default-keychain|list-keychains|unlock-keychain|set-keychain-settings) ;;
find-generic-password)
	[ -f "$store/item" ] || exit 44
	cat "$store/item"; echo ;;
-i)
	read -r line
	printf '%s' "$line" | sed -n 's/.* -X \([0-9a-fA-F]*\).*/\1/p' | tr -d '\n' | xxd -r -p > "$store/item" ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`

// fakeAgy prints usage rows only when the isolated keychain holds $FAKE_WANT.
// $FAKE_NEW makes it replace the item, like a token refresh. Without
// arguments it acts as a sign-in and writes the item and a log line.
const fakeAgy = `#!/bin/sh
store="$HOME/.fakekc"
mkdir -p "$store"
if [ "$1" = "-p" ]; then
	[ "$(cat "$store/item")" = "$FAKE_WANT" ] || exit 3
	printf 'Gemini Models\tWeekly Limit Remaining\t75%%\t2026-11-28T02:36:30Z\n'
	printf 'Claude and GPT models\tFive Hour Limit Remaining\t100%%\t2026-11-21T07:36:30Z\n'
	echo "some noise line"
	[ -z "$FAKE_NEW" ] || printf '%s' "$FAKE_NEW" > "$store/item"
	exit 0
fi
printf '%s' "go-keyring-base64:SIGNEDIN" > "$store/item"
mkdir -p "$HOME/.gemini/antigravity-cli/log"
echo "x applyAuthResult: email=me@example.com, authMethod=oauth" > "$HOME/.gemini/antigravity-cli/log/a.log"
`

// setup points the tool paths at fake scripts and gives the test its own HOME.
func setup(t *testing.T) (home, logFile string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"security": fakeSecurity, "agy": fakeAgy} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldSec, oldAgy := securityBin, agyBin
	securityBin, agyBin = filepath.Join(dir, "security"), filepath.Join(dir, "agy")
	t.Cleanup(func() { securityBin, agyBin = oldSec, oldAgy })
	home = filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile = filepath.Join(dir, "log")
	t.Setenv("HOME", home)
	t.Setenv("FAKE_LOG", logFile)
	return home, logFile
}

func TestParseUsage(t *testing.T) {
	out := "Gemini Models\tWeekly Limit Remaining\t75%\t2026-11-28T02:36:30Z\n" +
		"Claude and GPT models\tFive Hour Limit Remaining\t100%\t2026-11-21T07:36:30Z\n" +
		"noise\n" +
		"Gemini Models\tWeekly Limit Remaining\tabc%\t2026-11-28T02:36:30Z\n" +
		"Gemini Models\tWeekly Limit Remaining\t10%\tnot-a-time\n"
	u := ParseUsage(out)
	if len(u.Windows) != 2 {
		t.Fatalf("windows = %d, want 2: %+v", len(u.Windows), u.Windows)
	}
	w := u.Windows[0]
	if w.Name() != "Gemini weekly" || w.UsedPercent != 25 || w.Seconds != 604800 ||
		!w.ResetAt.Equal(time.Date(2026, 11, 28, 2, 36, 30, 0, time.UTC)) {
		t.Errorf("window 0 = %+v", w)
	}
	if w := u.Windows[1]; w.Name() != "Claude/GPT 5h" || w.UsedPercent != 0 || w.Seconds != 18000 {
		t.Errorf("window 1 = %+v", w)
	}
}

func TestReadWriteLive(t *testing.T) {
	setup(t)
	p := Provider{}
	if _, err := p.ReadLive(); !errors.Is(err, provider.ErrNoLiveLogin) {
		t.Fatalf("empty keychain: err = %v, want ErrNoLiveLogin", err)
	}
	if err := p.WriteLive([]byte("go-keyring-base64:ABC")); err != nil {
		t.Fatal(err)
	}
	got, err := p.ReadLive()
	if err != nil || string(got) != "go-keyring-base64:ABC" {
		t.Fatalf("ReadLive = %q, %v", got, err)
	}
}

func TestWriteLive_RefusesLargeValue(t *testing.T) {
	setup(t)
	err := Provider{}.WriteLive(make([]byte, 2100))
	if !errors.Is(err, keychain.ErrValueTooLarge) {
		t.Fatalf("err = %v, want ErrValueTooLarge", err)
	}
	if _, rerr := (Provider{}).ReadLive(); !errors.Is(rerr, provider.ErrNoLiveLogin) {
		t.Fatalf("a refused write must store nothing, got %v", rerr)
	}
}

func TestFetchUsage_IsolatedAndCleanedUp(t *testing.T) {
	home, logFile := setup(t)
	t.Setenv("FAKE_WANT", "STORED")
	p := Provider{}
	if err := p.WriteLive([]byte("LIVE")); err != nil {
		t.Fatal(err)
	}
	u, changed, err := p.FetchUsage(context.Background(), []byte("STORED"))
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Windows) != 2 || changed != nil {
		t.Fatalf("usage = %+v, changed = %q", u, changed)
	}
	if live, _ := p.ReadLive(); string(live) != "LIVE" {
		t.Fatalf("the live item changed to %q", live)
	}
	log, _ := os.ReadFile(logFile)
	if n := strings.Count(string(log), "delete-keychain"); n != 1 || strings.Contains(string(log), home) {
		t.Fatalf("log = %q, want one delete of an isolated keychain", log)
	}
}

func TestFetchUsage_ReturnsRefreshedLogin(t *testing.T) {
	setup(t)
	t.Setenv("FAKE_WANT", "STORED")
	t.Setenv("FAKE_NEW", "REFRESHED")
	_, changed, err := Provider{}.FetchUsage(context.Background(), []byte("STORED"))
	if err != nil || string(changed) != "REFRESHED" {
		t.Fatalf("changed = %q, err = %v", changed, err)
	}
}

func TestFetchUsage_NoRowsIsError(t *testing.T) {
	setup(t)
	t.Setenv("FAKE_WANT", "OTHER") // agy exits 3: the copy was not what it wanted
	if _, _, err := (Provider{}).FetchUsage(context.Background(), []byte("STORED")); err == nil {
		t.Fatal("want an error when agy prints no rows")
	}
}

func TestWithIsolatedHome_CleansUpOnError(t *testing.T) {
	_, logFile := setup(t)
	var seen string
	_, err := withIsolatedHome(context.Background(), []byte("X"), func(home string) error {
		seen = home
		return errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want the fn error", err)
	}
	if _, serr := os.Stat(seen); !os.IsNotExist(serr) {
		t.Fatalf("home %s still exists", seen)
	}
	if log, _ := os.ReadFile(logFile); !strings.Contains(string(log), "delete-keychain "+seen) {
		t.Fatalf("keychain of %s not deleted: %q", seen, log)
	}
}

func TestLogin_ReadsItemAndEmail(t *testing.T) {
	setup(t)
	var prompt provider.LoginPrompt
	blob, id, err := Provider{}.LoginIdentity(context.Background(), func(d provider.LoginPrompt) { prompt = d })
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != "go-keyring-base64:SIGNEDIN" || id.Email != "me@example.com" {
		t.Fatalf("blob = %q, id = %+v", blob, id)
	}
	if prompt.Message == "" {
		t.Error("onReady got no message")
	}
	if _, rerr := (Provider{}).ReadLive(); !errors.Is(rerr, provider.ErrNoLiveLogin) {
		t.Fatalf("the live keychain must stay untouched, got %v", rerr)
	}
}

func TestLogin_NoItemIsError(t *testing.T) {
	setup(t)
	if err := os.WriteFile(agyBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Provider{}).Login(context.Background(), func(provider.LoginPrompt) {}); err == nil {
		t.Fatal("want an error when agy saved no login")
	}
}

func TestMatch_UsesActiveProfile(t *testing.T) {
	reg := &provider.Registry{Active: "work", Profiles: map[string]provider.Profile{"work": {Label: "work"}}}
	p := Provider{}
	if l, ok := p.Match(reg, []byte("any bytes")); !ok || l != "work" {
		t.Errorf("Match = %q, %v", l, ok)
	}
	if _, ok := p.Match(reg, nil); ok {
		t.Error("an empty live login matches nothing")
	}
	reg.Active = "gone"
	if _, ok := p.Match(reg, []byte("x")); ok {
		t.Error("an active label with no profile matches nothing")
	}
}
