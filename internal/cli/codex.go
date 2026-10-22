package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/browser"
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
		newCodexLoginCmd(),
		newCodexPriorityCmd(),
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

func newCodexLoginCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "login <email>",
		Short: "Sign in a ChatGPT account and save it as a Codex profile",
		Long: "Sign in a ChatGPT account in a temporary CODEX_HOME and save the login as a\n" +
			"profile. The live login is not changed. The sign-in address opens in the\n" +
			"Chrome profile signed in to <email>, or prints when none matches.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			if label == "" {
				label = email
			}
			b, err := codexStoreFunc()
			if err != nil {
				return err
			}
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}

			blob, err := codex.SignIn(cmd.Context(), filepath.Join(backupDir(), "codex-login.lock"),
				func(d codex.DeviceLogin) { announceCodexLogin(email, d) })
			if err != nil {
				return err
			}
			id := codex.Describe(blob)
			if !strings.EqualFold(id.Email, email) {
				who := id.Email
				if who == "" {
					who = "an unknown account"
				}
				return fmt.Errorf("signed in as %s, not %s — nothing was saved", who, email)
			}
			if err := b.Write(cmd.Context(), codex.CredKey(label), blob); err != nil {
				return fmt.Errorf("save Codex credential: %w", err)
			}
			reg.Profiles[label] = codex.Profile{
				Label: label, Mode: id.Mode, Email: id.Email,
				Plan: id.Plan, AccountID: id.AccountID,
			}
			if !slices.Contains(reg.Priority, label) {
				reg.Priority = append(reg.Priority, label)
			}
			if err := reg.Save(codexRegistryPath()); err != nil {
				return err
			}
			fmt.Printf("Saved Codex profile %q: %s\n", label, describeProfile(reg.Profiles[label]))
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "profile label (default: the email)")
	return cmd
}

// announceCodexLogin tells the user how to finish the sign-in. It opens the
// address in the Chrome profile of email when one exists.
func announceCodexLogin(email string, d codex.DeviceLogin) {
	if d.BrowserFallback && d.Code == "" {
		fmt.Printf("Codex opened your default browser. Sign in as %s there.\n", email)
		return
	}
	fmt.Printf("One-time code: %s\n", d.Code)
	prof, found, err := browser.FindByEmail(email)
	if err == nil && found && browser.OpenURL(prof.Directory, d.URL) == nil {
		fmt.Printf("Opened %s in Chrome profile %q.\n", d.URL, prof.Name)
		return
	}
	fmt.Printf("No Chrome profile is signed in as %s. Open this address and sign in:\n  %s\n", email, d.URL)
}

func newCodexPriorityCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "priority [label...]",
		Short: "Show or set the order in which autoswitch tries Codex profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			if len(args) == 0 {
				for _, label := range reg.Ordered() {
					fmt.Println(label)
				}
				return nil
			}
			for _, label := range args {
				if _, ok := reg.Profiles[label]; !ok {
					return fmt.Errorf("no Codex profile named %q — `ccswitch codex list` shows what is saved", label)
				}
			}
			reg.Priority = args
			return reg.Save(codexRegistryPath())
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
			b, err := codexStoreFunc()
			if err != nil {
				return err
			}
			// The local backend holds the fallback copy. It stays nil when
			// it cannot be resolved; the store is then the only copy.
			local, localErr := resolveLocalBackend()
			if localErr != nil {
				local = nil
			}
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			if _, ok := reg.Profiles[label]; !ok {
				return fmt.Errorf("no Codex profile named %q — `ccswitch codex list` shows what is captured", label)
			}

			// Snapshot whatever is live first. Codex rotates its own tokens,
			// so the live file is almost always newer than the copy captured
			// for that profile; overwriting without saving would roll it back
			// to a superseded token and force a re-login. The snapshot runs
			// before the target is read so that switching to the profile
			// that is live now restores the rotated copy.
			if err := snapshotLiveCodex(cmd.Context(), b, local, reg); err != nil {
				return err
			}

			target, err := codexFresher(cmd.Context(), b, local, codex.CredKey(label))
			if err != nil {
				return fmt.Errorf("read Codex profile %q: %w", label, err)
			}
			if len(target) == 0 {
				return fmt.Errorf("Codex profile %q holds no credential — re-capture it with `ccswitch codex add %s`", label, label)
			}

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

// codexStoreFunc resolves the Codex store. Tests replace it.
var codexStoreFunc = codexStore

// codexFresher reads a profile from the store and from the local backend and
// returns the copy with the later last_refresh. A snapshot that could not
// reach the store sits in the local backend, so the store copy can be older.
// If only one copy is readable it wins; if neither is, the store's error
// returns. A nil local backend, or the store itself, needs only one read.
func codexFresher(ctx context.Context, store, local backend.Backend, key string) ([]byte, error) {
	fromStore, storeErr := store.Read(ctx, key)
	if local == nil || local == store {
		return fromStore, storeErr
	}
	fromLocal, localErr := local.Read(ctx, key)
	switch {
	case localErr != nil:
		return fromStore, storeErr
	case storeErr != nil:
		return fromLocal, nil
	}
	tStore, okStore := codex.LastRefresh(fromStore)
	tLocal, okLocal := codex.LastRefresh(fromLocal)
	if okLocal && (!okStore || tLocal.After(tStore)) {
		return fromLocal, nil
	}
	return fromStore, nil
}

// snapshotLiveCodex saves the live credential into the profile it belongs
// to, found by account identity. The active marker is not trusted: a Codex
// session that outlives a swap can write its own account back into the live
// file, and saving that under the active profile would file one account's
// login under another.
//
// A ChatGPT login that matches no profile is an error, so the caller stops
// and the live file stays the only copy. API-key logins do not rotate and a
// missing login has nothing to lose, so both return nil without a snapshot.
//
// If the store write fails, the same bytes go to the local backend and the
// switch continues with a warning. If that fails too, the live file is the
// only copy, so the caller must not overwrite it.
func snapshotLiveCodex(ctx context.Context, b, local backend.Backend, reg *codex.Registry) error {
	live, err := codex.ReadLive()
	if errors.Is(err, codex.ErrNoLiveLogin) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the live Codex login: %w", err)
	}
	id := codex.Describe(live)
	if id.Mode == "apikey" {
		return nil
	}
	label, ok := codex.MatchProfile(reg, live)
	if !ok {
		who := id.Email
		if who == "" {
			who = "an unknown account"
		}
		return fmt.Errorf("the live Codex login (%s) matches no saved profile, so switching would lose it — run `ccswitch codex add <label>` first", who)
	}
	key := codex.CredKey(label)
	if err := b.Write(ctx, key, live); err != nil {
		if local == nil {
			return fmt.Errorf("save the live Codex login into %q: %w (no local backend for a fallback copy)", label, err)
		}
		if lerr := local.Write(ctx, key, live); lerr != nil {
			return fmt.Errorf("save the live Codex login into %q: store: %v; local copy: %w", label, err, lerr)
		}
		fmt.Fprintf(os.Stderr, "Warning: could not save the live Codex login into %q in the store (%v); kept a local copy instead.\n", label, err)
	}
	if id.Email != "" {
		p := reg.Profiles[label]
		p.Mode, p.Email, p.Plan, p.AccountID = id.Mode, id.Email, id.Plan, id.AccountID
		reg.Profiles[label] = p
	}
	return nil
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
			reg.Priority = slices.DeleteFunc(reg.Priority, func(l string) bool { return l == label })
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
