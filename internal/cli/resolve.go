package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/file"
	"github.com/zach-source/ccswitch/internal/backend/keychain"
	"github.com/zach-source/ccswitch/internal/backend/onepassword"
	"github.com/zach-source/ccswitch/internal/backend/vault"
	"github.com/zach-source/ccswitch/internal/config"
)

// displayOrg normalizes an account's stored organization name for display.
// Claude names personal-account orgs "<name>'s Organization"; that form and
// the empty string both render as "Personal".
func displayOrg(org string) string {
	if org == "" || strings.HasSuffix(org, "'s Organization") {
		return "Personal"
	}
	return org
}

// claudeIdentity is the subset of ~/.claude/.claude.json that ccswitch reads:
// the live OAuth account Claude Code is currently using.
type claudeIdentity struct {
	Email string
	UUID  string
	Org   string
}

// readClaudeIdentity reads the live Claude Code account identity. A missing
// file or absent account yields a zero claudeIdentity (not an error) so every
// caller can treat "no identity" uniformly. This is the single decoder for
// .claude.json — do not re-inline the oauthAccount struct elsewhere.
func readClaudeIdentity() claudeIdentity {
	data, err := os.ReadFile(claudeConfigPath())
	if err != nil {
		return claudeIdentity{}
	}
	var j struct {
		OAuthAccount struct {
			EmailAddress     string `json:"emailAddress"`
			AccountUUID      string `json:"accountUuid"`
			OrganizationName string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return claudeIdentity{}
	}
	return claudeIdentity{
		Email: j.OAuthAccount.EmailAddress,
		UUID:  j.OAuthAccount.AccountUUID,
		Org:   j.OAuthAccount.OrganizationName,
	}
}

// activeID returns the ID of the account currently logged in to Claude Code.
// The live source of truth is ~/.claude/.claude.json, not sequence.json's
// activeAccountId field — that recorded value goes stale whenever the user
// logs in/out through `claude` directly without going through `ccswitch
// switch`. The recorded field is used only as a fallback when .claude.json
// has no usable account, or names one ccswitch does not manage.
func activeID(seq *account.Sequence) string {
	if seq == nil {
		return ""
	}
	if email := readClaudeIdentity().Email; email != "" {
		if id := account.HashEmail(email); id != "" {
			if _, ok := seq.Accounts[id]; ok {
				return id
			}
		}
	}
	return seq.ActiveAccountID
}

// validateConnectHost rejects a 1Password Connect URL that would transmit the
// bearer token in cleartext. https:// is always allowed; http:// is allowed
// only for a loopback host (a local Connect sidecar is a legitimate setup, a
// remote cleartext one exposes the token to the network).
func validateConnectHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid Connect URL %q: %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("Connect host %q uses http:// — cleartext is permitted "+
			"only for a loopback host; use https://", raw)
	default:
		return fmt.Errorf("Connect URL must start with http:// or https://, got %q", raw)
	}
}

// isLoopbackHost reports whether host is localhost or a loopback IP literal.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// resolveBackend returns the Backend implementation for the effective type.
// "auto" picks keychain on macOS, file elsewhere.
func resolveBackend(cfg *config.Config) (backend.Backend, error) {
	typ := cfg.Backend
	if typ == backend.TypeAuto {
		typ = autoLocalBackend()
	}
	switch typ {
	case backend.TypeFile:
		return file.New(filepath.Join(backupDir(), "credentials")), nil
	case backend.TypeKeychain:
		return keychain.New(), nil
	case backend.TypeOnePassword:
		return newOnePasswordBackend(cfg)
	case backend.TypeVault:
		return vault.New(vault.Config{
			Addr:  cfg.Vault.Addr,
			Path:  cfg.Vault.Path,
			Token: cfg.Vault.Token,
		})
	default:
		return nil, fmt.Errorf("unknown backend type %q", typ)
	}
}

// newOnePasswordBackend loads the three Connect/CF Access secrets from
// the macOS Keychain (or file fallback) and constructs the HTTP backend.
// Connect mode requires at least the bearer token; CF Access pair is
// optional (used only when the Connect server sits behind Cloudflare Access).
func newOnePasswordBackend(cfg *config.Config) (backend.Backend, error) {
	if cfg.OnePassword.ConnectHost == "" {
		return nil, errors.New("1password backend: connect_host not configured (run `ccswitch setup-op-connect`)")
	}
	// Enforced here — the chokepoint for both the TOML and env-var
	// (CCSWITCH_OP_CONNECT_HOST / OP_CONNECT_HOST) config paths.
	if err := validateConnectHost(cfg.OnePassword.ConnectHost); err != nil {
		return nil, fmt.Errorf("1password backend: %w", err)
	}

	kc := keychain.New()
	ctx := context.Background()

	loadSecret := func(service string) (string, error) {
		data, err := kc.Read(ctx, service)
		if errors.Is(err, backend.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read %s from keychain: %w", service, err)
		}
		return string(data), nil
	}

	bearer, err := loadSecret(cfg.OnePassword.ConnectTokenKeychainService)
	if err != nil {
		return nil, err
	}
	if bearer == "" {
		return nil, errors.New("1password backend: Connect bearer token missing from keychain (run `ccswitch setup-op-connect`)")
	}
	cfID, err := loadSecret(cfg.OnePassword.CFAccessClientIDService)
	if err != nil {
		return nil, err
	}
	cfSecret, err := loadSecret(cfg.OnePassword.CFAccessClientSecretService)
	if err != nil {
		return nil, err
	}

	return onepassword.New(onepassword.Config{
		Host:                 cfg.OnePassword.ConnectHost,
		BearerToken:          bearer,
		CFAccessClientID:     cfID,
		CFAccessClientSecret: cfSecret,
		VaultName:            cfg.OnePassword.Vault,
		ItemPrefix:           cfg.OnePassword.ItemPrefix,
	})
}

// resolvedBackendName returns the human-readable effective backend name,
// performing auto-resolution without constructing the backend.
func resolvedBackendName(cfg *config.Config) string {
	if cfg.Backend != backend.TypeAuto {
		return string(cfg.Backend)
	}
	return string(autoLocalBackend())
}

// backupDir returns the canonical path for ccswitch state files.
func backupDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-switch-backup")
}

// sequencePath returns the canonical path for sequence.json.
func sequencePath() string {
	return filepath.Join(backupDir(), "sequence.json")
}

// envDirPath returns the default isolated CLAUDE_CONFIG_DIR for a managed
// account — the directory `ccswitch env <id>` binds a shell to. It is the
// single source of truth for this path, shared by env and remove-account.
func envDirPath(id string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-env-"+id)
}

// claudeConfigPath returns the path to ~/.claude/.claude.json with a fallback.
func claudeConfigPath() string {
	home, _ := os.UserHomeDir()
	primary := filepath.Join(home, ".claude", ".claude.json")
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	return filepath.Join(home, ".claude.json")
}
