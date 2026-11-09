package onepassword

import (
	"context"
	"errors"

	"github.com/zach-source/ccswitch/internal/backend"
)

// CLIFallback layers the user's op CLI session under a Connect backend to cover
// two gaps in a read-only-token / CLI-written-item deployment:
//
//   - Writes: every write goes through the op CLI, so items stay op
//     documents. Connect cannot change a document's file: a PUT gets 400
//     "Item body does not match Item path". A Connect create also makes a
//     Secure Note, which `op document get` cannot read. Delete tries Connect
//     first and uses the op CLI on a 403 (a read-only token).
//   - Reads: the CLI backend stores credentials as op *documents*. Connect's
//     Read fetches a document's attached file itself, but reports ErrNotFound
//     when it can't (missing item, file fetch refused). Read retries through
//     the op CLI on ErrNotFound so document-stored items remain readable.
//
// Reads try Connect first (fast, no biometric) and only touch the CLI when
// Connect can't satisfy them, so the op session is prompted at most once per
// run and only when actually needed.
type CLIFallback struct {
	primary *Backend
	cli     *CLIBackend
}

// NewCLIFallback wraps primary so reads fall back to cli on ErrNotFound,
// writes always use cli, and deletes fall back to cli on a 403.
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
	return w.cli.Write(ctx, key, data)
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
