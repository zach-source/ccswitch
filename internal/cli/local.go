package cli

import (
	"os"
	"runtime"

	"github.com/zach-source/ccswitch/internal/backend"
)

// autoLocalBackend returns the backend type used as the "local" side of
// sync and the active credential slot — keychain on macOS, file elsewhere.
// Independent of the user's configured remote backend selection.
//
// CCSWITCH_LOCAL_BACKEND overrides the OS default. It exists both as a
// genuine escape hatch (a macOS user who prefers a file-based local store)
// and so the conformance suite can exercise switch/sync hermetically
// without touching the real login keychain.
func autoLocalBackend() backend.Type {
	if v := os.Getenv("CCSWITCH_LOCAL_BACKEND"); v != "" {
		return backend.Type(v)
	}
	if runtime.GOOS == "darwin" {
		return backend.TypeKeychain
	}
	return backend.TypeFile
}

// autoStoreBackend returns the backend type used for per-account credential
// *backups* when the user has not configured one — 0600 files, on every OS.
//
// This deliberately differs from autoLocalBackend on macOS. The active slot
// has to live in the keychain because that is where `claude` reads it, but
// backups do not, and putting them there is expensive: every keychain item
// carries its own access-control list naming the exact binaries allowed to
// read it, keyed by code signature. ccswitch ships ad-hoc signed, so its
// designated requirement is a bare cdhash that changes on every rebuild —
// after which no ACL recognises it and macOS prompts for the login keychain
// password once per item. Observed in the wild: a single backup item had
// accumulated seven dead nix-store entries, none matching the installed
// binary. Files have no such per-item grant, so N+1 prompt surfaces collapse
// to the one slot that genuinely needs the keychain.
//
// An explicit `backend = "keychain"` (or CCSWITCH_BACKEND=keychain) still
// works and is still honoured — this only changes what "auto" means.
func autoStoreBackend() backend.Type { return backend.TypeFile }
