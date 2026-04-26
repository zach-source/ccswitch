// Command ccswitch manages multiple Claude Code OAuth credential sets
// across machines via macOS Keychain, files, 1Password (Connect HTTP +
// optional Cloudflare Access), or HashiCorp Vault.
package main

import (
	"fmt"
	"os"

	"github.com/zach-source/ccswitch/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
