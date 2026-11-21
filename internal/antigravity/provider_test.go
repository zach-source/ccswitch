package antigravity

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
printf '%s' "$FAKE_LOGIN" > "$store/item"
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
	_, err := withIsolatedHome(context.Background(), []byte("X"), func(_ context.Context, home string) error {
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

// login builds a login value in the shape agy writes.
func login(email, expiry string) string {
	j := `{"token":{"access_token":"a","refresh_token":"r","expiry":"` + expiry + `"},"auth_method":"oauth","id_token_email":"` + email + `"}`
	return keyringPrefix + base64.StdEncoding.EncodeToString([]byte(j))
}

func TestDescribeAndExpiry(t *testing.T) {
	p := Provider{}
	b := []byte(login("me@example.com", "2026-11-21T07:36:30.123456789+01:00"))
	if id := p.Describe(b); id.Email != "me@example.com" || id.Mode != "oauth" {
		t.Errorf("Describe = %+v", id)
	}
	exp, ok := p.Expiry(b)
	if !ok || !exp.Equal(time.Date(2026, 11, 21, 6, 36, 30, 123456789, time.UTC)) {
		t.Errorf("Expiry = %v, %v", exp, ok)
	}
	if lr, _ := p.LastRefresh(b); !lr.Equal(exp) {
		t.Errorf("LastRefresh = %v, want the expiry", lr)
	}
	for _, bad := range []string{"", "opaque", keyringPrefix + "!!!", keyringPrefix + base64.StdEncoding.EncodeToString([]byte("x"))} {
		if id := p.Describe([]byte(bad)); id != (provider.Identity{}) {
			t.Errorf("Describe(%q) = %+v, want empty", bad, id)
		}
		if _, ok := p.Expiry([]byte(bad)); ok {
			t.Errorf("Expiry(%q) ok, want false", bad)
		}
	}
}

func TestLogin_ReadsItem(t *testing.T) {
	setup(t)
	want := login("me@example.com", "2026-11-21T07:36:30Z")
	t.Setenv("FAKE_LOGIN", want)
	var prompt provider.LoginPrompt
	blob, err := Provider{}.Login(context.Background(), func(d provider.LoginPrompt) { prompt = d })
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != want || (Provider{}).Describe(blob).Email != "me@example.com" {
		t.Fatalf("blob = %q", blob)
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

func TestMatch_ByEmail(t *testing.T) {
	reg := &provider.Registry{Profiles: map[string]provider.Profile{
		"work":     {Label: "work", Email: "Me@Example.com"},
		"personal": {Label: "personal", Email: "other@example.com"},
	}}
	p := Provider{}
	live := []byte(login("me@example.com", "2026-11-21T07:36:30Z"))
	if l, ok := p.Match(reg, live); !ok || l != "work" {
		t.Errorf("Match = %q, %v", l, ok)
	}
	if _, ok := p.Match(reg, []byte(login("nobody@example.com", "x"))); ok {
		t.Error("an unknown email matches nothing")
	}
	if _, ok := p.Match(reg, []byte("opaque")); ok {
		t.Error("an unreadable login matches nothing")
	}
	reg.Profiles["dup"] = provider.Profile{Label: "dup", Email: "me@example.com"}
	if _, ok := p.Match(reg, live); ok {
		t.Error("two profiles with one email match nothing")
	}
}

func TestRefresh_ReturnsRenewedLoginOrNotSupported(t *testing.T) {
	setup(t)
	t.Setenv("FAKE_WANT", "STORED")
	t.Setenv("FAKE_NEW", "RENEWED")
	got, err := Provider{}.Refresh(context.Background(), []byte("STORED"))
	if err != nil || string(got) != "RENEWED" {
		t.Fatalf("Refresh = %q, %v", got, err)
	}
	t.Setenv("FAKE_NEW", "")
	if _, err := (Provider{}).Refresh(context.Background(), []byte("STORED")); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("an unchanged login: err = %v, want ErrNotSupported", err)
	}
}

// A SIGTERM during `agy -p /usage` must kill agy and remove the isolated home,
// which holds a keychain with a refresh token.
func TestFetchUsage_SIGTERMKillsAgyAndRemovesHome(t *testing.T) {
	setup(t)
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("FAKE_STARTED", started)
	slow := "#!/bin/sh\necho \"$HOME\" > \"$FAKE_STARTED\"\nexec sleep 10\n"
	if err := os.WriteFile(agyBin, []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep the test process alive when the signal comes without a handler.
	absorb := make(chan os.Signal, 1)
	signal.Notify(absorb, syscall.SIGTERM)
	defer signal.Stop(absorb)

	done := make(chan error, 1)
	go func() {
		_, _, err := Provider{}.FetchUsage(context.Background(), []byte("STORED"))
		done <- err
	}()
	var home string
	for range 200 {
		if b, err := os.ReadFile(started); err == nil && len(b) > 0 {
			home = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if home == "" {
		t.Fatal("agy did not start")
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("FetchUsage kept running after SIGTERM")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("isolated home %s still exists", home)
	}
}
