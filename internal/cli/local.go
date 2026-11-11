package cli

import (
	"os"
	"runtime"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/config"
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

// codexLocalBackend returns the backend for local Codex copies: always the
// file backend, at 0600 under the backup dir. A Codex auth.json is about
// 4.5 KB, and the keychain backend cannot write a value over about 2 KB.
// Codex itself keeps the same login as a plain 0600 file.
func codexLocalBackend() (backend.Backend, error) {
	return resolveBackend(&config.Config{Backend: backend.TypeFile})
}
