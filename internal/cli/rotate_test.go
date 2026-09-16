package cli

import (
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/account"
)

// A refresh of the active account must land in the live slot, not only in the
// backup. The exchange rotates the refresh token server-side; a live slot left
// on the old one replays it at Claude Code's next refresh, and a replayed
// refresh token is what gets the whole family revoked.
func TestRefreshAll_RotatesTheLiveActiveSlot(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)

	fakeTokenEndpoint(t, 200, `{"access_token":"fresh-AT","refresh_token":"fresh-RT","expires_in":3600}`)

	expired := `{"accessToken":"old-AT","refreshToken":"old-RT","expiresAt":1}`
	seedCred(t, home, account.ActiveCredKey,
		[]byte(`{"claudeAiOauth":`+expired+`,"mcpOAuth":{"linear":"mcp-token"}}`))
	seedCred(t, home, account.BackupCredKey(aliceID, alice),
		[]byte(`{"claudeAiOauth":`+expired+`}`))

	if err := run(t, "refresh-all"); err != nil {
		t.Fatalf("refresh-all: %v", err)
	}

	live := string(readCred(t, home, account.ActiveCredKey))
	if !strings.Contains(live, "fresh-RT") {
		t.Errorf("live slot kept a superseded refresh token:\n%s", live)
	}
	if !strings.Contains(live, "mcp-token") {
		t.Errorf("refreshing the live slot dropped mcpOAuth:\n%s", live)
	}

	backup := string(readCred(t, home, account.BackupCredKey(aliceID, alice)))
	if !strings.Contains(backup, "fresh-RT") {
		t.Errorf("backup slot was not refreshed:\n%s", backup)
	}
	if strings.Contains(backup, "mcp-token") {
		t.Errorf("per-account backup absorbed account-independent MCP state:\n%s", backup)
	}
}

// A still-valid live credential is snapshotted, not refreshed — spending a
// rotation on a token that has hours left is pure risk.
func TestRefreshAll_ValidActiveCredentialIsSnapshottedNotRotated(t *testing.T) {
	home := newTestHome(t)
	alice := "alice@example.com"
	aliceID := account.HashEmail(alice)
	seedClaudeJSON(t, home, alice)
	seqWith(t, aliceID, alice)

	// No token endpoint is stood up: any exchange attempt fails the test by
	// reaching the real one, and the assertions below catch the result.
	valid := `{"accessToken":"live-AT","refreshToken":"live-RT","expiresAt":99999999999999}`
	seedCred(t, home, account.ActiveCredKey,
		[]byte(`{"claudeAiOauth":`+valid+`,"mcpOAuth":{"linear":"mcp-token"}}`))

	if err := run(t, "refresh-all"); err != nil {
		t.Fatalf("refresh-all: %v", err)
	}

	live := string(readCred(t, home, account.ActiveCredKey))
	if !strings.Contains(live, "live-RT") || !strings.Contains(live, "mcp-token") {
		t.Errorf("live slot was disturbed by a no-op refresh:\n%s", live)
	}

	backup := string(readCred(t, home, account.BackupCredKey(aliceID, alice)))
	if !strings.Contains(backup, "live-RT") {
		t.Errorf("valid live credential was not snapshotted into the backup:\n%s", backup)
	}
	if strings.Contains(backup, "mcp-token") {
		t.Errorf("snapshot absorbed account-independent MCP state:\n%s", backup)
	}
}
