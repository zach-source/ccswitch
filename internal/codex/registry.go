package codex

import "github.com/zach-source/ccswitch/internal/provider"

// The registry shape belongs to the provider seam. Codex keeps its names.
type (
	Profile  = provider.Profile
	Registry = provider.Registry
)

// SchemaVersion is the registry format version.
const SchemaVersion = provider.SchemaVersion

// LoadRegistry reads the registry at path. See provider.LoadRegistry.
var LoadRegistry = provider.LoadRegistry

// CredKey returns the backend key holding a profile's captured credential.
// The prefix matches the "Claude Code-Account-…" convention so a shared
// 1Password vault stays legible.
func CredKey(label string) string { return "Codex-Account-" + label }
