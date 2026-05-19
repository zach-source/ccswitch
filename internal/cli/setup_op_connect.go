package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend/keychain"
	"github.com/zach-source/ccswitch/internal/config"
	"golang.org/x/term"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newSetupOpConnectCmd)
}

func newSetupOpConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup-op-connect",
		Short: "Interactive setup for 1Password Connect (URL + secrets → keychain/file)",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanner := bufio.NewScanner(os.Stdin)
			prompt := func(msg string) string {
				fmt.Print(msg)
				scanner.Scan()
				return strings.TrimSpace(scanner.Text())
			}

			cfg, _ := config.Load(config.DefaultPath())

			fmt.Println("Setting up 1Password Connect for ccswitch")
			fmt.Println()

			// URL prompt.
			currentHost := cfg.OnePassword.ConnectHost
			defaultHint := ""
			if currentHost != "" {
				defaultHint = fmt.Sprintf(" [%s]", currentHost)
			}
			connectHost := prompt(fmt.Sprintf("Connect server URL%s: ", defaultHint))
			if connectHost == "" {
				connectHost = currentHost
			}
			if connectHost == "" {
				return fmt.Errorf("Connect URL required")
			}
			if err := validateConnectHost(connectHost); err != nil {
				return err
			}

			// CF Access?
			fmt.Println()
			cfAns := prompt("Is the Connect server behind Cloudflare Access? [y/N]: ")
			wantCF := strings.EqualFold(cfAns, "y")

			// Source.
			fmt.Println()
			fmt.Println("Secret source:")
			fmt.Println("  1) Enter tokens manually")
			fmt.Println("  2) Read from 1Password references (via signed-in op CLI, one-time)")
			choice := prompt("Choose [1/2] (default 2): ")
			if choice == "" {
				choice = "2"
			}

			type secretSpec struct {
				label   string
				service string
				file    string
				cfOnly  bool
			}
			specs := []secretSpec{
				{"Connect bearer token", cfg.OnePassword.ConnectTokenKeychainService, "connect-token", false},
				{"CF Access client id", cfg.OnePassword.CFAccessClientIDService, "cf-access-client-id", true},
				{"CF Access client secret", cfg.OnePassword.CFAccessClientSecretService, "cf-access-client-secret", true},
			}

			if choice == "2" {
				// op read mode.
				if _, err := exec.LookPath("op"); err != nil {
					return fmt.Errorf("op CLI not installed; choose option 1 instead")
				}
				opAccount := prompt("1Password account shorthand (empty for default): ")
				var acctArgs []string
				if opAccount != "" {
					acctArgs = []string{"--account", opAccount}
				}

				refs := []string{
					"op://Personal Agents/op-connect-token/credential",
					"op://Personal Agents/op-connect-cf-access/client_id",
					"op://Personal Agents/op-connect-cf-access/client_secret",
				}
				labels := []string{
					"Reference for Connect bearer token",
					"Reference for CF Access client id",
					"Reference for CF Access client secret",
				}
				for i, spec := range specs {
					if spec.cfOnly && !wantCF {
						continue
					}
					ref := prompt(fmt.Sprintf("%s [%s]: ", labels[i], refs[i]))
					if ref == "" {
						ref = refs[i]
					}
					// op emits the secret on stdout; it never lands in argv.
					opArgs := append(acctArgs, "read", ref)
					opCmd := exec.CommandContext(cmd.Context(), "op", opArgs...)
					out, err := opCmd.Output()
					if err != nil {
						return fmt.Errorf("op read %s: %w", ref, err)
					}
					value := strings.TrimRight(string(out), "\n")
					if err := storeSecret(cmd.Context(), spec.service, spec.file, []byte(value)); err != nil {
						return fmt.Errorf("store %s: %w", spec.label, err)
					}
					fmt.Printf("Stored %s ✓\n", spec.label)
				}
			} else {
				// Manual entry via hidden prompt.
				for _, spec := range specs {
					if spec.cfOnly && !wantCF {
						continue
					}
					fmt.Printf("%s (hidden): ", spec.label)
					value, err := term.ReadPassword(int(os.Stdin.Fd()))
					fmt.Println()
					if err != nil {
						return fmt.Errorf("read %s: %w", spec.label, err)
					}
					if err := storeSecret(cmd.Context(), spec.service, spec.file, value); err != nil {
						return fmt.Errorf("store %s: %w", spec.label, err)
					}
					fmt.Printf("Stored %s ✓\n", spec.label)
				}
			}

			fmt.Println("Secrets stored.")

			// Update TOML config with connect_host.
			if err := patchTOMLConnectHost(config.DefaultPath(), connectHost,
				cfg.OnePassword.ConnectTokenKeychainService,
				cfg.OnePassword.ConnectTokenKeychainAccount); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not auto-update TOML: %v. Edit %s manually.\n",
					err, config.DefaultPath())
			}

			fmt.Println()
			fmt.Println("Setup complete. Run 'ccswitch config' to verify.")
			return nil
		},
	}
}

// storeSecret writes value to the macOS Keychain via the keychain backend
// (Security.framework — no argv exposure), or a 0600 file fallback on
// platforms where the keychain is unavailable.
//
// It deliberately does NOT shell out to `security add-generic-password -w`:
// that exposes the secret as a process argument visible to any local process
// via `ps`. The keychain backend's Read path uses the same service key and
// account, so writer and reader stay consistent.
func storeSecret(ctx context.Context, service, fileBasename string, value []byte) error {
	if err := keychain.New().Write(ctx, service, value); err != nil {
		if !errors.Is(err, keychain.ErrNotSupported) {
			return fmt.Errorf("keychain write: %w", err)
		}
		// Non-macOS: 0600 file fallback.
		home, _ := os.UserHomeDir()
		tokenFile := filepath.Join(home, ".config", "ccswitch", fileBasename)
		if err := os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
			return err
		}
		return os.WriteFile(tokenFile, value, 0o600)
	}
	return nil
}

// patchTOMLConnectHost updates or creates ~/.config/ccswitch/config.toml with
// the connect_host and related keychain service keys.
func patchTOMLConnectHost(path, host, service, kcAccount string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// Read existing or start fresh.
	type rawConfig map[string]any
	existing := make(rawConfig)
	if data, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(data, &existing)
	}

	// Ensure backend.type = "1password".
	backendBlock, _ := existing["backend"].(map[string]any)
	if backendBlock == nil {
		backendBlock = make(map[string]any)
	}
	backendBlock["type"] = "1password"

	// Ensure backend.onepassword block.
	opBlock, _ := backendBlock["onepassword"].(map[string]any)
	if opBlock == nil {
		opBlock = make(map[string]any)
	}
	opBlock["connect_host"] = host
	opBlock["connect_token_keychain_service"] = service
	opBlock["connect_token_keychain_account"] = kcAccount
	backendBlock["onepassword"] = opBlock
	existing["backend"] = backendBlock

	data, err := toml.Marshal(existing)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
