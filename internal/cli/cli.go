// Package cli wires the cobra command tree. The top-level Execute() is
// the entry point used by cmd/ccswitch/main.go.
package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Execute runs the root cobra command, after rewriting any legacy
// double-dashed subcommand invocation (`ccswitch --switch-to 2`) into the
// cobra-native form (`ccswitch switch-to 2`).
func Execute() error {
	root := Root()
	root.SetArgs(normalizeLegacyArgs(os.Args[1:], root))
	return root.Execute()
}

// Root returns the root *cobra.Command. Subcommands are registered in
// their respective files in this package.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "ccswitch",
		Short: "Multi-account switcher for Claude Code",
		Long: "ccswitch manages multiple Claude Code OAuth credential sets " +
			"across macOS Keychain, files, 1Password (Connect HTTP + optional " +
			"Cloudflare Access), or HashiCorp Vault, and keeps them in sync " +
			"across devices.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	registerSubcommands(root)
	return root
}

// normalizeLegacyArgs rewrites a leading `--subcommand` token into the
// bare `subcommand` form when it names a registered subcommand. This keeps
// the original ccswitch.sh flag interface (`--switch-to`, `--sync`, `--env`,
// …) working for callers — the launchd daemon, the granted skill, shell
// integrations — that have not migrated to cobra-native syntax.
//
// Only the first argument is rewritten, and only when it resolves to a real
// subcommand, so genuine flags like `--help` are left untouched.
func normalizeLegacyArgs(args []string, root *cobra.Command) []string {
	if len(args) == 0 || !strings.HasPrefix(args[0], "--") {
		return args
	}
	name := strings.TrimPrefix(args[0], "--")
	for _, sub := range root.Commands() {
		if sub.Name() == name {
			out := make([]string, len(args))
			copy(out, args)
			out[0] = name
			return out
		}
	}
	return args
}

// registerSubcommands is implemented via the builder registry below and is a
// single extension point so tests can probe the command tree.
func registerSubcommands(root *cobra.Command) {
	// Each subcommand file (add_account.go, switch.go, sync.go, ...)
	// appends its own *cobra.Command via this registry pattern.
	for _, builder := range subcommandBuilders {
		root.AddCommand(builder())
	}
}

// subcommandBuilders is populated by init() functions in the
// command-specific files in this package.
var subcommandBuilders []func() *cobra.Command
