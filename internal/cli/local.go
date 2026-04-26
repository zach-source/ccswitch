package cli

import (
	"runtime"

	"github.com/zach-source/ccswitch/internal/backend"
)

// autoLocalBackend returns the backend type used as the "local" side of
// sync — keychain on macOS, file elsewhere. Independent of the user's
// configured remote backend selection.
func autoLocalBackend() backend.Type {
	if runtime.GOOS == "darwin" {
		return backend.TypeKeychain
	}
	return backend.TypeFile
}
