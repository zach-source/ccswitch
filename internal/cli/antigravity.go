package cli

import (
	"path/filepath"

	"github.com/zach-source/ccswitch/internal/antigravity"
	"github.com/zach-source/ccswitch/internal/provider"
)

// antigravitySpec wires agy, the Antigravity CLI, into the generic provider
// commands. Nothing here is specific to it beyond the texts.
var antigravitySpec = providerSpec{
	New:     antigravityProvider,
	Aliases: []string{"agy"},
	Long: "Manage multiple Antigravity CLI (agy) logins.\n\n" +
		"agy keeps one login at a time in the macOS keychain (service gemini,\n" +
		"account antigravity). These commands capture that login into a named\n" +
		"profile stored in the same ccswitch backend as your Claude accounts, and\n" +
		"restore it on demand. Usage of every profile comes from `agy -p /usage`,\n" +
		"run against a copy of the profile in a temporary keychain.\n\n" +
		"  ccswitch agy add work     # capture the current login as \"work\"\n" +
		"  ccswitch agy use personal # make \"personal\" the live login\n" +
		"  ccswitch agy list",
	LoginShort: "Sign in a Google account in agy and save it as an Antigravity profile",
	LoginLong: "Run agy in a temporary home with its own keychain, so you sign in there and\n" +
		"the live login is not changed. Sign in, then exit agy (Ctrl-C or /exit).\n" +
		"ccswitch checks that the account is <email> and saves the login.",
}

// antigravityProvider is agy behind the provider seam.
func antigravityProvider() provider.Provider {
	return antigravity.Provider{RegistryFile: filepath.Join(backupDir(), "antigravity.json")}
}
