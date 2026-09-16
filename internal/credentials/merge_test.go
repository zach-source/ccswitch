package credentials

import (
	"encoding/json"
	"testing"
)

// liveBlob mirrors the real shape of the active keychain slot: the account's
// own OAuth tokens alongside account-independent MCP server authorizations.
const liveBlob = `{"mcpOAuth":{"linear|abc":{"serverName":"linear","accessToken":"mcp-tok"}},` +
	`"claudeAiOauth":{"accessToken":"old","refreshToken":"old-r","expiresAt":1,"refreshTokenExpiresAt":9}}`

// backupBlob mirrors a per-account backup: claudeAiOauth only.
const backupBlob = `{"claudeAiOauth":{"accessToken":"new","refreshToken":"new-r","expiresAt":2}}`

func TestSwapOAuthPreservesMCPState(t *testing.T) {
	merged, err := SwapOAuth([]byte(liveBlob), []byte(backupBlob))
	if err != nil {
		t.Fatalf("SwapOAuth: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(merged, &top); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if _, ok := top["mcpOAuth"]; !ok {
		t.Fatal("mcpOAuth was dropped — every MCP server would need re-authorization")
	}

	cred, err := Parse(merged)
	if err != nil {
		t.Fatalf("Parse merged: %v", err)
	}
	if got := cred.ClaudeAIOAuth.AccessToken; got != "new" {
		t.Errorf("accessToken = %q, want %q (the target account did not activate)", got, "new")
	}
	if got := cred.ClaudeAIOAuth.RefreshToken; got != "new-r" {
		t.Errorf("refreshToken = %q, want %q", got, "new-r")
	}
}

func TestSwapOAuthEmptyLiveReturnsIncoming(t *testing.T) {
	merged, err := SwapOAuth(nil, []byte(backupBlob))
	if err != nil {
		t.Fatalf("SwapOAuth: %v", err)
	}
	if string(merged) != backupBlob {
		t.Errorf("merged = %s, want the incoming blob verbatim", merged)
	}
}

func TestSwapOAuthRejectsBlobWithoutOAuth(t *testing.T) {
	if _, err := SwapOAuth([]byte(liveBlob), []byte(`{"mcpOAuth":{}}`)); err == nil {
		t.Fatal("expected an error for an incoming blob with no claudeAiOauth")
	}
}

func TestOAuthOnlyStripsSiblingsAndKeepsUnmodelledFields(t *testing.T) {
	out, err := OAuthOnly([]byte(liveBlob))
	if err != nil {
		t.Fatalf("OAuthOnly: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(top) != 1 {
		t.Errorf("top-level keys = %d, want 1 (claudeAiOauth only)", len(top))
	}
	if _, ok := top["mcpOAuth"]; ok {
		t.Error("mcpOAuth leaked into a per-account backup")
	}

	// refreshTokenExpiresAt is not modelled by Credentials; the raw-bytes
	// copy must keep it anyway.
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(top["claudeAiOauth"], &oauth); err != nil {
		t.Fatalf("unmarshal oauth: %v", err)
	}
	if _, ok := oauth["refreshTokenExpiresAt"]; !ok {
		t.Error("refreshTokenExpiresAt was dropped; OAuthOnly must copy raw bytes")
	}
}

// A backup produced by OAuthOnly must round-trip back through SwapOAuth
// without disturbing the live blob's sibling keys.
func TestOAuthOnlyRoundTripsThroughSwap(t *testing.T) {
	backup, err := OAuthOnly([]byte(backupBlob))
	if err != nil {
		t.Fatalf("OAuthOnly: %v", err)
	}
	merged, err := SwapOAuth([]byte(liveBlob), backup)
	if err != nil {
		t.Fatalf("SwapOAuth: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(merged, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := top["mcpOAuth"]; !ok {
		t.Fatal("mcpOAuth lost on round-trip")
	}
}
