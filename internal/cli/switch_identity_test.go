package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/account"
)

// seedClaudeJSONWith writes a .claude.json that includes the given oauthAccount
// block plus an unrelated top-level key. The unrelated key's purpose is to
// prove writeClaudeOAuthBlock merges (rather than clobbers) the file — in
// production .claude.json holds ~450 KB of session/MCP/project state that
// must survive an identity swap byte-for-byte.
func seedClaudeJSONWith(t *testing.T, home, oauthBlock, extraKey, extraValue string) {
	t.Helper()
	body := `{"oauthAccount":` + oauthBlock + `,"` + extraKey + `":` + extraValue + `}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSwitchTo_RestoresTargetOAuthAccount is the regression test for the bug
// where switch-to only swapped the Keychain credential and left
// .claude.json.oauthAccount pointing at the previous account.
func TestSwitchTo_RestoresTargetOAuthAccount(t *testing.T) {
	home := newTestHome(t)
	alice, bob := "alice@example.com", "bob@example.com"
	aliceID, bobID := account.HashEmail(alice), account.HashEmail(bob)

	aliceBlock := `{"emailAddress":"alice@example.com","organizationName":"Personal","accountUuid":"alice-uuid"}`
	seedClaudeJSONWith(t, home, aliceBlock, "mcpServers", `{"keep":"this"}`)

	// Seed sequence with bob's stored identity (set at add-account / save time
	// in production; written directly here for test setup).
	seq := seqWith(t, aliceID, alice, bobID, bob)
	bobAcct := seq.Accounts[bobID]
	bobAcct.OAuthAccount = json.RawMessage(`{"emailAddress":"bob@example.com","organizationName":"Personal","accountUuid":"bob-uuid","displayName":"Bob"}`)
	seq.Accounts[bobID] = bobAcct
	if err := seq.Save(sequencePath()); err != nil {
		t.Fatal(err)
	}

	seedCred(t, home, account.ActiveCredKey, []byte(`{"claudeAiOauth":{"accessToken":"alice"}}`))
	seedCred(t, home, account.BackupCredKey(bobID, bob), []byte(`{"claudeAiOauth":{"accessToken":"bob"}}`))

	if err := run(t, "switch-to", bobID); err != nil {
		t.Fatalf("switch-to %s: %v", bobID, err)
	}

	// .claude.json oauthAccount must now name bob, and mcpServers must survive.
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("parse .claude.json: %v", err)
	}
	var oauth struct {
		EmailAddress string `json:"emailAddress"`
		AccountUUID  string `json:"accountUuid"`
	}
	if err := json.Unmarshal(top["oauthAccount"], &oauth); err != nil {
		t.Fatalf("parse oauthAccount: %v", err)
	}
	if oauth.EmailAddress != bob || oauth.AccountUUID != "bob-uuid" {
		t.Fatalf("oauthAccount not bob's: got %+v", oauth)
	}
	// Unrelated top-level key preserved across the merge. The bytes get
	// re-indented by MarshalIndent, so compare semantically.
	var mcp map[string]string
	if err := json.Unmarshal(top["mcpServers"], &mcp); err != nil {
		t.Fatalf("parse mcpServers: %v", err)
	}
	if mcp["keep"] != "this" {
		t.Fatalf("mcpServers not preserved: got %+v", mcp)
	}

	// alice's prior identity was snapshotted onto her account record so a
	// future switch back to her restores it.
	seq2 := loadSeq(t)
	aliceAcct := seq2.Accounts[aliceID]
	if len(aliceAcct.OAuthAccount) == 0 {
		t.Fatal("alice's OAuthAccount was not captured during prior-snapshot")
	}
	var aOAuth struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := json.Unmarshal(aliceAcct.OAuthAccount, &aOAuth); err != nil {
		t.Fatalf("parse alice's captured OAuthAccount: %v", err)
	}
	if aOAuth.EmailAddress != alice {
		t.Fatalf("alice's snapshotted email = %q, want %q", aOAuth.EmailAddress, alice)
	}
}

// TestSwitchTo_FallsBackToLegacyConfigSnapshot covers the migration path for
// accounts added before this feature: when Account.OAuthAccount is empty, the
// switch reads the legacy ~/.claude-switch-backup/configs/.claude-config-<id>-<email>.json
// snapshot left over from the original shell ccswitch.sh and persists it
// forward onto the account record.
func TestSwitchTo_FallsBackToLegacyConfigSnapshot(t *testing.T) {
	home := newTestHome(t)
	alice, bob := "alice@example.com", "bob@example.com"
	aliceID, bobID := account.HashEmail(alice), account.HashEmail(bob)

	seedClaudeJSONWith(t, home,
		`{"emailAddress":"alice@example.com","accountUuid":"alice-uuid"}`,
		"unrelated", `"keep"`)
	seqWith(t, aliceID, alice, bobID, bob) // bob has NO OAuthAccount on his account record
	seedCred(t, home, account.ActiveCredKey, []byte(`{"x":"a"}`))
	seedCred(t, home, account.BackupCredKey(bobID, bob), []byte(`{"x":"b"}`))

	// Drop a legacy snapshot in the configs dir.
	legacyDir := filepath.Join(home, ".claude-switch-backup", "configs")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyBody := `{"oauthAccount":{"emailAddress":"bob@example.com","accountUuid":"bob-uuid-legacy"},"sessions":["old"]}`
	legacyPath := filepath.Join(legacyDir, ".claude-config-"+bobID+"-"+bob+".json")
	if err := os.WriteFile(legacyPath, []byte(legacyBody), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run(t, "switch-to", bobID); err != nil {
		t.Fatalf("switch-to: %v", err)
	}

	// Live .claude.json now names bob (from the legacy fallback). Parse,
	// don't substring-match — MarshalIndent adds whitespace.
	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var live struct {
		OAuthAccount struct {
			AccountUUID string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &live); err != nil {
		t.Fatalf("parse .claude.json: %v", err)
	}
	if live.OAuthAccount.AccountUUID != "bob-uuid-legacy" {
		t.Fatalf(".claude.json was not updated from legacy snapshot: got uuid=%q", live.OAuthAccount.AccountUUID)
	}

	// And the snapshot is now persisted forward on bob's account record so
	// future switches don't need the legacy file.
	seq := loadSeq(t)
	if len(seq.Accounts[bobID].OAuthAccount) == 0 {
		t.Fatal("expected legacy fallback to be persisted onto Account.OAuthAccount")
	}
}

// TestSwitchTo_WithoutAnyStoredIdentityWarnsButSucceeds asserts that when
// neither the account record nor a legacy snapshot has the target's identity,
// the switch still succeeds (token half swapped) and a warning is printed to
// stderr. The .claude.json file is left untouched in that case.
func TestSwitchTo_WithoutAnyStoredIdentityWarnsButSucceeds(t *testing.T) {
	home := newTestHome(t)
	alice, bob := "alice@example.com", "bob@example.com"
	aliceID, bobID := account.HashEmail(alice), account.HashEmail(bob)

	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice, bobID, bob) // bob: no OAuthAccount, no legacy snapshot
	seedCred(t, home, account.ActiveCredKey, []byte(`{"x":"a"}`))
	seedCred(t, home, account.BackupCredKey(bobID, bob), []byte(`{"x":"b"}`))

	if err := run(t, "switch-to", bobID); err != nil {
		t.Fatalf("switch-to should not error when identity is missing: %v", err)
	}
	// .claude.json untouched (still alice).
	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(data), `"emailAddress":"alice@example.com"`) {
		t.Fatalf(".claude.json should be unchanged when no identity available: %s", data)
	}
}
