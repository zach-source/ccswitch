package cli

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/antigravity"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
	"github.com/zach-source/ccswitch/internal/provider"
)

// fakeAgySecurity keeps the agy item in $HOME/.fakekc/item, like the real tool
// picks its keychain from $HOME.
const fakeAgySecurity = `#!/bin/sh
store="$HOME/.fakekc"
mkdir -p "$store"
case "$1" in
create-keychain) mkdir -p "$(dirname "$4")"; : > "$4" ;;
delete-keychain|default-keychain|list-keychains|unlock-keychain|set-keychain-settings) ;;
find-generic-password)
	[ -f "$store/item" ] || exit 44
	cat "$store/item"; echo ;;
-i)
	read -r line
	printf '%s' "$line" | sed -n 's/.* -X \([0-9a-fA-F]*\).*/\1/p' | tr -d '\n' | xxd -r -p > "$store/item" ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`

// fakeAgyRenews replaces the item with $FAKE_NEW, as agy does when it renews
// an expired token, then prints usage rows.
const fakeAgyRenews = `#!/bin/sh
store="$HOME/.fakekc"
[ "$1" = "-p" ] || exit 2
printf '%s' "$FAKE_NEW" > "$store/item"
printf 'Gemini Models\tWeekly Limit Remaining\t75%%\t2026-11-28T02:36:30Z\n'
`

func agyLogin(email string, expiry time.Time) string {
	j := `{"token":{"expiry":"` + expiry.UTC().Format(time.RFC3339) + `"},"auth_method":"oauth","id_token_email":"` + email + `"}`
	return "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(j))
}

// An expired agy login has a row with usage after the generic code renews it
// through an isolated agy run.
func TestCollectProviderUsage_RenewsExpiredAgyProfile(t *testing.T) {
	newTestHome(t)
	dir := t.TempDir()
	for name, body := range map[string]string{"security": fakeAgySecurity, "agy": fakeAgyRenews} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(antigravity.SetToolPaths(filepath.Join(dir, "security"), filepath.Join(dir, "agy")))
	now := time.Now()
	fresh := agyLogin("a@example.com", now.Add(time.Hour))
	t.Setenv("FAKE_NEW", fresh)

	p := antigravity.Provider{RegistryFile: filepath.Join(dir, "agy.json")}
	reg := &provider.Registry{Version: provider.SchemaVersion, Profiles: map[string]provider.Profile{
		"work": {Label: "work", Email: "a@example.com"},
	}}
	store := inmem.New()
	ctx := context.Background()
	if err := store.Write(ctx, p.CredKeyPrefix()+"work", []byte(agyLogin("a@example.com", now.Add(-time.Hour)))); err != nil {
		t.Fatal(err)
	}

	rows := collectProviderUsage(ctx, p, reg, store, nil, nil, now, true)
	if len(rows) != 1 || rows[0].Status != usageOK || len(rows[0].Windows) == 0 {
		t.Fatalf("rows = %+v, want real usage after the renewal", rows)
	}
	if got, _ := store.Read(ctx, p.CredKeyPrefix()+"work"); string(got) != fresh {
		t.Errorf("the renewed login was not stored: %q", got)
	}
}
