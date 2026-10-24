package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/browser"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newTokenCmd)
}

// tokenLife is how long `claude setup-token` tokens last, about.
const tokenLife = 365 * 24 * time.Hour

const tokenPrefix = "sk-ant-oat01-"

var (
	ansiRe     = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][A-Za-z0-9]|\x1b[=>]`)
	tokenRunRe = regexp.MustCompile(`sk-ant-oat01-[A-Za-z0-9_\-]*(?:[ \t]*\n[A-Za-z0-9_\-]+)*`)
)

// parseSetupToken finds the token in the typescript of `claude setup-token`.
// It removes ANSI codes and carriage returns, then joins a token that the TUI
// wrapped over lines. A blank line ends the token. It returns the longest
// token found, or "" when there is none.
func parseSetupToken(out string) string {
	clean := strings.ReplaceAll(ansiRe.ReplaceAllString(out, ""), "\r", "")
	best := ""
	for _, run := range tokenRunRe.FindAllString(clean, -1) {
		joined := strings.Join(strings.Fields(run), "")
		// A redraw can put two tokens on adjacent lines; split them again.
		for _, part := range strings.Split(joined, tokenPrefix)[1:] {
			if tok := tokenPrefix + part; len(tok) > len(best) {
				best = tok
			}
		}
	}
	return best
}

// scriptArgs builds the script(1) arguments that run `claude setup-token`
// under a pty and record the session in typescript. BSD (macOS) and
// util-linux script take different forms.
func scriptArgs(goos, typescript string) []string {
	if goos == "linux" {
		return []string{"-q", "-c", "claude setup-token", typescript}
	}
	return []string{"-q", typescript, "claude", "setup-token"}
}

// runSetupToken runs `claude setup-token` under script(1), so the TUI gets a
// real pty and the user can see it and answer it. shimDir, when set, goes
// first on PATH so the sign-in opens in the right Chrome profile. The
// recording sits in a private temp dir that is always removed.
func runSetupToken(ctx context.Context, shimDir string) (string, error) {
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		return "", errors.New("script(1) is not on PATH; it gives claude setup-token a terminal")
	}
	tmp, err := os.MkdirTemp("", "ccswitch-token-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	typescript := filepath.Join(tmp, "typescript")
	// A private CLAUDE_CONFIG_DIR keeps setup-token away from the live login,
	// the same as `ccswitch login`. The seed skips onboarding.
	configDir := filepath.Join(tmp, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(configDir, ".claude.json"), []byte(`{"hasCompletedOnboarding":true}`), 0o600); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, scriptPath, scriptArgs(runtime.GOOS, typescript)...)
	cmd.Dir = tmp
	cmd.Env = append(setupTokenEnv(), "CLAUDE_CONFIG_DIR="+configDir)
	if shimDir != "" {
		cmd.Env = append(cmd.Env, "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()

	data, _ := os.ReadFile(typescript)
	if tok := parseSetupToken(string(data)); tok != "" {
		return tok, nil
	}
	if runErr != nil {
		return "", fmt.Errorf("claude setup-token failed: %w", runErr)
	}
	return "", errors.New("claude setup-token printed no token")
}

// setupTokenEnv is the environment without the variables that select a
// Claude login, so setup-token signs in from the browser.
func setupTokenEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		switch key, _, _ := strings.Cut(kv, "="); key {
		case "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_SCOPES", "ANTHROPIC_API_KEY":
		default:
			out = append(out, kv)
		}
	}
	return out
}

// setupTokenRunner runs the sign-in and returns the token. Tests replace it.
var setupTokenRunner = runSetupToken

// savedToken is the stored form of a long-lived token.
type savedToken struct {
	Token     string `json:"token"`
	CreatedAt string `json:"createdAt"`
}

func tokenKey(email string) string { return "Claude-Token-" + email }

func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Make and keep long-lived Claude tokens for scripts and CI",
	}
	cmd.AddCommand(newTokenCreateCmd(), newTokenGetCmd(), newTokenListCmd(), newTokenRemoveCmd())
	return cmd
}

// tokenTarget resolves an email or index the way switch-to does and opens
// the configured backend.
func tokenTarget(ident string) (string, backend.Backend, error) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return "", nil, err
	}
	seq, err := account.LoadSequence(sequencePath())
	if err != nil {
		return "", nil, err
	}
	id := seq.Resolve(ident)
	if id == "" {
		return "", nil, fmt.Errorf("no account found matching: %s", ident)
	}
	b, err := resolveBackend(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("backend not available: %w", err)
	}
	return seq.Accounts[id].Email, b, nil
}

func readSavedToken(ctx context.Context, b backend.Backend, email string) (savedToken, error) {
	var st savedToken
	data, err := b.Read(ctx, tokenKey(email))
	if errors.Is(err, backend.ErrNotFound) {
		return st, fmt.Errorf("no saved token for %s (run `ccswitch token create %s`)", email, email)
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil || st.Token == "" {
		return st, fmt.Errorf("saved token for %s is unreadable", email)
	}
	return st, nil
}

func newTokenCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <email|N>",
		Short: "Sign in an account in its Chrome profile and save a long-lived token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email, b, err := tokenTarget(args[0])
			if err != nil {
				return err
			}
			shimDir, cleanup := tokenOpenerShim(email)
			defer cleanup()

			tok, err := setupTokenRunner(cmd.Context(), shimDir)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			data, err := json.Marshal(savedToken{Token: tok, CreatedAt: now.Format(time.RFC3339)})
			if err != nil {
				return err
			}
			if err := b.Write(cmd.Context(), tokenKey(email), data); err != nil {
				return fmt.Errorf("save token: %w", err)
			}
			fmt.Printf("Saved a long-lived token for %s (expires about %s)\n",
				email, now.Add(tokenLife).Format("2006-01-02"))
			return nil
		},
	}
}

// tokenOpenerShim installs the Chrome `open` shim for email, the same one
// `ccswitch login` uses. It returns the shim directory ("" when there is no
// matching profile, so the default browser opens) and a cleanup function.
func tokenOpenerShim(email string) (string, func()) {
	noop := func() {}
	profile, ok, err := browser.FindByEmail(email)
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "  (no Chrome profile found for %s; using the default browser)\n", email)
		return "", noop
	}
	tmp, err := os.MkdirTemp("", "ccswitch-token-bin-")
	if err != nil {
		return "", noop
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := browser.InstallOpener(tmp, profile.Directory); err != nil {
		fmt.Fprintf(os.Stderr, "  (could not install Chrome opener shim: %v)\n", err)
		cleanup()
		return "", noop
	}
	fmt.Printf("  Using Chrome profile: %s (%s)\n", profile.Directory, profile.UserName)
	return tmp, cleanup
}

func newTokenGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <email|N>",
		Short: "Print the saved token, for CLAUDE_CODE_OAUTH_TOKEN=$(ccswitch token get x)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email, b, err := tokenTarget(args[0])
			if err != nil {
				return err
			}
			st, err := readSavedToken(cmd.Context(), b, email)
			if err != nil {
				return err
			}
			fmt.Println(st.Token)
			return nil
		},
	}
}

func newTokenListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved tokens with their age (no token text)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			b, err := resolveBackend(cfg)
			if err != nil {
				return fmt.Errorf("backend not available: %w", err)
			}
			found := false
			for _, id := range seq.Sequence {
				email := seq.Accounts[id].Email
				st, err := readSavedToken(cmd.Context(), b, email)
				if err != nil {
					continue
				}
				found = true
				fmt.Println(tokenLine(email, st, time.Now()))
			}
			if !found {
				fmt.Println("No saved tokens.")
			}
			return nil
		},
	}
}

// tokenLine formats one list row: email, created date, age.
func tokenLine(email string, st savedToken, now time.Time) string {
	created, err := time.Parse(time.RFC3339, st.CreatedAt)
	if err != nil {
		return fmt.Sprintf("%s  created unknown", email)
	}
	days := int(now.Sub(created).Hours() / 24)
	return fmt.Sprintf("%s  created %s  age %dd", email, created.Format("2006-01-02"), days)
}

func newTokenRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <email|N>",
		Short: "Delete the saved token from the store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email, b, err := tokenTarget(args[0])
			if err != nil {
				return err
			}
			if err := b.Delete(cmd.Context(), tokenKey(email)); err != nil {
				return err
			}
			fmt.Printf("Removed the saved token for %s\n", email)
			return nil
		},
	}
}
