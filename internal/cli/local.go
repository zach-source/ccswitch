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
