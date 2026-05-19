// Package account manages account identity (8-char SHA-256 prefix from
// email), the sequence.json on-disk metadata file, and per-account
// keying conventions used across backends.
package account

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SchemaVersion is the on-disk format version for sequence.json.
const SchemaVersion = 2

// HashEmail returns the stable 8-char hex ID derived from an email address.
func HashEmail(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:4])
}

// Account is one entry in sequence.json.
type Account struct {
	Email            string `json:"email"`
	OrgName          string `json:"orgName,omitempty"`
	AccountUUID      string `json:"accountUuid,omitempty"`
	AddedAt          string `json:"addedAt,omitempty"`
	WeeklyTokenLimit int64  `json:"weeklyTokenLimit,omitempty"`
}

// Sequence is the on-disk sequence.json format (v2 — hash-based IDs).
type Sequence struct {
	Version         int                `json:"version"`
	ActiveAccountID string             `json:"activeAccountId,omitempty"`
	Sequence        []string           `json:"sequence"`
	Accounts        map[string]Account `json:"accounts"`
	LastUpdated     string             `json:"lastUpdated,omitempty"`
	SwitchLog       []SwitchLogEntry   `json:"switchLog,omitempty"`
}

// SwitchLogEntry records one --switch invocation.
type SwitchLogEntry struct {
	Timestamp string `json:"timestamp"`
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
}

// ParseSequence decodes a Sequence from a JSON byte slice (e.g. from a backend).
func ParseSequence(data []byte) (*Sequence, error) {
	var s Sequence
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse sequence: %w", err)
	}
	if s.Accounts == nil {
		s.Accounts = map[string]Account{}
	}
	return &s, nil
}

// MarshalSequence serialises a Sequence to compact JSON.
// When stripSwitchLog is true the SwitchLog field is omitted (matching
// cmd_push / cmd_sync behaviour in the bash reference).
func MarshalSequence(s *Sequence, stripSwitchLog bool) ([]byte, error) {
	if !stripSwitchLog {
		return json.Marshal(s)
	}
	type seqNoLog struct {
		Version         int                `json:"version"`
		ActiveAccountID string             `json:"activeAccountId,omitempty"`
		Sequence        []string           `json:"sequence"`
		Accounts        map[string]Account `json:"accounts"`
		LastUpdated     string             `json:"lastUpdated,omitempty"`
	}
	return json.Marshal(seqNoLog{
		Version:         s.Version,
		ActiveAccountID: s.ActiveAccountID,
		Sequence:        s.Sequence,
		Accounts:        s.Accounts,
		LastUpdated:     s.LastUpdated,
	})
}

// LoadSequence reads sequence.json from path. Returns a fresh empty
// Sequence (v2) if the file is missing.
func LoadSequence(path string) (*Sequence, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Sequence{
			Version:  SchemaVersion,
			Accounts: map[string]Account{},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sequence: %w", err)
	}
	var s Sequence
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse sequence: %w", err)
	}
	if s.Accounts == nil {
		s.Accounts = map[string]Account{}
	}
	return &s, nil
}

// Save writes sequence.json atomically (write-temp + rename) with 0600 perms.
func (s *Sequence) Save(path string) error {
	s.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sequence: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IDs returns account IDs in their canonical sequence order.
func (s *Sequence) IDs() []string {
	out := make([]string, len(s.Sequence))
	copy(out, s.Sequence)
	return out
}

// SortedIDs returns account IDs sorted by sequence position then ID
// (deterministic for testing).
func (s *Sequence) SortedIDs() []string {
	ids := s.IDs()
	sort.Strings(ids)
	return ids
}

// Add inserts an account into the sequence (idempotent on ID).
func (s *Sequence) Add(id string, acct Account) {
	if _, exists := s.Accounts[id]; exists {
		s.Accounts[id] = acct
		return
	}
	s.Accounts[id] = acct
	s.Sequence = append(s.Sequence, id)
	if s.ActiveAccountID == "" {
		s.ActiveAccountID = id
	}
}

// Remove deletes an account by ID. Returns true if removed.
func (s *Sequence) Remove(id string) bool {
	if _, ok := s.Accounts[id]; !ok {
		return false
	}
	delete(s.Accounts, id)
	for i, x := range s.Sequence {
		if x == id {
			s.Sequence = append(s.Sequence[:i], s.Sequence[i+1:]...)
			break
		}
	}
	if s.ActiveAccountID == id {
		s.ActiveAccountID = ""
		if len(s.Sequence) > 0 {
			s.ActiveAccountID = s.Sequence[0]
		}
	}
	return true
}

// SetWeeklyLimit records a weekly token limit on the account identified by
// id. Returns false if no such account exists. Encapsulating the map-value
// read-modify-write here keeps callers from forgetting the write-back that a
// bare `s.Accounts[id]` copy would silently require.
func (s *Sequence) SetWeeklyLimit(id string, limit int64) bool {
	acct, ok := s.Accounts[id]
	if !ok {
		return false
	}
	acct.WeeklyTokenLimit = limit
	s.Accounts[id] = acct
	return true
}

// Resolve returns the account ID for a hash, email, or 1-based numeric
// index identifier. Empty string if no match.
func (s *Sequence) Resolve(identifier string) string {
	// Already a hash?
	if _, ok := s.Accounts[identifier]; ok {
		return identifier
	}
	// Email?
	if h := HashEmail(identifier); s.Accounts[h].Email == identifier {
		return h
	}
	// Numeric index?
	for i, id := range s.Sequence {
		if fmt.Sprintf("%d", i+1) == identifier {
			return id
		}
	}
	// Email scan (legacy / edge cases where hash may differ)
	for id, acct := range s.Accounts {
		if acct.Email == identifier {
			return id
		}
	}
	return ""
}
