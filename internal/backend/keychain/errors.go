package keychain

import "errors"

// ErrNotSupported is returned by every Backend operation on non-macOS
// platforms, where the keychain is unavailable. It is declared in this
// build-tag-free file so cross-platform callers can test for it with
// errors.Is on every OS (the darwin build never returns it).
var ErrNotSupported = errors.New("keychain backend: not supported on this platform")
