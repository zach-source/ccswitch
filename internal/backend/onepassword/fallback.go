package onepassword

import (
	"context"
	"errors"

	"github.com/zach-source/ccswitch/internal/backend"
)

// WriteFallback reads through a Connect backend but retries writes and deletes
// through the op CLI when Connect denies them with 403. This covers the common
// deployment where the Connect token is read-only on the vault (its scope is
// fixed at token-creation time) while the user's op session has full access.
type WriteFallback struct {
	primary *Backend
	cli     *CLIBackend
}

// NewWriteFallback wraps primary so mutating operations fall back to cli on a
// permission-denied (403) response. Reads and health checks stay on primary.
func NewWriteFallback(primary *Backend, cli *CLIBackend) *WriteFallback {
	return &WriteFallback{primary: primary, cli: cli}
}

func (w *WriteFallback) Name() string { return w.primary.Name() }

func (w *WriteFallback) Read(ctx context.Context, key string) ([]byte, error) {
	return w.primary.Read(ctx, key)
}

func (w *WriteFallback) HealthCheck(ctx context.Context) error {
	return w.primary.HealthCheck(ctx)
}

func (w *WriteFallback) Write(ctx context.Context, key string, data []byte) error {
	err := w.primary.Write(ctx, key, data)
	if isPermissionDenied(err) {
		return w.cli.Write(ctx, key, data)
	}
	return err
}

func (w *WriteFallback) Delete(ctx context.Context, key string) error {
	err := w.primary.Delete(ctx, key)
	if isPermissionDenied(err) {
		return w.cli.Delete(ctx, key)
	}
	return err
}

// isPermissionDenied reports whether err is (or wraps) a Connect 403.
func isPermissionDenied(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.status == 403
}

// Compile-time interface check.
var _ backend.Backend = (*WriteFallback)(nil)
