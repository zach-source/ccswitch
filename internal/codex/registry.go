package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// SchemaVersion is the registry format version.
const SchemaVersion = 1

// CredKey returns the backend key holding a profile's captured credential.
// The prefix matches the "Claude Code-Account-…" convention so a shared
// 1Password vault stays legible.
func CredKey(label string) string { return "Codex-Account-" + label }

// Profile is one captured Codex login. The identity fields are a snapshot
// taken at capture time, kept only for display — the credential itself is
// the source of truth and lives in the backend, never here.
type Profile struct {
	Label     string `json:"label"`
	Mode      string `json:"mode,omitempty"`
	Email     string `json:"email,omitempty"`
	Plan      string `json:"plan,omitempty"`
	AccountID string `json:"accountId,omitempty"`
}

// Registry is the set of known Codex profiles and which one was last made
// live. It holds no secrets, so it sits in a plain file rather than a
// backend — the same split as sequence.json.
//
// Priority and LastSwitch are additive, so the schema version stays 1: an
// older file loads with both empty.
type Registry struct {
	Version  int                `json:"version"`
	Active   string             `json:"active,omitempty"`
	Profiles map[string]Profile `json:"profiles"`
	// Priority lists labels in the order autoswitch tries them.
	Priority []string `json:"priority,omitempty"`
	// LastSwitch is when autoswitch last switched, used for its cooldown.
	LastSwitch time.Time `json:"lastSwitch,omitzero"`
}

// LoadRegistry reads the registry at path. A missing file is an empty
// registry, not an error — that is the state before the first capture.
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Registry{Version: SchemaVersion, Profiles: map[string]Profile{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read codex registry: %w", err)
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse codex registry: %w", err)
	}
	if r.Profiles == nil {
		r.Profiles = map[string]Profile{}
	}
	r.Version = SchemaVersion
	return &r, nil
}

// Save writes the registry atomically at mode 0600.
func (r *Registry) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create registry dir: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal codex registry: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write codex registry: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install codex registry: %w", err)
	}
	return nil
}

// Labels returns every profile label, sorted.
func (r *Registry) Labels() []string {
	out := make([]string, 0, len(r.Profiles))
	for label := range r.Profiles {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// Ordered returns the labels in switch order: Priority first, skipping
// duplicates and labels with no profile, then every remaining label sorted.
// A stale Priority entry therefore never breaks a switch. A promax profile
// always comes before a pro profile; plans of the same rank keep this order.
func (r *Registry) Ordered() []string {
	out := make([]string, 0, len(r.Profiles))
	seen := make(map[string]bool, len(r.Profiles))
	for _, label := range r.Priority {
		if _, ok := r.Profiles[label]; ok && !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	for _, label := range r.Labels() {
		if !seen[label] {
			out = append(out, label)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int {
		return planRank(r.Profiles[a].Plan) - planRank(r.Profiles[b].Plan)
	})
	return out
}

// planRank orders plans for switch order. A lower rank comes first.
func planRank(plan string) int {
	switch plan {
	case "promax":
		return 0
	case "pro":
		return 1
	}
	return 2
}
