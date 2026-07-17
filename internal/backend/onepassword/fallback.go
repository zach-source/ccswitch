package onepassword

import (
	"context"
	"errors"

	"github.com/zach-source/ccswitch/internal/backend"
)

// CLIFallback layers the user's op CLI session under a Connect backend to cover
// two gaps in a read-only-token / CLI-written-item deployment:
//
//   - Writes: a Connect token's read/write scope is fixed at creation, so a
//     read-only token 403s on login's credential write. Write/Delete retry
//     through the op CLI (full user permissions) on a 403.
//   - Reads: the CLI backend stores credentials as op *documents*, whose payload
//     Connect's Read (which only understands secure-note "credentials" fields)
//     cannot see — it returns ErrNotFound. Read retries through the op CLI on
//     ErrNotFound so document-stored items remain readable (e.g. usage-all for
//     non-active accounts).
//
// Reads try Connect first (fast, no biometric) and only touch the CLI when
// Connect can't satisfy them, so the op session is prompted at most once per
// run and only when actually needed.
type CLIFallback struct {
	primary *Backend
	cli     *CLIBackend
}

// NewCLIFallback wraps primary so reads fall back to cli on ErrNotFound and
// writes/deletes fall back to cli on a permission-denied (403) response.
func NewCLIFallback(primary *Backend, cli *CLIBackend) *CLIFallback {
	return &CLIFallback{primary: primary, cli: cli}
}

func (w *CLIFallback) Name() string { return w.primary.Name() }

func (w *CLIFallback) Read(ctx context.Context, key string) ([]byte, error) {
	data, err := w.primary.Read(ctx, key)
	if errors.Is(err, backend.ErrNotFound) {
		return w.cli.Read(ctx, key)
	}
	return data, err
}

func (w *CLIFallback) HealthCheck(ctx context.Context) error {
	return w.primary.HealthCheck(ctx)
}

func (w *CLIFallback) Write(ctx context.Context, key string, data []byte) error {
	err := w.primary.Write(ctx, key, data)
	if isPermissionDenied(err) {
		return w.cli.Write(ctx, key, data)
	}
	return err
}

func (w *CLIFallback) Delete(ctx context.Context, key string) error {
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
var _ backend.Backend = (*CLIFallback)(nil)
