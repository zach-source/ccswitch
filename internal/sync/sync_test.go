package sync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
	"github.com/zach-source/ccswitch/internal/credentials"
	syncp "github.com/zach-source/ccswitch/internal/sync"
)

// helpers

func makeSeq(ids ...string) *account.Sequence {
	seq := &account.Sequence{
		Version:  account.SchemaVersion,
		Accounts: make(map[string]account.Account),
	}
	for _, id := range ids {
		email := id + "@example.com"
		seq.Accounts[id] = account.Account{Email: email}
		seq.Sequence = append(seq.Sequence, id)
	}
	return seq
}

func credKey(id, email string) string {
	return account.BackupCredKey(id, email)
}

func writeCred(t *testing.T, b backend.Backend, id, email string, expiresAtMillis int64) {
	t.Helper()
	cred := credentials.Credentials{
		ClaudeAIOAuth: credentials.ClaudeAIOAuth{
			AccessToken:     "tok-" + id,
			RefreshToken:    "ref-" + id,
			ExpiresAtMillis: expiresAtMillis,
		},
	}
	data, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Write(context.Background(), credKey(id, email), data); err != nil {
		t.Fatal(err)
	}
}

func readCred(t *testing.T, b backend.Backend, id, email string) *credentials.Credentials {
	t.Helper()
	data, err := b.Read(context.Background(), credKey(id, email))
	if err != nil {
		t.Fatalf("readCred %s: %v", id, err)
	}
	cred, err := credentials.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

func newEngine(local, remote backend.Backend, seq *account.Sequence) *syncp.Engine {
	return syncp.New(local, remote, seq, syncp.Options{})
}

// TestPush_LocalOnly verifies that credentials present only in local are pushed
// to remote.
func TestPush_LocalOnly(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()
	seq := makeSeq("aa11bb22")
	id, email := "aa11bb22", "aa11bb22@example.com"
	now := time.Now().Add(time.Hour).UnixMilli()
	writeCred(t, local, id, email, now)

	e := newEngine(local, remote, seq)
	res, err := e.Push(context.Background())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	// sequence + 1 cred = 2 pushes
	if res.Pushed < 2 {
		t.Errorf("want >=2 pushed, got %d", res.Pushed)
	}
	cred := readCred(t, remote, id, email)
	if cred.ClaudeAIOAuth.ExpiresAtMillis != now {
		t.Errorf("expiresAt mismatch: want %d got %d", now, cred.ClaudeAIOAuth.ExpiresAtMillis)
	}
}

// TestPull_RemoteOnly verifies that credentials present only in remote are
// pulled to local.
func TestPull_RemoteOnly(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()
	seq := makeSeq("cc33dd44")
	id, email := "cc33dd44", "cc33dd44@example.com"
	now := time.Now().Add(2 * time.Hour).UnixMilli()
	writeCred(t, remote, id, email, now)

	// Write a remote sequence so Pull can read it.
	seqData, _ := account.MarshalSequence(seq, true)
	if err := remote.Write(context.Background(), account.SequenceKey, seqData); err != nil {
		t.Fatal(err)
	}

	e := newEngine(local, remote, seq)
	res, err := e.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if res.Pulled < 2 {
		t.Errorf("want >=2 pulled, got %d", res.Pulled)
	}
	cred := readCred(t, local, id, email)
	if cred.ClaudeAIOAuth.ExpiresAtMillis != now {
		t.Errorf("expiresAt mismatch: want %d got %d", now, cred.ClaudeAIOAuth.ExpiresAtMillis)
	}
}

// TestSync_NewerWins verifies that the side with the later expiresAt wins.
func TestSync_NewerWins(t *testing.T) {
	cases := []struct {
		name          string
		localExpMs    int64
		remoteExpMs   int64
		wantLocalExp  int64
		wantRemoteExp int64
	}{
		{
			name:          "local fresher",
			localExpMs:    time.Now().Add(3 * time.Hour).UnixMilli(),
			remoteExpMs:   time.Now().Add(1 * time.Hour).UnixMilli(),
			wantLocalExp:  time.Now().Add(3 * time.Hour).UnixMilli(),
			wantRemoteExp: time.Now().Add(3 * time.Hour).UnixMilli(),
		},
		{
			name:          "remote fresher",
			localExpMs:    time.Now().Add(1 * time.Hour).UnixMilli(),
			remoteExpMs:   time.Now().Add(3 * time.Hour).UnixMilli(),
			wantLocalExp:  time.Now().Add(3 * time.Hour).UnixMilli(),
			wantRemoteExp: time.Now().Add(3 * time.Hour).UnixMilli(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local := inmem.New()
			remote := inmem.New()
			seq := makeSeq("ee55ff66")
			id, email := "ee55ff66", "ee55ff66@example.com"

			writeCred(t, local, id, email, tc.localExpMs)
			writeCred(t, remote, id, email, tc.remoteExpMs)
			// No remote sequence → sync will push local sequence first.

			e := newEngine(local, remote, seq)
			_, err := e.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			localCred := readCred(t, local, id, email)
			remoteCred := readCred(t, remote, id, email)

			// Both sides should converge on the greater expiresAt (within 1s tolerance).
			want := tc.wantLocalExp
			if abs(localCred.ClaudeAIOAuth.ExpiresAtMillis-want) > 1000 {
				t.Errorf("local expiresAt: want ~%d got %d", want, localCred.ClaudeAIOAuth.ExpiresAtMillis)
			}
			if abs(remoteCred.ClaudeAIOAuth.ExpiresAtMillis-want) > 1000 {
				t.Errorf("remote expiresAt: want ~%d got %d", want, remoteCred.ClaudeAIOAuth.ExpiresAtMillis)
			}
		})
	}
}

// TestSync_Noop verifies that equal expiresAt produces no writes (Unchanged).
func TestSync_Noop(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()
	seq := makeSeq("gg77hh88")
	id, email := "gg77hh88", "gg77hh88@example.com"
	exp := time.Now().Add(2 * time.Hour).UnixMilli()

	writeCred(t, local, id, email, exp)
	writeCred(t, remote, id, email, exp)

	// Pre-populate remote sequence so the sequence sync is a no-op too.
	seqData, _ := account.MarshalSequence(seq, true)
	seq.LastUpdated = "2024-01-01T00:00:00Z" // set before writing remote
	seqData, _ = account.MarshalSequence(seq, true)
	if err := remote.Write(context.Background(), account.SequenceKey, seqData); err != nil {
		t.Fatal(err)
	}

	e := newEngine(local, remote, seq)
	res, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Both sequence and credential are equal → everything unchanged.
	if res.Pushed != 0 {
		t.Errorf("want 0 pushed, got %d", res.Pushed)
	}
	if res.Unchanged < 2 {
		t.Errorf("want >=2 unchanged (sequence + cred), got %d", res.Unchanged)
	}
}

// TestSync_SequenceMerge verifies that the newer lastUpdated wins and the local
// switchLog is always preserved.
func TestSync_SequenceMerge(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()

	seq := makeSeq("ii99jj00")
	seq.LastUpdated = "2024-01-01T00:00:00Z"
	seq.SwitchLog = []account.SwitchLogEntry{
		{Timestamp: "2024-01-01T00:00:00Z", To: "ii99jj00"},
	}

	// Remote sequence is newer.
	remoteSeq := makeSeq("ii99jj00")
	remoteSeq.LastUpdated = "2024-06-01T00:00:00Z"
	remoteSeq.SwitchLog = nil // remote never has switchLog
	remoteData, _ := account.MarshalSequence(remoteSeq, false)
	if err := remote.Write(context.Background(), account.SequenceKey, remoteData); err != nil {
		t.Fatal(err)
	}

	e := newEngine(local, remote, seq)
	_, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// seq should now have the remote's lastUpdated but preserve local switchLog.
	if seq.LastUpdated != "2024-06-01T00:00:00Z" {
		t.Errorf("lastUpdated: want 2024-06-01T00:00:00Z got %s", seq.LastUpdated)
	}
	if len(seq.SwitchLog) == 0 {
		t.Error("switchLog should be preserved from local, got empty")
	}
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestSync_ActiveSlot_PullMirrorsBoth verifies that when a remote-only
// credential is pulled for the active account, BOTH the active slot
// (account.ActiveCredKey) and the per-account backup slot are written
// on local. Otherwise a subsequent switch-away would copy stale backup
// data over the freshly-pulled active slot, silently losing the token.
func TestSync_ActiveSlot_PullMirrorsBoth(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()

	id, email := "active11", "active11@example.com"
	seq := makeSeq(id)
	seq.ActiveAccountID = id

	exp := time.Now().Add(2 * time.Hour).UnixMilli()
	writeCred(t, remote, id, email, exp)

	e := newEngine(local, remote, seq)
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Active slot must contain the pulled cred.
	activeData, err := local.Read(context.Background(), account.ActiveCredKey)
	if err != nil {
		t.Fatalf("read active slot: %v", err)
	}
	activeCred, err := credentials.Parse(activeData)
	if err != nil {
		t.Fatalf("parse active cred: %v", err)
	}
	if activeCred.ClaudeAIOAuth.ExpiresAtMillis != exp {
		t.Errorf("active slot expiresAt: want %d got %d", exp, activeCred.ClaudeAIOAuth.ExpiresAtMillis)
	}

	// Backup slot must also contain the pulled cred (this is the recovery
	// path for the next switch-away).
	backupCred := readCred(t, local, id, email)
	if backupCred.ClaudeAIOAuth.ExpiresAtMillis != exp {
		t.Errorf("backup slot expiresAt: want %d got %d", exp, backupCred.ClaudeAIOAuth.ExpiresAtMillis)
	}
}

// TestSync_PreservesRawCredentialBytes verifies that a credential blob is
// stored byte-for-byte through a sync pass — including a top-level field the
// credentials.Credentials struct does not model. Any struct round-trip in the
// pipeline would silently drop it; this test guards against that regression.
func TestSync_PreservesRawCredentialBytes(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()
	seq := makeSeq("aa11bb22")
	email := seq.Accounts["aa11bb22"].Email

	// Raw blob with an unmodeled "futureField"; far-future expiresAt so the
	// local copy is the fresher side and gets pushed to remote.
	raw := []byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r",` +
		`"expiresAt":9999999999999},"futureField":"keep-me"}`)
	if err := local.Write(context.Background(), credKey("aa11bb22", email), raw); err != nil {
		t.Fatal(err)
	}

	if _, err := newEngine(local, remote, seq).Run(context.Background()); err != nil {
		t.Fatalf("sync run: %v", err)
	}

	got, err := remote.Read(context.Background(), credKey("aa11bb22", email))
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("sync altered the credential blob:\n  want %s\n   got %s", raw, got)
	}
}
