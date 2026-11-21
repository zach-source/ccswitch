package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/codex"
	"github.com/zach-source/ccswitch/internal/provider"
	"golang.org/x/term"
)

// codexSpec wires Codex into the generic provider commands. Only the parts
// that are Codex alone live here: the file-backed check and add-key.
var codexSpec = providerSpec{
	New: codexProvider,
	Long: "Manage multiple Codex CLI logins.\n\n" +
		"Codex keeps one login at a time in $CODEX_HOME/auth.json (default ~/.codex).\n" +
		"These commands capture that file into a named profile stored in the same\n" +
		"ccswitch backend as your Claude accounts, and restore it on demand.\n\n" +
		"Codex refreshes the live login itself. ccswitch refreshes the saved profiles\n" +
		"that are not live. It saves and restores the file whole, so a schema change\n" +
		"upstream cannot corrupt a profile.\n\n" +
		"  ccswitch codex add work     # capture the current login as \"work\"\n" +
		"  ccswitch codex use personal # make \"personal\" the live login\n" +
		"  ccswitch codex list",
	LoginShort: "Sign in a ChatGPT account and save it as a Codex profile",
	LoginLong: "Sign in a ChatGPT account in a temporary CODEX_HOME and save the login as a\n" +
		"profile. The live login is not changed. The sign-in address opens in the\n" +
		"Chrome profile signed in to <email>, or prints when none matches.",
	Store: func() (backend.Backend, error) { return codexStoreFunc() },
	Unreadable: func() string {
		if ok, store := codex.IsFileBacked(); !ok {
			return fmt.Sprintf("Codex is keyring-backed (cli_auth_credentials_store = %q); ccswitch cannot read it.", store)
		}
		return ""
	},
	Extra: func() []*cobra.Command { return []*cobra.Command{newCodexAddKeyCmd()} },
}

// codexRegistryPath returns the canonical path for the Codex profile
// registry, alongside sequence.json.
func codexRegistryPath() string {
	return filepath.Join(backupDir(), "codex.json")
}

// codexProvider is Codex behind the provider seam.
func codexProvider() provider.Provider {
	return codex.Provider{
		RegistryFile: codexRegistryPath(),
		LoginLock:    filepath.Join(backupDir(), "codex-login.lock"),
	}
}

// codexStore resolves the backend holding Codex profile credentials, after
// checking that Codex is file-backed at all.
//
// The keyring check is a refusal rather than a warning: with the credential
// in a keychain item keyed by a hash of the CODEX_HOME path, auth.json is
// stale or absent, so a capture would silently save the wrong thing and a
// restore would silently do nothing.
func codexStore() (backend.Backend, error) {
	if ok, store := codex.IsFileBacked(); !ok {
		return nil, fmt.Errorf(
			"Codex is configured with cli_auth_credentials_store = %q, so its login is in the OS keyring, not %s.\n"+
				"ccswitch can only swap the file. Set cli_auth_credentials_store = \"file\" in %s and log in again to use these commands",
			store, codex.AuthPath(), filepath.Join(codex.Home(), "config.toml"))
	}
	return defaultStore()
}

// codexStoreFunc resolves the Codex store. Tests replace it.
var codexStoreFunc = codexStore

func newCodexAddKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-key <label>",
		Short: "Save an OpenAI API key as a Codex profile",
		Long: "Read an OpenAI API key from stdin and save it as a profile for CI and\n" +
			"scripts. Codex writes the login in a temporary CODEX_HOME. The live login\n" +
			"is not changed. Run `ccswitch codex use <label>` to make it live.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			key, err := readAPIKey(os.Stdin, os.Stderr)
			if err != nil {
				return err
			}
			b, err := codexStoreFunc()
			if err != nil {
				return err
			}
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			blob, err := codex.SaveAPIKey(cmd.Context(), key)
			if err != nil {
				return err
			}
			id := codex.Describe(blob)
			if id.Mode != "apikey" {
				return errors.New("codex did not write an API key login — nothing was saved")
			}
			if err := b.Write(cmd.Context(), codex.CredKey(label), blob); err != nil {
				return fmt.Errorf("save Codex credential: %w", err)
			}
			reg.Profiles[label] = codex.Profile{Label: label, Mode: id.Mode}
			if !slices.Contains(reg.Priority, label) {
				reg.Priority = append(reg.Priority, label)
			}
			if err := reg.Save(codexRegistryPath()); err != nil {
				return err
			}
			fmt.Printf("Saved Codex profile %q: API key\n", label)
			return nil
		},
	}
}

// readAPIKey reads the key from in. On a terminal it prompts on prompt and
// reads without echo. The key is never an argument, so shell history misses it.
func readAPIKey(in *os.File, prompt io.Writer) (string, error) {
	var raw []byte
	var err error
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprint(prompt, "OpenAI API key: ")
		raw, err = term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(prompt)
	} else {
		raw, err = io.ReadAll(in)
	}
	if err != nil {
		return "", fmt.Errorf("read API key: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", errors.New("the API key is empty")
	}
	return key, nil
}
