package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newSetupOpVaultCmd)
}

const (
	// defaultOpVaultName is the vault created when none is named.
	defaultOpVaultName = "ccswitch"

	// opVaultPermissions is the least privilege ccswitch needs: it reads
	// credentials, and writes them on refresh, switch, save and push. It
	// never shares items, so share_items is deliberately absent.
	opVaultPermissions = "read_items,write_items"
)

func newSetupOpVaultCmd() *cobra.Command {
	var vaultName, saName, expiresIn string
	var assumeYes, printOnly bool

	cmd := &cobra.Command{
		Use:   "setup-op-vault",
		Short: "Create a dedicated 1Password vault and print the service-account setup for headless access",
		Long: "Create a dedicated 1Password vault for ccswitch and print the commands that\n" +
			"grant a service account access to it.\n\n" +
			"Two problems this solves at once. A service account cannot be granted access\n" +
			"to a Private or Personal vault — 1Password forbids it — so headless access\n" +
			"requires a separate vault whether you wanted segmentation or not. And a\n" +
			"service-account token authenticates with no biometric prompt, no desktop app,\n" +
			"and no interactive session, which is what a launchd/systemd daemon needs.\n\n" +
			"The vault is created here because that is additive and reversible. The service\n" +
			"account is not: 1Password returns its token exactly once, and minting one needs\n" +
			"owner or admin rights. So the command is printed for you to run and capture,\n" +
			"rather than executed with the token passing through ccswitch.",
		RunE: func(cmd *cobra.Command, args []string) error {
			opBin, err := exec.LookPath("op")
			if err != nil {
				return fmt.Errorf("op CLI not installed — `brew install 1password-cli`: %w", err)
			}
			cfg, _ := config.Load(config.DefaultPath())

			if vaultName == "" {
				vaultName = cfg.OnePassword.Vault
			}
			if vaultName == "" || isReservedOpVault(vaultName) {
				if isReservedOpVault(vaultName) {
					fmt.Printf("The configured vault %q cannot be used: 1Password does not let a service\n"+
						"account access a Private or Personal vault. Using %q instead.\n\n",
						vaultName, defaultOpVaultName)
				}
				vaultName = defaultOpVaultName
			}
			if saName == "" {
				saName = defaultServiceAccountName()
			}

			ctx := cmd.Context()
			if printOnly {
				writeServiceAccountGuide(os.Stdout, vaultName, saName, expiresIn, cfg.OnePassword.Account)
				return nil
			}

			exists, err := opVaultExists(ctx, opBin, cfg.OnePassword.Account, vaultName)
			if err != nil {
				return err
			}
			switch {
			case exists:
				fmt.Printf("Vault %q already exists — leaving it as it is.\n", vaultName)
			default:
				if !assumeYes && !confirm(fmt.Sprintf("Create 1Password vault %q? [y/N] ", vaultName)) {
					fmt.Println("Cancelled.")
					return nil
				}
				if err := opVaultCreate(ctx, opBin, cfg.OnePassword.Account, vaultName); err != nil {
					return err
				}
				fmt.Printf("Created 1Password vault %q.\n", vaultName)
			}

			if err := patchTOMLOpVault(config.DefaultPath(), vaultName); err != nil {
				fmt.Fprintf(os.Stderr,
					"Warning: could not auto-update TOML: %v. Set backend.onepassword.vault = %q in %s manually.\n",
					err, vaultName, config.DefaultPath())
			} else {
				fmt.Printf("Pointed ccswitch at it (%s).\n", config.DefaultPath())
			}

			fmt.Println()
			writeServiceAccountGuide(os.Stdout, vaultName, saName, expiresIn, cfg.OnePassword.Account)
			return nil
		},
	}
	cmd.Flags().StringVar(&vaultName, "vault", "", "Vault name to create (default: the configured vault, else \"ccswitch\")")
	cmd.Flags().StringVar(&saName, "service-account", "", "Service account name to suggest (default: ccswitch-<hostname>)")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "90d", "Suggested service-account lifetime for the printed command (empty for no expiry)")
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "Do not prompt before creating the vault")
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "Print the service-account instructions without touching 1Password")
	return cmd
}

// isReservedOpVault reports whether name is a vault 1Password refuses to
// share with a service account. Both spellings exist across account types.
func isReservedOpVault(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "private", "personal":
		return true
	}
	return false
}

// defaultServiceAccountName suggests a name that says which machine the
// token belongs to, so revoking one host does not mean guessing.
func defaultServiceAccountName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "ccswitch"
	}
	// Trim the mDNS suffix macOS appends; it adds nothing to the name.
	return "ccswitch-" + strings.TrimSuffix(host, ".local")
}

// confirm asks a yes/no question on stdin, defaulting to no.
func confirm(prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	ans := strings.TrimSpace(scanner.Text())
	return ans == "y" || ans == "Y"
}

// opAccountArgs prepends --account when one is configured. Global op flags
// must precede the subcommand.
func opAccountArgs(account string, args ...string) []string {
	if account == "" {
		return args
	}
	return append([]string{"--account", account}, args...)
}

// opVaultExists reports whether a vault by that name is already visible to
// the signed-in account.
func opVaultExists(ctx context.Context, opBin, account, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, opBin, opAccountArgs(account, "vault", "get", name, "--format", "json")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.ToLower(stderr.String())
		if strings.Contains(msg, "no vault") || strings.Contains(msg, "not found") ||
			strings.Contains(msg, "isn't a vault") || strings.Contains(msg, "doesn't seem to be a vault") {
			return false, nil
		}
		// Anything else — not signed in, no network, no permission — is a
		// real failure. Reporting it as "absent" would send the caller into
		// a create that fails just as hard, with a worse message.
		return false, fmt.Errorf("op vault get %q: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return true, nil
}

// opVaultCreate creates the vault, tagging it so its purpose is obvious in
// the 1Password UI months from now.
func opVaultCreate(ctx context.Context, opBin, account, name string) error {
	cmd := exec.CommandContext(ctx, opBin, opAccountArgs(account,
		"vault", "create", name,
		"--description", "Claude Code + Codex credentials managed by ccswitch",
		"--icon", "id-card",
		"--format", "json")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("op vault create %q: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// writeServiceAccountGuide prints the commands that grant headless access to
// the vault. It is a separate function taking an io.Writer so the wording can
// be tested without an op binary or a 1Password account.
func writeServiceAccountGuide(w io.Writer, vaultName, saName, expiresIn, account string) {
	fmt.Fprintf(w, serviceAccountGuide,
		serviceAccountCommand(saName, vaultName, expiresIn, account), opVaultPermissions)
}

// serviceAccountGuide takes the create command and the granted permissions.
const serviceAccountGuide = `Headless access via a service account
─────────────────────────────────────

A service-account token authenticates with no biometric prompt and no desktop
app, which is what a daemon or CI runner needs. Run this as a 1Password owner
or admin — it needs rights a service account does not have:

    %s

1Password prints the token once and never again. Put it straight into the
environment of whatever runs ccswitch — not into a shell history, a dotfile,
or this terminal's scrollback:

    export OP_SERVICE_ACCOUNT_TOKEN="ops_…"
    ccswitch backend        # should report 1password without prompting

    # launchd: put it in the plist's EnvironmentVariables
    # systemd: EnvironmentFile=/etc/ccswitch.env, mode 0600, root-owned

Populate the vault from this machine once, while still signed in as yourself:

    ccswitch push

Worth knowing
  • Service accounts cannot be granted Private or Personal vaults. That is the
    reason for a dedicated vault, beyond wanting the segmentation.
  • They need a 1Password Business or Teams plan.
  • The grant is %s on that one vault and nothing else, so the
    token cannot reach your other vaults. It can reach everything in this one,
    though, and so can anything that reads the process environment — scope the
    host accordingly.
  • Revoke from the 1Password admin console; rotate by creating a second
    account, swapping the token, then deleting the first.
`

// serviceAccountCommand renders the `op service-account create` invocation.
func serviceAccountCommand(saName, vaultName, expiresIn, account string) string {
	var b strings.Builder
	b.WriteString("op ")
	if account != "" {
		fmt.Fprintf(&b, "--account %s ", shellQuote(account))
	}
	fmt.Fprintf(&b, "service-account create %s \\\n", shellQuote(saName))
	fmt.Fprintf(&b, "        --vault %s", shellQuote(vaultName+":"+opVaultPermissions))
	if expiresIn != "" {
		fmt.Fprintf(&b, " \\\n        --expires-in %s", shellQuote(expiresIn))
	}
	return b.String()
}

// shellQuote wraps a value in double quotes when it is not a bare word, so
// the printed command survives a copy-paste of a vault name with a space.
func shellQuote(s string) string {
	const bare = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:,/"
	quoted := `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	if s == "" {
		return quoted
	}
	for _, r := range s {
		if !strings.ContainsRune(bare, r) {
			return quoted
		}
	}
	return s
}

// patchTOMLOpVault sets backend.onepassword.vault, leaving every other key
// in the file untouched.
func patchTOMLOpVault(path, vaultName string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(data, &existing)
	}

	backendBlock, _ := existing["backend"].(map[string]any)
	if backendBlock == nil {
		backendBlock = map[string]any{}
	}
	opBlock, _ := backendBlock["onepassword"].(map[string]any)
	if opBlock == nil {
		opBlock = map[string]any{}
	}
	opBlock["vault"] = vaultName
	backendBlock["onepassword"] = opBlock
	existing["backend"] = backendBlock

	data, err := toml.Marshal(existing)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
