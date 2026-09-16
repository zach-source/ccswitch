package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/codex"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newCodexCmd)
}

// codexRegistryPath returns the canonical path for the Codex profile
// registry, alongside sequence.json.
func codexRegistryPath() string {
	return filepath.Join(backupDir(), "codex.json")
}

func newCodexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Manage multiple Codex CLI logins",
		Long: "Manage multiple Codex CLI logins.\n\n" +
			"Codex keeps one login at a time in $CODEX_HOME/auth.json (default ~/.codex).\n" +
			"These commands capture that file into a named profile stored in the same\n" +
			"ccswitch backend as your Claude accounts, and restore it on demand.\n\n" +
			"Codex refreshes its own tokens; ccswitch only ever saves and restores the\n" +
			"file whole, so a schema change upstream cannot corrupt a profile.\n\n" +
			"  ccswitch codex add work     # capture the current login as \"work\"\n" +
			"  ccswitch codex use personal # make \"personal\" the live login\n" +
			"  ccswitch codex list",
	}
	cmd.AddCommand(
		newCodexListCmd(),
		newCodexAddCmd(),
		newCodexUseCmd(),
		newCodexCurrentCmd(),
		newCodexRemoveCmd(),
	)
	return cmd
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
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, err
	}
	b, err := resolveBackend(cfg)
	if err != nil {
		return nil, fmt.Errorf("backend not available: %w", err)
	}
	return b, nil
}

func newCodexListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List captured Codex profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			if len(reg.Profiles) == 0 {
				fmt.Println("No Codex profiles captured yet. Run `ccswitch codex add <label>` while logged in.")
				return nil
			}
			fmt.Println("Codex profiles:")
			for _, label := range reg.Labels() {
				p := reg.Profiles[label]
				marker := ""
				if label == reg.Active {
					marker = " (active)"
				}
				fmt.Printf("  %-16s %s%s\n", label, describeProfile(p), marker)
			}
			return nil
		},
	}
}

// describeProfile renders a profile's captured identity for one table row.
func describeProfile(p codex.Profile) string {
	who := p.Email
	if who == "" {
		who = "unknown account"
	}
	detail := p.Plan
	if detail == "" {
		detail = p.Mode
	}
	if detail == "" {
		return who
	}
	return fmt.Sprintf("%s  [%s]", who, detail)
}

func newCodexAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <label>",
		Short: "Capture the current Codex login as a named profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			b, err := codexStore()
			if err != nil {
				return err
			}
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			data, err := codex.ReadLive()
			if errors.Is(err, codex.ErrNoLiveLogin) {
				return fmt.Errorf("no Codex login at %s — run `codex login` first", codex.AuthPath())
			}
			if err != nil {
				return err
			}

			if _, exists := reg.Profiles[label]; exists {
				fmt.Printf("Replacing the stored credential for profile %q.\n", label)
			}
			if err := b.Write(cmd.Context(), codex.CredKey(label), data); err != nil {
				return fmt.Errorf("save Codex credential: %w", err)
			}

			id := codex.Describe(data)
			reg.Profiles[label] = codex.Profile{
				Label: label, Mode: id.Mode, Email: id.Email,
				Plan: id.Plan, AccountID: id.AccountID,
			}
			// The captured login is the live one by definition.
			reg.Active = label
			if err := reg.Save(codexRegistryPath()); err != nil {
				return err
			}
			fmt.Printf("Captured Codex profile %q: %s\n", label, describeProfile(reg.Profiles[label]))
			return nil
		},
	}
}

func newCodexUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <label>",
		Short: "Make a captured Codex profile the live login",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			b, err := codexStore()
			if err != nil {
				return err
			}
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			if _, ok := reg.Profiles[label]; !ok {
				return fmt.Errorf("no Codex profile named %q — `ccswitch codex list` shows what is captured", label)
			}

			target, err := b.Read(cmd.Context(), codex.CredKey(label))
			if err != nil {
				return fmt.Errorf("read Codex profile %q: %w", label, err)
			}
			if len(target) == 0 {
				return fmt.Errorf("Codex profile %q holds no credential — re-capture it with `ccswitch codex add %s`", label, label)
			}

			// Snapshot whatever is live first. Codex rotates its own tokens,
			// so the live file is almost always newer than the copy captured
			// for that profile; overwriting without saving would roll it back
			// to a superseded token and force a re-login.
			snapshotLiveCodex(cmd.Context(), b, reg)

			if err := codex.WriteLive(target); err != nil {
				return err
			}
			reg.Active = label
			if err := reg.Save(codexRegistryPath()); err != nil {
				return err
			}
			fmt.Printf("Codex now using %q: %s\n", label, describeProfile(reg.Profiles[label]))
			return nil
		},
	}
}

// snapshotLiveCodex saves the live credential into whichever profile is
// currently marked active, so a switch never discards a rotated token.
// Best-effort: a failure here must not block the switch the user asked for,
// but it is reported rather than swallowed.
func snapshotLiveCodex(ctx context.Context, b backend.Backend, reg *codex.Registry) {
	prior := reg.Active
	if prior == "" {
		return
	}
	if _, known := reg.Profiles[prior]; !known {
		return
	}
	live, err := codex.ReadLive()
	if err != nil {
		return
	}
	if err := b.Write(ctx, codex.CredKey(prior), live); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not snapshot the live Codex login into %q: %v\n", prior, err)
		return
	}
	if id := codex.Describe(live); id.Email != "" {
		p := reg.Profiles[prior]
		p.Mode, p.Email, p.Plan, p.AccountID = id.Mode, id.Email, id.Plan, id.AccountID
		reg.Profiles[prior] = p
	}
}

func newCodexCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the live Codex login",
		RunE: func(cmd *cobra.Command, args []string) error {
			if ok, store := codex.IsFileBacked(); !ok {
				fmt.Printf("Codex is keyring-backed (cli_auth_credentials_store = %q); ccswitch cannot read it.\n", store)
				return nil
			}
			data, err := codex.ReadLive()
			if errors.Is(err, codex.ErrNoLiveLogin) {
				fmt.Printf("No Codex login at %s\n", codex.AuthPath())
				return nil
			}
			if err != nil {
				return err
			}
			id := codex.Describe(data)
			who := id.Email
			if who == "" {
				who = "unknown account"
			}
			fmt.Printf("%s", who)
			if id.Plan != "" {
				fmt.Printf("  [%s]", id.Plan)
			} else if id.Mode != "" {
				fmt.Printf("  [%s]", id.Mode)
			}
			fmt.Println()

			// Name the profile only when the stored identity still matches:
			// a `codex login` outside ccswitch leaves the marker pointing at
			// an account that is no longer live.
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err == nil && reg.Active != "" {
				if p, ok := reg.Profiles[reg.Active]; ok && p.Email == id.Email {
					fmt.Printf("Profile: %s\n", reg.Active)
				}
			}
			return nil
		},
	}
}

func newCodexRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <label>",
		Short: "Forget a captured Codex profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			if _, ok := reg.Profiles[label]; !ok {
				return fmt.Errorf("no Codex profile named %q", label)
			}

			cfg, cfgErr := config.Load(config.DefaultPath())
			if cfgErr == nil {
				if b, bErr := resolveBackend(cfg); bErr == nil {
					if delErr := b.Delete(cmd.Context(), codex.CredKey(label)); delErr != nil {
						fmt.Fprintf(os.Stderr, "Warning: could not delete the stored credential: %v\n", delErr)
					}
				}
			}

			delete(reg.Profiles, label)
			if reg.Active == label {
				reg.Active = ""
			}
			if err := reg.Save(codexRegistryPath()); err != nil {
				return err
			}
			fmt.Printf("Removed Codex profile %q. The live login at %s is untouched.\n", label, codex.AuthPath())
			return nil
		},
	}
}
