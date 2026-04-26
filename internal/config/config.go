// Package config loads ccswitch configuration from
// ~/.config/ccswitch/config.toml plus environment overrides.
//
// Precedence (highest wins): env var > TOML > built-in default.
package config

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zach-source/ccswitch/internal/backend"
)

// Config is the merged effective configuration after loading TOML + env.
type Config struct {
	Backend     backend.Type
	OnePassword OnePasswordConfig
	Vault       VaultConfig
	Sync        SyncConfig
	Refresh     RefreshConfig

	// ConfigFile records the TOML path actually read (may be empty if
	// the caller used CCSWITCH_CONFIG_FILE=/dev/null or it was missing).
	ConfigFile string
}

// OnePasswordConfig holds 1Password backend settings.
type OnePasswordConfig struct {
	Vault      string
	ItemPrefix string

	// Account is the op CLI account shorthand (signed-in mode only).
	// Ignored when ConnectHost is set.
	Account string

	// ConnectHost — when non-empty, route through HTTP Connect
	// (with X-OP-Token + optional CF Access headers) instead of `op` CLI.
	ConnectHost string

	// Keychain service/account names from which Connect/CF Access
	// secrets are loaded at startup.
	ConnectTokenKeychainService string
	ConnectTokenKeychainAccount string
	CFAccessClientIDService     string
	CFAccessClientSecretService string
}

// VaultConfig holds HashiCorp Vault / OpenBao settings.
type VaultConfig struct {
	Addr  string
	Path  string
	Token string
}

// SyncConfig holds daemon timing.
type SyncConfig struct {
	Interval time.Duration
}

// RefreshConfig holds token-refresh behavior.
type RefreshConfig struct {
	ExpiryBuffer time.Duration
}

// Defaults returns a Config populated with built-in defaults (no TOML, no env).
func Defaults() *Config {
	return &Config{
		Backend: backend.TypeAuto,
		OnePassword: OnePasswordConfig{
			Vault:                       "Private",
			ItemPrefix:                  "Claude Code Account",
			ConnectTokenKeychainService: "ccswitch-op-connect-token",
			ConnectTokenKeychainAccount: "ccswitch",
			CFAccessClientIDService:     "ccswitch-cf-access-client-id",
			CFAccessClientSecretService: "ccswitch-cf-access-client-secret",
		},
		Vault: VaultConfig{
			Path: "secret/data/ccswitch",
		},
		Sync:    SyncConfig{Interval: 5 * time.Minute},
		Refresh: RefreshConfig{ExpiryBuffer: 5 * time.Minute},
	}
}

// DefaultPath returns the standard config file location for the current user.
func DefaultPath() string {
	if p := os.Getenv("CCSWITCH_CONFIG_FILE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ccswitch", "config.toml")
}

// Load merges defaults + TOML file + environment overrides.
// Missing TOML file is not an error — defaults stand.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	cfg.ConfigFile = path

	if path != "" && path != "/dev/null" {
		if err := loadTOML(cfg, path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("load TOML %s: %w", path, err)
		}
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

// IsConnectMode reports whether 1Password Connect HTTP should be used.
func (c *Config) IsConnectMode() bool {
	return c.OnePassword.ConnectHost != ""
}

// applyEnvOverrides applies environment variable overrides to cfg.
// Env wins over TOML.
func applyEnvOverrides(c *Config) {
	if v := os.Getenv("CCSWITCH_BACKEND"); v != "" {
		c.Backend = backend.Type(v)
	}
	if v := os.Getenv("CCSWITCH_OP_VAULT"); v != "" {
		c.OnePassword.Vault = v
	}
	if v := os.Getenv("CCSWITCH_OP_ITEM_PREFIX"); v != "" {
		c.OnePassword.ItemPrefix = v
	}
	if v := os.Getenv("CCSWITCH_OP_ACCOUNT"); v != "" {
		c.OnePassword.Account = v
	}
	if v := cmp.Or(os.Getenv("CCSWITCH_OP_CONNECT_HOST"), os.Getenv("OP_CONNECT_HOST")); v != "" {
		c.OnePassword.ConnectHost = v
	}
	if v := cmp.Or(os.Getenv("CCSWITCH_VAULT_ADDR"), os.Getenv("VAULT_ADDR")); v != "" {
		c.Vault.Addr = v
	}
	if v := os.Getenv("CCSWITCH_VAULT_PATH"); v != "" {
		c.Vault.Path = v
	}
	if v := cmp.Or(os.Getenv("CCSWITCH_VAULT_TOKEN"), os.Getenv("VAULT_TOKEN")); v != "" {
		c.Vault.Token = v
	}
	if v := os.Getenv("CCSWITCH_SYNC_INTERVAL"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			c.Sync.Interval = time.Duration(secs) * time.Second
		}
	}
	if v := os.Getenv("CCSWITCH_EXPIRY_BUFFER_MINUTES"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 {
			c.Refresh.ExpiryBuffer = time.Duration(mins) * time.Minute
		}
	}
}
