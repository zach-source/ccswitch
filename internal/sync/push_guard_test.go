package sync_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
	ccsync "github.com/zach-source/ccswitch/internal/sync"
)

// blob builds a credential whose access token and expiry are both distinctive.
func blob(token string, expiresAt int64) []byte {
	return []byte(`{"claudeAiOauth":{"accessToken":"` + token +
		`","refreshToken":"r-` + token + `","expiresAt":` + strconv.FormatInt(expiresAt, 10) + `}}`)
}

func pushFixture(t *testing.T, localExp, remoteExp int64) (*ccsync.Engine, *inmem.Backend, string) {
	t.Helper()
	email := "alice@example.com"
	id := account.HashEmail(email)
	key := account.BackupCredKey(id, email)

	local, remote := inmem.New(), inmem.New()
	ctx := context.Background()
	if err := local.Write(ctx, key, blob("local", localExp)); err != nil {
		t.Fatal(err)
	}
	if remoteExp > 0 {
		if err := remote.Write(ctx, key, blob("remote", remoteExp)); err != nil {
			t.Fatal(err)
		}
	}

	seq := &account.Sequence{
		Sequence: []string{id},
		Accounts: map[string]account.Account{id: {Email: email}},
	}
	return ccsync.New(local, remote, seq, ccsync.Options{}), remote, key
}

// Pushing a stale local credential over a fresher remote one replays a
// superseded refresh token, which the server reuse-detects and revokes.
func TestPushSkipsWhenRemoteIsFresher(t *testing.T) {
	e, remote, key := pushFixture(t, 100, 500)

	res, err := e.Push(context.Background(), false)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.Unchanged != 1 {
		t.Errorf("Unchanged = %d, want 1 (the stale account should be skipped)", res.Unchanged)
	}

	got, _ := remote.Read(context.Background(), key)
	if !strings.Contains(string(got), `"remote"`) {
		t.Errorf("push clobbered the fresher remote credential: %s", got)
	}
}

func TestPushProceedsWhenLocalIsFresher(t *testing.T) {
	e, remote, key := pushFixture(t, 900, 500)

	if _, err := e.Push(context.Background(), false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, _ := remote.Read(context.Background(), key)
	if !strings.Contains(string(got), `"local"`) {
		t.Errorf("fresher local credential was not pushed: %s", got)
	}
}

func TestPushForceOverwritesFresherRemote(t *testing.T) {
	e, remote, key := pushFixture(t, 100, 500)

	if _, err := e.Push(context.Background(), true); err != nil {
		t.Fatalf("Push --force: %v", err)
	}
	got, _ := remote.Read(context.Background(), key)
	if !strings.Contains(string(got), `"local"`) {
		t.Errorf("--force did not overwrite the remote: %s", got)
	}
}

// A remote with nothing stored is the first-push case and must not be blocked.
func TestPushProceedsWhenRemoteIsEmpty(t *testing.T) {
	e, remote, key := pushFixture(t, 100, 0)

	if _, err := e.Push(context.Background(), false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, _ := remote.Read(context.Background(), key)
	if !strings.Contains(string(got), `"local"`) {
		t.Errorf("first push to an empty remote was blocked: %s", got)
	}
}
