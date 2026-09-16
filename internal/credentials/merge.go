package credentials

import (
	"encoding/json"
	"fmt"
)

// oauthKey is the top-level key in a Claude Code credential blob that holds
// the account's own OAuth tokens. Everything else in the blob — notably
// "mcpOAuth", which caches per-MCP-server authorizations — is
// account-independent and must survive an account switch.
const oauthKey = "claudeAiOauth"

// SwapOAuth returns live with its claudeAiOauth value replaced by the one
// from incoming, leaving every other top-level key of live byte-identical.
//
// This is the correct way to activate a stored credential. Writing incoming
// verbatim into the active slot destroys the sibling keys: an account backup
// holds only claudeAiOauth, so a wholesale write wipes mcpOAuth and forces a
// re-authorization of every MCP server after every switch.
//
// live may be empty or unparsable (no prior active slot, or a blob written by
// a future Claude Code we cannot decode) — incoming is then returned as-is,
// which is exactly the old behaviour and the best we can do.
func SwapOAuth(live, incoming []byte) ([]byte, error) {
	block, err := extractOAuth(incoming)
	if err != nil {
		return nil, fmt.Errorf("swap oauth: incoming: %w", err)
	}

	var top map[string]json.RawMessage
	if len(live) == 0 || json.Unmarshal(live, &top) != nil || top == nil {
		return incoming, nil
	}
	top[oauthKey] = block

	merged, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("swap oauth: marshal: %w", err)
	}
	return merged, nil
}

// OAuthOnly returns data reduced to just its claudeAiOauth key.
//
// Per-account backups are stored in this form so account-independent state
// (mcpOAuth) is never filed under one account's identity and then smeared
// onto another account on the next switch. The claudeAiOauth value itself is
// copied as raw bytes, so fields this package does not model — today
// refreshTokenExpiresAt — are preserved.
func OAuthOnly(data []byte) ([]byte, error) {
	block, err := extractOAuth(data)
	if err != nil {
		return nil, fmt.Errorf("oauth only: %w", err)
	}
	out, err := json.Marshal(map[string]json.RawMessage{oauthKey: block})
	if err != nil {
		return nil, fmt.Errorf("oauth only: marshal: %w", err)
	}
	return out, nil
}

// extractOAuth pulls the raw claudeAiOauth value out of a credential blob.
func extractOAuth(data []byte) (json.RawMessage, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty credential blob")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse credential blob: %w", err)
	}
	block, ok := top[oauthKey]
	if !ok || len(block) == 0 {
		return nil, fmt.Errorf("credential blob has no %s", oauthKey)
	}
	return block, nil
}
