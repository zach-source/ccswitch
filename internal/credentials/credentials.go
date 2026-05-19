// Package credentials defines the on-disk shape of a Claude Code OAuth
// credential blob and helpers for inspecting it. The shape mirrors what
// `claude auth login` writes to ~/.claude/.credentials.json.
package credentials

import (
	"encoding/json"
	"fmt"
	"time"
)

// Credentials is the top-level credential JSON shape.
type Credentials struct {
	ClaudeAIOAuth ClaudeAIOAuth `json:"claudeAiOauth"`
}

// ClaudeAIOAuth holds the OAuth token data Claude Code persists.
type ClaudeAIOAuth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAtMillis  int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes,omitempty"`
	SubscriptionType string   `json:"subscriptionType,omitempty"`
	RateLimitTier    string   `json:"rateLimitTier,omitempty"`
}

// Parse decodes a JSON credential blob; returns nil + error on bad input.
func Parse(data []byte) (*Credentials, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty credential blob")
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &c, nil
}

// Marshal serializes a credential blob to compact JSON.
//
// WARNING: this is lossy. Credentials is a typed inspection lens, not a
// faithful mirror of .credentials.json — any field Claude Code writes that
// this struct does not model is dropped. Never persist Marshal output as a
// stored credential; write the original raw bytes instead. Marshal is safe
// only for throwaway uses (e.g. seeding a tmpdir that will be overwritten).
func (c *Credentials) Marshal() ([]byte, error) {
	return json.Marshal(c)
}

// ExpiresAt returns the credential's expiry as a time.Time. Zero time
// if the field is missing.
func (c *Credentials) ExpiresAt() time.Time {
	if c.ClaudeAIOAuth.ExpiresAtMillis == 0 {
		return time.Time{}
	}
	return time.UnixMilli(c.ClaudeAIOAuth.ExpiresAtMillis)
}

// HoursLeft returns time until expiry in hours (negative if expired).
func (c *Credentials) HoursLeft() float64 {
	if c.ClaudeAIOAuth.ExpiresAtMillis == 0 {
		return 0
	}
	return time.Until(c.ExpiresAt()).Hours()
}

// IsExpired returns true if the access token is expired or expires within
// the buffer window (typical buffer: 5 minutes).
func (c *Credentials) IsExpired(buffer time.Duration) bool {
	if c.ClaudeAIOAuth.ExpiresAtMillis == 0 {
		return true
	}
	return time.Now().Add(buffer).After(c.ExpiresAt())
}
