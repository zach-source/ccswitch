package cli

import (
	"os"
	"path/filepath"

	"github.com/zach-source/ccswitch/internal/grok"
	"github.com/zach-source/ccswitch/internal/provider"
)

// grokSpec wires Grok into the generic provider commands. Grok has no
// keyring mode and no extra commands, so only the texts and the API-key
// warning are Grok alone.
var grokSpec = providerSpec{
	New: grokProvider,
	Long: "Manage multiple Grok Build CLI logins.\n\n" +
		"Grok keeps one login at a time in $GROK_HOME/auth.json (default ~/.grok).\n" +
		"These commands capture that file into a named profile stored in the same\n" +
		"ccswitch backend as your Claude accounts, and restore it on demand.\n\n" +
		"Grok renews the live login itself. ccswitch renews the saved profiles that\n" +
		"are not live.\n\n" +
		"  ccswitch grok add work     # capture the current login as \"work\"\n" +
		"  ccswitch grok use personal # make \"personal\" the live login\n" +
		"  ccswitch grok list",
	LoginShort: "Sign in an x.ai account and save it as a Grok profile",
	LoginLong: "Sign in an x.ai account with the device code flow in a temporary GROK_HOME\n" +
		"and save the login as a profile. The live login is not changed. The sign-in\n" +
		"address opens in the Chrome profile signed in to <email>, or prints when\n" +
		"none matches.",
	Warn: func() string {
		if os.Getenv("XAI_API_KEY") != "" {
			return "XAI_API_KEY is set. Grok uses it instead of the saved login, so a switch has no effect until you unset it."
		}
		return ""
	},
}

// grokProvider is Grok behind the provider seam.
func grokProvider() provider.Provider {
	return grok.Provider{RegistryFile: filepath.Join(backupDir(), "grok.json")}
}
