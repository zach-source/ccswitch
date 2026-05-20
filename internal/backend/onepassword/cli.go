package onepassword

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zach-source/ccswitch/internal/backend"
)

// CLIConfig configures a CLIBackend.
type CLIConfig struct {
	Vault      string // 1Password vault name (e.g. "Personal Agents")
	ItemPrefix string // prepended to titles to namespace ccswitch items
	Account    string // op CLI account shorthand; empty uses the op default
}

// CLIBackend stores credentials as 1Password "document" items using the
// user's authenticated `op` CLI session. It exists as an alternative to the
// Connect HTTP backend when the Connect server's bearer token does not
// have create/update permission on the target vault: the CLI uses the
// user's full permissions and bypasses Connect entirely. The trade-off is
// that the caller must have an authenticated op session — typically via
// the 1Password desktop app's biometric integration.
//
// One `op signin` happens lazily on the first sub-command and the resulting
// session token is kept in sessionEnv ("OP_SESSION_<short>=<token>"). Every
// subsequent op invocation in the same process appends that env entry, so
// the biometric / desktop-app prompt fires at most once per ccswitch run
// no matter how many items get touched.
type CLIBackend struct {
	opBin   string
	vault   string
	prefix  string
	account string

	sessionEnv string // populated lazily by ensureSession; empty when signin yielded nothing
	signedIn   bool   // true once ensureSession has run (success or no-op)
}

// NewCLI returns a CLIBackend, verifying that the op CLI is on PATH and
// that a vault has been configured. The op session itself is not probed
// here; HealthCheck does that.
func NewCLI(cfg CLIConfig) (*CLIBackend, error) {
	op, err := exec.LookPath("op")
	if err != nil {
		return nil, errors.New("1password-cli backend: op CLI not installed " +
			"(install with `brew install --cask 1password-cli`)")
	}
	if cfg.Vault == "" {
		return nil, errors.New("1password-cli backend: vault not configured")
	}
	return &CLIBackend{
		opBin:   op,
		vault:   cfg.Vault,
		prefix:  cfg.ItemPrefix,
		account: cfg.Account,
	}, nil
}

// Name implements backend.Backend.
func (b *CLIBackend) Name() string { return "1password-cli" }

// itemTitle is the per-key 1Password item title — same format the Connect
// backend uses, so the two backends are interoperable against a shared
// vault (one writes, the other can read).
func (b *CLIBackend) itemTitle(key string) string {
	if b.prefix == "" {
		return key
	}
	return b.prefix + " - " + key
}

// withAccount prepends --account if one is configured. (Global op flags
// must come before the subcommand.)
func (b *CLIBackend) withAccount(args ...string) []string {
	if b.account == "" {
		return args
	}
	return append([]string{"--account", b.account}, args...)
}

// accountShorthand returns the OP_SESSION_<short> name op expects for the
// configured account. op derives the shorthand from the URL's first dot
// segment ("stigenai.1password.com" → "stigenai"); when no --account is
// configured we leave the shorthand empty and skip session caching.
func (b *CLIBackend) accountShorthand() string {
	s := strings.TrimPrefix(b.account, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return s
}

// ensureSession runs `op signin --raw` once and stashes the resulting
// session token in an OP_SESSION_<short>=<token> env entry. Subsequent op
// invocations in the same process append sessionEnv to cmd.Env so they do
// not re-prompt for biometric / desktop-app approval.
//
// If signin produces no token (the user has 1Password desktop app
// integration enabled, which provides authentication on its own) the
// method is a harmless no-op — those calls already run without prompts.
// If signin fails, the method does not surface the error; the next op
// call will fail with the real cause if the user truly is unauthenticated.
func (b *CLIBackend) ensureSession(ctx context.Context) {
	if b.signedIn {
		return
	}
	b.signedIn = true // either way, only try once per process

	short := b.accountShorthand()
	if short == "" {
		return // no shorthand to bind OP_SESSION_<short> to; skip
	}

	args := []string{"signin", "--raw"}
	if b.account != "" {
		args = append([]string{"--account", b.account}, args...)
	}
	cmd := exec.CommandContext(ctx, b.opBin, args...)
	// Inherit stdio so the biometric / passphrase prompt reaches the user.
	cmd.Stdin, cmd.Stderr = nil, nil
	out, err := cmd.Output()
	if err != nil {
		return
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return
	}
	b.sessionEnv = "OP_SESSION_" + short + "=" + token
}

// opCmd returns an op *exec.Cmd with the cached session env appended
// (when present) so the call does not re-prompt for approval. Every op
// invocation in this file goes through here.
func (b *CLIBackend) opCmd(ctx context.Context, args ...string) *exec.Cmd {
	b.ensureSession(ctx)
	cmd := exec.CommandContext(ctx, b.opBin, args...)
	if b.sessionEnv != "" {
		cmd.Env = append(os.Environ(), b.sessionEnv)
	}
	return cmd
}

// isItemNotFound recognizes op's various "this item doesn't exist" error
// messages across CLI versions.
func isItemNotFound(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "isn't an item") ||
		strings.Contains(s, "no item found") ||
		strings.Contains(s, "could not find item") ||
		strings.Contains(s, "doesn't seem to be an item")
}

// Read implements backend.Backend by downloading the document with the
// per-key title from the configured vault.
func (b *CLIBackend) Read(ctx context.Context, key string) ([]byte, error) {
	title := b.itemTitle(key)
	cmd := b.opCmd(ctx, b.withAccount("document", "get", title, "--vault", b.vault)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isItemNotFound(stderr.String()) {
			return nil, backend.ErrNotFound
		}
		return nil, fmt.Errorf("1password-cli backend: op document get %q: %w (%s)",
			title, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Write implements backend.Backend by streaming data to the document
// via `op document edit … -`. Falls back to `op document create … -`
// when no item exists yet. The credential bytes go through stdin — never
// through argv — so they never appear in `ps`.
func (b *CLIBackend) Write(ctx context.Context, key string, data []byte) error {
	title := b.itemTitle(key)
	// Edit-first; create on "not found". Updates are the common case for
	// credential refresh, so this saves one op invocation per refresh.
	if err := b.docEdit(ctx, title, data); err == nil {
		return nil
	} else if !errors.Is(err, backend.ErrNotFound) {
		return err
	}
	return b.docCreate(ctx, title, data)
}

func (b *CLIBackend) docEdit(ctx context.Context, title string, data []byte) error {
	cmd := b.opCmd(ctx, b.withAccount("document", "edit", title, "-", "--vault", b.vault)...)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isItemNotFound(stderr.String()) {
			return backend.ErrNotFound
		}
		return fmt.Errorf("1password-cli backend: op document edit %q: %w (%s)",
			title, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (b *CLIBackend) docCreate(ctx context.Context, title string, data []byte) error {
	cmd := b.opCmd(ctx, b.withAccount("document", "create", "-", "--vault", b.vault, "--title", title)...)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("1password-cli backend: op document create %q: %w (%s)",
			title, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Delete implements backend.Backend; a missing item is not an error
// (idempotent).
func (b *CLIBackend) Delete(ctx context.Context, key string) error {
	title := b.itemTitle(key)
	cmd := b.opCmd(ctx, b.withAccount("document", "delete", title, "--vault", b.vault)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isItemNotFound(stderr.String()) {
			return nil
		}
		return fmt.Errorf("1password-cli backend: op document delete %q: %w (%s)",
			title, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// HealthCheck implements backend.Backend by reading the configured vault's
// metadata. Surfaces missing op-CLI sessions as a clear error.
func (b *CLIBackend) HealthCheck(ctx context.Context) error {
	cmd := b.opCmd(ctx, b.withAccount("vault", "get", b.vault)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("1password-cli backend: op vault get %q: %w (%s)",
			b.vault, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Compile-time interface check.
var _ backend.Backend = (*CLIBackend)(nil)
