package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend/file"
	"github.com/zach-source/ccswitch/internal/config"
)

// ─── hermetic test harness ──────────────────────────────────────────────────
//
// These tests exercise whole commands end-to-end against a temp $HOME with the
// file backend forced for both the remote and the local (active-slot) side, so
// nothing touches the real keychain, 1Password, or network. t.Setenv marks
// each test non-parallel, which the os.Stdin/os.Stdout swaps below also need.

// newTestHome points $HOME at a fresh temp dir and forces the file backend.
func newTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CCSWITCH_BACKEND", "file")
	t.Setenv("CCSWITCH_LOCAL_BACKEND", "file")
	t.Setenv("CCSWITCH_CONFIG_FILE", filepath.Join(home, ".config", "ccswitch", "config.toml"))
	return home
}

// credBackend returns the file backend rooted where resolveBackend(file) and
// the switch/sync local side both look.
func credBackend(home string) *file.Backend {
	return file.New(filepath.Join(home, ".claude-switch-backup", "credentials"))
}

func seedCred(t *testing.T, home, key string, data []byte) {
	t.Helper()
	if err := credBackend(home).Write(context.Background(), key, data); err != nil {
		t.Fatalf("seed cred %q: %v", key, err)
	}
}

// readCred returns the raw blob at key, or nil on any miss.
func readCred(t *testing.T, home, key string) []byte {
	t.Helper()
	data, err := credBackend(home).Read(context.Background(), key)
	if err != nil {
		return nil
	}
	return data
}

// seedClaudeJSON writes ~/.claude.json naming email as the live account.
func seedClaudeJSON(t *testing.T, home, email string) {
	t.Helper()
	body := `{"oauthAccount":{"emailAddress":"` + email +
		`","organizationName":"` + email + `'s Organization"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seqWith builds and persists a sequence.json from id→email pairs (added in
// order). The first account becomes ActiveAccountID via Sequence.Add.
func seqWith(t *testing.T, accounts ...string) *account.Sequence {
	t.Helper()
	if len(accounts)%2 != 0 {
		t.Fatal("seqWith: accounts must be id,email pairs")
	}
	seq := &account.Sequence{Version: account.SchemaVersion, Accounts: map[string]account.Account{}}
	for i := 0; i < len(accounts); i += 2 {
		seq.Add(accounts[i], account.Account{Email: accounts[i+1]})
	}
	if err := seq.Save(sequencePath()); err != nil {
		t.Fatal(err)
	}
	return seq
}

func loadSeq(t *testing.T) *account.Sequence {
	t.Helper()
	s, err := account.LoadSequence(sequencePath())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// run executes a command exactly as the real entrypoint would, including the
// legacy --flag rewrite.
func run(t *testing.T, args ...string) error {
	t.Helper()
	root := Root()
	root.SetArgs(normalizeLegacyArgs(args, root))
	return root.Execute()
}

// runWithStdin runs a command with stdinText fed to os.Stdin — needed for the
// interactive y/N confirmations, which read os.Stdin directly.
func runWithStdin(t *testing.T, stdinText string, args ...string) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()
	go func() {
		_, _ = io.WriteString(w, stdinText)
		_ = w.Close()
	}()
	return run(t, args...)
}

// capture runs fn with os.Stdout redirected and returns everything printed.
func capture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// ─── add-account ────────────────────────────────────────────────────────────

func TestAddAccount(t *testing.T) {
	home := newTestHome(t)
	seedClaudeJSON(t, home, "alice@example.com")

	if err := run(t, "add-account"); err != nil {
		t.Fatalf("add-account: %v", err)
	}
	id := account.HashEmail("alice@example.com")
	seq := loadSeq(t)
	if _, ok := seq.Accounts[id]; !ok {
		t.Fatalf("account %s not added: %+v", id, seq.Accounts)
	}
	if seq.Accounts[id].Email != "alice@example.com" {
		t.Errorf("wrong email: %q", seq.Accounts[id].Email)
	}

	// Idempotent: a second add does not duplicate.
	if err := run(t, "add-account"); err != nil {
		t.Fatalf("second add-account: %v", err)
	}
	if n := len(loadSeq(t).Sequence); n != 1 {
		t.Fatalf("want 1 account after re-add, got %d", n)
	}
}

func TestAddAccount_NoLiveAccount(t *testing.T) {
	newTestHome(t) // no .claude.json
	if err := run(t, "add-account"); err == nil {
		t.Fatal("expected an error when no Claude account is logged in")
	}
}

// ─── switch-to ──────────────────────────────────────────────────────────────

func TestSwitchTo(t *testing.T) {
	home := newTestHome(t)
	alice, bob := "alice@example.com", "bob@example.com"
	aliceID, bobID := account.HashEmail(alice), account.HashEmail(bob)

	seedClaudeJSON(t, home, alice) // alice is the live account
	seqWith(t, aliceID, alice, bobID, bob)

	// Distinctive blobs; bob's carries an unmodeled field to prove the swap
	// is byte-faithful (no struct round-trip can drop it).
	aliceActive := []byte(`{"claudeAiOauth":{"accessToken":"alice-live","refreshToken":"ra","expiresAt":111}}`)
	bobBackup := []byte(`{"claudeAiOauth":{"accessToken":"bob","refreshToken":"rb","expiresAt":222},"futureField":"keep-me"}`)
	seedCred(t, home, account.ActiveCredKey, aliceActive)
	seedCred(t, home, account.BackupCredKey(bobID, bob), bobBackup)

	if err := run(t, "switch-to", bobID); err != nil {
		t.Fatalf("switch-to %s: %v", bobID, err)
	}

	// The active slot now holds bob's blob, byte-for-byte.
	if got := readCred(t, home, account.ActiveCredKey); !bytes.Equal(got, bobBackup) {
		t.Fatalf("active slot not bob's creds:\n want %s\n  got %s", bobBackup, got)
	}
	// alice's prior live creds were snapshotted into her backup slot.
	if got := readCred(t, home, account.BackupCredKey(aliceID, alice)); !bytes.Equal(got, aliceActive) {
		t.Fatalf("alice backup not snapshotted:\n want %s\n  got %s", aliceActive, got)
	}
	// sequence.json records the new active account.
	if got := loadSeq(t).ActiveAccountID; got != bobID {
		t.Fatalf("sequence ActiveAccountID = %q, want %q", got, bobID)
	}
}

func TestSwitchTo_NoStoredCredentials(t *testing.T) {
	bob := "bob@example.com"
	bobID := account.HashEmail(bob)
	newTestHome(t)
	seqWith(t, bobID, bob) // no backup creds seeded

	err := run(t, "switch-to", bobID)
	if err == nil {
		t.Fatal("expected an error switching to an account with no stored creds")
	}
	if !strings.Contains(err.Error(), "no stored credentials") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSwitchTo_UnknownIdentifier(t *testing.T) {
	newTestHome(t)
	seqWith(t, account.HashEmail("a@x.com"), "a@x.com")

	err := run(t, "switch-to", "99")
	if err == nil || !strings.Contains(err.Error(), "no account found matching") {
		t.Fatalf("want 'no account found matching' error, got %v", err)
	}
}

// ─── remove-account ─────────────────────────────────────────────────────────

func TestRemoveAccount(t *testing.T) {
	home := newTestHome(t)
	bob := "bob@example.com"
	bobID := account.HashEmail(bob)
	seqWith(t, account.HashEmail("alice@example.com"), "alice@example.com", bobID, bob)

	// An isolated env dir with a cached credential file must be purged too.
	envDir := filepath.Join(home, ".claude-env-"+bobID)
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, ".credentials.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runWithStdin(t, "y\n", "remove-account", bobID); err != nil {
		t.Fatalf("remove-account: %v", err)
	}
	if _, ok := loadSeq(t).Accounts[bobID]; ok {
		t.Error("bob still present in sequence after removal")
	}
	if _, err := os.Stat(envDir); !os.IsNotExist(err) {
		t.Errorf("env dir %s not removed", envDir)
	}
}

func TestRemoveAccount_Cancelled(t *testing.T) {
	bob := "bob@example.com"
	bobID := account.HashEmail(bob)
	newTestHome(t)
	seqWith(t, bobID, bob)

	if err := runWithStdin(t, "n\n", "remove-account", bobID); err != nil {
		t.Fatalf("remove-account (cancel): %v", err)
	}
	if _, ok := loadSeq(t).Accounts[bobID]; !ok {
		t.Error("bob removed despite the confirmation being declined")
	}
}

// ─── save ───────────────────────────────────────────────────────────────────

func TestSave(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)

	cred := []byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":999}}`)
	seedCred(t, home, account.ActiveCredKey, cred)

	if err := run(t, "save"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := readCred(t, home, account.BackupCredKey(aliceID, alice)); !bytes.Equal(got, cred) {
		t.Fatalf("save did not copy creds to the backup slot:\n want %s\n  got %s", cred, got)
	}
}

// ─── set-limit ──────────────────────────────────────────────────────────────

func TestSetLimit_TargetsLiveAccountNotStaleField(t *testing.T) {
	home := newTestHome(t)
	alice, bob := "alice@example.com", "bob@example.com"
	aliceID, bobID := account.HashEmail(alice), account.HashEmail(bob)

	seedClaudeJSON(t, home, alice) // alice is live
	seq := seqWith(t, aliceID, alice, bobID, bob)
	// Make sequence.json's recorded active account disagree with .claude.json.
	seq.ActiveAccountID = bobID
	if err := seq.Save(sequencePath()); err != nil {
		t.Fatal(err)
	}

	if err := run(t, "set-limit", "5B"); err != nil {
		t.Fatalf("set-limit: %v", err)
	}
	got := loadSeq(t)
	if got.Accounts[aliceID].WeeklyTokenLimit != 5_000_000_000 {
		t.Errorf("limit not set on the live account: %d", got.Accounts[aliceID].WeeklyTokenLimit)
	}
	if got.Accounts[bobID].WeeklyTokenLimit != 0 {
		t.Errorf("limit wrongly set on the stale recorded account: %d", got.Accounts[bobID].WeeklyTokenLimit)
	}
}

// ─── config / init-config ───────────────────────────────────────────────────

func TestInitConfig(t *testing.T) {
	newTestHome(t)
	if err := run(t, "init-config"); err != nil {
		t.Fatalf("init-config: %v", err)
	}
	if _, err := os.Stat(config.DefaultPath()); err != nil {
		t.Fatalf("init-config did not create %s: %v", config.DefaultPath(), err)
	}
}

func TestConfigPrintsBackend(t *testing.T) {
	newTestHome(t)
	out, err := capture(t, func() error { return run(t, "config") })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !strings.Contains(out, "backend") {
		t.Fatalf("config output does not mention the backend:\n%s", out)
	}
}

// ─── use-anthropic ──────────────────────────────────────────────────────────

func TestUseAnthropicStripsEnvBlock(t *testing.T) {
	newTestHome(t)
	sp := settingsPath()
	if err := os.MkdirAll(filepath.Dir(sp), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://x"},"other":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run(t, "use-anthropic"); err != nil {
		t.Fatalf("use-anthropic: %v", err)
	}
	m, ok, err := readSettings(sp)
	if err != nil || !ok {
		t.Fatalf("read settings back: ok=%v err=%v", ok, err)
	}
	if _, hasEnv := m["env"]; hasEnv {
		t.Error("env block not stripped")
	}
	if m["other"] != "keep" {
		t.Error("unrelated settings key was lost")
	}
}

// ─── legacy flag dispatch (end-to-end through a real command) ───────────────

func TestLegacyFlagDispatch_E2E(t *testing.T) {
	home := newTestHome(t)
	seedClaudeJSON(t, home, "alice@example.com")
	seqWith(t, account.HashEmail("alice@example.com"), "alice@example.com")

	out, err := capture(t, func() error { return run(t, "--list") })
	if err != nil {
		t.Fatalf("--list: %v", err)
	}
	if !strings.Contains(out, "alice@example.com") {
		t.Fatalf("legacy --list did not reach the list command:\n%s", out)
	}
}
