package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newUseZaiCmd)
	subcommandBuilders = append(subcommandBuilders, newUseAnthropicCmd)
	subcommandBuilders = append(subcommandBuilders, newAPIStatusCmd)
}

// z.ai endpoint configuration written into settings.json's env block.
const (
	zaiBaseURL   = "https://api.z.ai/api/anthropic"
	zaiTimeoutMS = "3000000" // 50 minutes
	// zaiOPRef is the 1Password secret reference for the z.ai API token,
	// read with `op read` as a fallback when the keychain cache is absent.
	zaiOPAccount = "S43LKCIJPNGYLE52ZXH2MM7LJA"
	zaiOPRef     = "op://Employee/bzdhsxie4x5emfkacyiwtyc6bi/credential"
)

func newUseZaiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use-zai",
		Short: "Point Claude Code at the z.ai API endpoint (writes settings.json env block)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Configuring Claude to use z.ai API...")

			token, err := fetchZaiToken()
			if err != nil {
				return err
			}

			path := settingsPath()
			settings, _, err := readSettings(path)
			if err != nil {
				return err
			}

			// Replace the env block wholesale, matching `.env = {...}` in
			// ccswitch.sh — z.ai mode owns all three of these keys.
			settings["env"] = map[string]any{
				"ANTHROPIC_AUTH_TOKEN": token,
				"ANTHROPIC_BASE_URL":   zaiBaseURL,
				"API_TIMEOUT_MS":       zaiTimeoutMS,
			}
			if err := writeSettings(path, settings); err != nil {
				return err
			}

			fmt.Println("✓ Configured Claude to use z.ai API")
			fmt.Printf("  Base URL: %s\n", zaiBaseURL)
			fmt.Printf("  Timeout:  %sms\n", zaiTimeoutMS)
			fmt.Println()
			fmt.Println("Restart Claude Code to use the new configuration.")
			return nil
		},
	}
}

func newUseAnthropicCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use-anthropic",
		Short: "Revert Claude Code to the default Anthropic API (removes settings.json env block)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := settingsPath()
			settings, exists, err := readSettings(path)
			if err != nil {
				return err
			}
			if !exists {
				fmt.Println("No settings.json found — already using the default Anthropic API.")
				return nil
			}
			if _, hasEnv := settings["env"]; !hasEnv {
				fmt.Println("Already using the default Anthropic API (no custom env block set).")
				return nil
			}

			fmt.Println("Removing custom API configuration and reverting to the default Anthropic API...")
			delete(settings, "env")
			if err := writeSettings(path, settings); err != nil {
				return err
			}

			fmt.Println("✓ Reverted to the default Anthropic API")
			fmt.Println()
			fmt.Println("Restart Claude Code to use the new configuration.")
			return nil
		},
	}
}

func newAPIStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api-status",
		Short: "Show whether Claude Code is using the default Anthropic API or a custom endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := settingsPath()
			settings, exists, err := readSettings(path)
			if err != nil {
				return err
			}
			if !exists {
				fmt.Println("API:      Default Anthropic API")
				fmt.Println("Settings: no settings.json")
				return nil
			}

			env, _ := settings["env"].(map[string]any)
			baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
			if baseURL == "" {
				fmt.Println("API: Default Anthropic API")
				return nil
			}

			fmt.Println("API: Custom endpoint")
			fmt.Printf("  Base URL: %s\n", baseURL)
			if strings.Contains(baseURL, "z.ai") {
				fmt.Println("  Provider: z.ai")
			}
			if timeout, _ := env["API_TIMEOUT_MS"].(string); timeout != "" {
				fmt.Printf("  Timeout:  %sms\n", timeout)
			}
			if token, _ := env["ANTHROPIC_AUTH_TOKEN"].(string); token != "" {
				fmt.Println("  Auth Token: ✓ configured")
			}
			return nil
		},
	}
}

// fetchZaiToken resolves the z.ai API token, mirroring ccswitch.sh: it first
// tries the keychain-backed mcp-secret-cache helper, then falls back to a
// direct `op read`. Returns a clear error if neither yields a token.
func fetchZaiToken() (string, error) {
	home, _ := os.UserHomeDir()
	cache := filepath.Join(home, ".claude", "scripts", "mcp-secret-cache.sh")
	if info, err := os.Stat(cache); err == nil && info.Mode()&0o111 != 0 {
		if out, err := exec.Command(cache, "get", "zai", "ZAI_API_TOKEN").Output(); err == nil {
			if tok := strings.TrimSpace(string(out)); tok != "" {
				return tok, nil
			}
		}
	}

	if _, err := exec.LookPath("op"); err == nil {
		if out, err := exec.Command("op", "read", "--account="+zaiOPAccount, zaiOPRef).Output(); err == nil {
			if tok := strings.TrimSpace(string(out)); tok != "" {
				return tok, nil
			}
		}
	}

	return "", fmt.Errorf("could not fetch z.ai token: ensure you are signed in to 1Password "+
		"(account %s) or that ~/.claude/scripts/mcp-secret-cache.sh has it cached", zaiOPAccount)
}
