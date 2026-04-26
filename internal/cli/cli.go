// Package cli wires the cobra command tree. The top-level Execute() is
// the entry point used by cmd/ccswitch/main.go.
package cli

import "github.com/spf13/cobra"

// Execute runs the root cobra command.
func Execute() error {
	return Root().Execute()
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

// registerSubcommands is implemented in subcommands.go and is a single
// extension point so tests can probe the command tree.
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
