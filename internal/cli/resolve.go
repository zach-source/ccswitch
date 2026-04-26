package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/backend/file"
	"github.com/zach-source/ccswitch/internal/backend/keychain"
	"github.com/zach-source/ccswitch/internal/backend/onepassword"
	"github.com/zach-source/ccswitch/internal/backend/vault"
	"github.com/zach-source/ccswitch/internal/config"
)

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

// claudeConfigPath returns the path to ~/.claude/.claude.json with a fallback.
func claudeConfigPath() string {
	home, _ := os.UserHomeDir()
	primary := filepath.Join(home, ".claude", ".claude.json")
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	return filepath.Join(home, ".claude.json")
}
