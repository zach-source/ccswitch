package config

import (
	"fmt"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/zach-source/ccswitch/internal/backend"
)

// tomlFile mirrors the TOML config schema for unmarshalling.
// Schema: [backend] type = "..."; [backend.onepassword] ...; [backend.vault] ...
type tomlFile struct {
	Backend struct {
		Type        string `toml:"type"`
		OnePassword struct {
			Vault                       string `toml:"vault"`
			ItemPrefix                  string `toml:"item_prefix"`
			Account                     string `toml:"account"`
			ConnectHost                 string `toml:"connect_host"`
			ConnectTokenKeychainService string `toml:"connect_token_keychain_service"`
			ConnectTokenKeychainAccount string `toml:"connect_token_keychain_account"`
			CFAccessClientIDService     string `toml:"cf_access_client_id_service"`
			CFAccessClientSecretService string `toml:"cf_access_client_secret_service"`
		} `toml:"onepassword"`
		Vault struct {
			Addr  string `toml:"addr"`
			Path  string `toml:"path"`
			Token string `toml:"token"`
		} `toml:"vault"`
	} `toml:"backend"`
	Sync struct {
		Interval int `toml:"interval"` // seconds
	} `toml:"sync"`
	Refresh struct {
		ExpiryBufferMinutes int `toml:"expiry_buffer_minutes"`
	} `toml:"refresh"`
}

// loadTOML reads the TOML file at path and merges non-zero values into cfg.
func loadTOML(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var f tomlFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse TOML: %w", err)
	}
	if f.Backend.Type != "" {
		cfg.Backend = backend.Type(f.Backend.Type)
	}
	op := f.Backend.OnePassword
	if op.Vault != "" {
		cfg.OnePassword.Vault = op.Vault
	}
	if op.ItemPrefix != "" {
		cfg.OnePassword.ItemPrefix = op.ItemPrefix
	}
	if op.Account != "" {
		cfg.OnePassword.Account = op.Account
	}
	if op.ConnectHost != "" {
		cfg.OnePassword.ConnectHost = op.ConnectHost
	}
	if op.ConnectTokenKeychainService != "" {
		cfg.OnePassword.ConnectTokenKeychainService = op.ConnectTokenKeychainService
	}
	if op.ConnectTokenKeychainAccount != "" {
		cfg.OnePassword.ConnectTokenKeychainAccount = op.ConnectTokenKeychainAccount
	}
	if op.CFAccessClientIDService != "" {
		cfg.OnePassword.CFAccessClientIDService = op.CFAccessClientIDService
	}
	if op.CFAccessClientSecretService != "" {
		cfg.OnePassword.CFAccessClientSecretService = op.CFAccessClientSecretService
	}
	if f.Backend.Vault.Addr != "" {
		cfg.Vault.Addr = f.Backend.Vault.Addr
	}
	if f.Backend.Vault.Path != "" {
		cfg.Vault.Path = f.Backend.Vault.Path
	}
	if f.Backend.Vault.Token != "" {
		cfg.Vault.Token = f.Backend.Vault.Token
	}
	if f.Sync.Interval > 0 {
		cfg.Sync.Interval = time.Duration(f.Sync.Interval) * time.Second
	}
	if f.Refresh.ExpiryBufferMinutes > 0 {
		cfg.Refresh.ExpiryBuffer = time.Duration(f.Refresh.ExpiryBufferMinutes) * time.Minute
	}
	return nil
}
