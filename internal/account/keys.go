package account

import "fmt"

// ActiveCredKey is the backend key for the currently-active credential slot.
// All backends use the same logical key; the keychain backend treats it as
// the macOS Keychain "service" name; the file backend turns it into a
// filename; the 1Password backend prefixes it with the configured
// ItemPrefix to form an item title.
const ActiveCredKey = "Claude Code-credentials"

// BackupCredKey returns the backend key for an account's backup credential
// slot, identified by the 8-char hash ID and the email. The format is
// stable across backends and matches the bash reference's naming so a
// shared 1Password vault can be read/written by either implementation.
func BackupCredKey(id, email string) string {
	return fmt.Sprintf("Claude Code-Account-%s-%s", id, email)
}

// SequenceKey is the backend key under which the sequence.json metadata is
// mirrored. Backend-agnostic — the 1Password backend may decorate it with
// ItemPrefix at the storage layer, but the logical key is constant.
const SequenceKey = "ccswitch-sequence"
