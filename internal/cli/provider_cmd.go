package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/browser"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/provider"
)

// providerSpec wires one provider into the command layer. Only New is
// required; the other fields carry what one provider alone needs.
type providerSpec struct {
	New        func() provider.Provider
	Aliases    []string                        // extra names of the command
	Long       string                          // help text of the command
	LoginShort string                          // help of the login command
	LoginLong  string                          // long help of the login command
	Store      func() (backend.Backend, error) // default: defaultStore
	Unreadable func() string                   // non-empty: why the live login cannot be read
	Extra      func() []*cobra.Command         // commands of this provider only
	Warn       func() string                   // non-empty: a warning for `use` and `current`
}

// providerSpecs lists every provider. A new provider adds one line here.
var providerSpecs = []providerSpec{codexSpec, grokSpec}

func init() {
	for _, s := range providerSpecs {
		subcommandBuilders = append(subcommandBuilders, func() *cobra.Command { return providerCmd(s) })
	}
}

// titleCase is the display name of a provider: "codex" becomes "Codex".
func titleCase(name string) string { return strings.ToUpper(name[:1]) + name[1:] }

func title(p provider.Provider) string { return titleCase(p.Name()) }

// defaultStore resolves the backend that holds profile credentials.
func defaultStore() (backend.Backend, error) {
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

// warn prints the warning of the provider, when it has one, to stderr.
func (s providerSpec) warn() {
	if s.Warn == nil {
		return
	}
	if w := s.Warn(); w != "" {
		fmt.Fprintln(os.Stderr, "Warning: "+w)
	}
}

func (s providerSpec) store() (backend.Backend, error) {
	if s.Store != nil {
		return s.Store()
	}
	return defaultStore()
}

// localOrNil returns the backend for local copies, or nil when it cannot be
// resolved. The store is then the only copy.
func localOrNil() backend.Backend {
	local, err := providerLocalBackend()
	if err != nil {
		return nil
	}
	return local
}

func loadRegistry(p provider.Provider) (*provider.Registry, error) {
	return provider.LoadRegistry(p.RegistryPath())
}

// switchLock is the lock that keeps a switch, autoswitch and a renewal apart.
func switchLock(p provider.Provider) string { return p.Name() + "-autoswitch.lock" }

// profileLock is the lock that keeps two processes from spending one refresh token.
func profileLock(p provider.Provider, label string) string {
	return p.Name() + "-profile-" + strings.ReplaceAll(label, "/", "_") + ".lock"
}

// providerCmd builds `ccswitch <name> …` for one provider.
func providerCmd(s providerSpec) *cobra.Command {
	p := s.New()
	long := s.Long
	if long == "" {
		long = "Manage multiple " + title(p) + " CLI logins."
	}
	cmd := &cobra.Command{
		Use:     p.Name(),
		Aliases: s.Aliases,
		Short:   "Manage multiple " + title(p) + " CLI logins",
		Long:    long,
	}
	cmd.AddCommand(
		newProviderListCmd(s), newProviderAddCmd(s), newProviderUseCmd(s), newProviderCurrentCmd(s), newProviderRemoveCmd(s),
		newProviderLoginCmd(s), newProviderPriorityCmd(s), newProviderUsageCmd(s), newProviderAutoswitchCmd(s),
	)
	if s.Extra != nil {
		cmd.AddCommand(s.Extra()...)
	}
	return cmd
}

func newProviderListCmd(s providerSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List captured " + title(s.New()) + " profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			reg, err := loadRegistry(p)
			if err != nil {
				return err
			}
			if len(reg.Profiles) == 0 {
				fmt.Printf("No %s profiles captured yet. Run `ccswitch %s add <label>` while logged in.\n", title(p), p.Name())
				return nil
			}
			fmt.Printf("%s profiles:\n", title(p))
			for _, label := range reg.Labels() {
				marker := ""
				if label == reg.Active {
					marker = " (active)"
				}
				fmt.Printf("  %-16s %s%s\n", label, describeProfile(reg.Profiles[label]), marker)
			}
			return nil
		},
	}
}

// describeProfile renders a profile's captured identity for one table row.
func describeProfile(p provider.Profile) string {
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

// profileOf is the registry entry for a login.
func profileOf(label string, id provider.Identity) provider.Profile {
	return provider.Profile{Label: label, Mode: id.Mode, Email: id.Email, Plan: id.Plan, AccountID: id.AccountID}
}

func newProviderAddCmd(s providerSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "add <label>",
		Short: "Capture the current " + title(s.New()) + " login as a named profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			label := args[0]
			b, err := s.store()
			if err != nil {
				return err
			}
			reg, err := loadRegistry(p)
			if err != nil {
				return err
			}
			data, err := p.ReadLive()
			if errors.Is(err, provider.ErrNoLiveLogin) {
				return fmt.Errorf("no %s login at %s — run `%s login` first", title(p), p.LiveLocation(), p.Name())
			}
			if err != nil {
				return err
			}

			if _, exists := reg.Profiles[label]; exists {
				fmt.Printf("Replacing the stored credential for profile %q.\n", label)
			}
			if err := b.Write(cmd.Context(), p.CredKeyPrefix()+label, data); err != nil {
				return fmt.Errorf("save %s credential: %w", title(p), err)
			}

			reg.Profiles[label] = profileOf(label, p.Describe(data))
			// The captured login is the live one by definition.
			reg.Active = label
			if err := reg.Save(p.RegistryPath()); err != nil {
				return err
			}
			fmt.Printf("Captured %s profile %q: %s\n", title(p), label, describeProfile(reg.Profiles[label]))
			return nil
		},
	}
}

func newProviderLoginCmd(s providerSpec) *cobra.Command {
	var label string
	p := s.New()
	short, long := s.LoginShort, s.LoginLong
	if short == "" {
		short = "Sign in an account and save it as a " + title(p) + " profile"
	}
	if long == "" {
		long = short + ". The live login is not changed."
	}
	cmd := &cobra.Command{
		Use:   "login <email>",
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			email := args[0]
			if label == "" {
				label = email
			}
			b, err := s.store()
			if err != nil {
				return err
			}
			reg, err := loadRegistry(p)
			if err != nil {
				return err
			}

			if _, err := signInAndSave(cmd.Context(), p, b, reg, email, label, false); err != nil {
				return err
			}
			fmt.Printf("Saved %s profile %q: %s\n", title(p), label, describeProfile(reg.Profiles[label]))
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "profile label (default: the email)")
	return cmd
}

// signInAndSave signs in an account in a temporary home and saves the login
// as profile label. With manual set it prints the address and opens no
// browser. It checks that the login belongs to email and saves nothing when it
// does not. It saves reg and returns the new login.
func signInAndSave(ctx context.Context, p provider.Provider, b backend.Backend, reg *provider.Registry, email, label string, manual bool) ([]byte, error) {
	blob, err := p.Login(ctx, func(d provider.LoginPrompt) { announceLogin(p, email, d, manual) })
	if err != nil {
		return nil, err
	}
	id := p.Describe(blob)
	if !strings.EqualFold(id.Email, email) {
		who := id.Email
		if who == "" {
			who = "an unknown account"
		}
		return nil, fmt.Errorf("signed in as %s, not %s — nothing was saved", who, email)
	}
	if err := b.Write(ctx, p.CredKeyPrefix()+label, blob); err != nil {
		return nil, fmt.Errorf("save %s credential: %w", title(p), err)
	}
	reg.Profiles[label] = profileOf(label, id)
	if !slices.Contains(reg.Priority, label) {
		reg.Priority = append(reg.Priority, label)
	}
	if err := reg.Save(p.RegistryPath()); err != nil {
		return nil, err
	}
	return blob, nil
}

// announceLogin tells the user how to finish the sign-in. It opens the
// address in the Chrome profile of email when one exists, unless manual is set.
func announceLogin(p provider.Provider, email string, d provider.LoginPrompt, manual bool) {
	if d.Browser && d.Code == "" {
		fmt.Printf("%s opened your default browser. Sign in as %s there.\n", title(p), email)
		return
	}
	fmt.Printf("One-time code: %s\n", d.Code)
	if manual {
		fmt.Printf("Open this address on any device and sign in as %s:\n  %s\n", email, d.URL)
		return
	}
	prof, found, err := browser.FindByEmail(email)
	if err == nil && found && browser.OpenURL(prof.Directory, d.URL) == nil {
		fmt.Printf("Opened %s in Chrome profile %q.\n", d.URL, prof.Name)
		return
	}
	fmt.Printf("No Chrome profile is signed in as %s. Open this address and sign in:\n  %s\n", email, d.URL)
}

func newProviderPriorityCmd(s providerSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "priority [label...]",
		Short: "Show or set the order in which autoswitch tries " + title(s.New()) + " profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			reg, err := loadRegistry(p)
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
					return fmt.Errorf("no %s profile named %q — `ccswitch %s list` shows what is saved", title(p), label, p.Name())
				}
			}
			reg.Priority = args
			return reg.Save(p.RegistryPath())
		},
	}
}

// useLockWait is how long `use` waits for the autoswitch lock.
var useLockWait = 5 * time.Second

func newProviderUseCmd(s providerSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "use <label>",
		Short: "Make a captured " + title(s.New()) + " profile the live login",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			label := args[0]
			// Wait for autoswitch, so the two cannot switch at the same time.
			unlock, err := lockFileWait(switchLock(p), useLockWait)
			if err != nil {
				return err
			}
			defer unlock()
			b, err := s.store()
			if err != nil {
				return err
			}
			// The local backend holds the fallback copy. It stays nil when
			// it cannot be resolved; the store is then the only copy.
			local := localOrNil()
			reg, err := loadRegistry(p)
			if err != nil {
				return err
			}
			if _, ok := reg.Profiles[label]; !ok {
				return fmt.Errorf("no %s profile named %q — `ccswitch %s list` shows what is captured", title(p), label, p.Name())
			}

			if err := useProfile(cmd.Context(), p, b, local, reg, label); err != nil {
				return err
			}
			fmt.Printf("%s now using %q: %s\n", title(p), label, describeProfile(reg.Profiles[label]))
			s.warn()
			return nil
		},
	}
}

// useProfile makes label the live login. It snapshots the live login
// first, writes the target's credential to the live slot and saves reg with
// label as active. `use` and autoswitch both call it.
func useProfile(ctx context.Context, p provider.Provider, b, local backend.Backend, reg *provider.Registry, label string) error {
	// Snapshot whatever is live first. The CLI rotates its own tokens,
	// so the live login is almost always newer than the copy captured
	// for that profile; overwriting without saving would roll it back
	// to a superseded token and force a re-login. The snapshot runs
	// before the target is read so that switching to the profile
	// that is live now restores the rotated copy.
	if err := snapshotLive(ctx, p, b, local, reg); err != nil {
		return err
	}

	target, err := fresher(ctx, p, b, local, p.CredKeyPrefix()+label)
	if err != nil {
		return fmt.Errorf("read %s profile %q: %w", title(p), label, err)
	}
	if len(target) == 0 {
		return fmt.Errorf("%s profile %q holds no credential — re-capture it with `ccswitch %s add %s`", title(p), label, p.Name(), label)
	}

	if err := p.WriteLive(target); err != nil {
		return err
	}
	reg.Active = label
	return reg.Save(p.RegistryPath())
}

// fresher reads a profile from the store and from the local backend and
// returns the copy with the later last refresh. A snapshot that could not
// reach the store sits in the local backend, so the store copy can be older.
// If only one copy is readable it wins; if neither is, the store's error
// returns. A nil local backend, or the store itself, needs only one read.
func fresher(ctx context.Context, p provider.Provider, store, local backend.Backend, key string) ([]byte, error) {
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
	tStore, okStore := p.LastRefresh(fromStore)
	tLocal, okLocal := p.LastRefresh(fromLocal)
	if okLocal && (!okStore || tLocal.After(tStore)) {
		return fromLocal, nil
	}
	return fromStore, nil
}

// snapshotLive saves the live credential into the profile it belongs
// to, found by account identity. The active marker is not trusted: a session
// that outlives a swap can write its own account back into the live
// slot, and saving that under the active profile would file one account's
// login under another.
//
// A rotating login that matches no profile is an error, so the caller stops
// and the live slot stays the only copy. Static logins such as API keys do not
// rotate and a missing login has nothing to lose, so both return nil without
// a snapshot.
//
// If the store write fails, the same bytes go to the local backend and the
// switch continues with a warning. If that fails too, the live slot is the
// only copy, so the caller must not overwrite it.
func snapshotLive(ctx context.Context, p provider.Provider, b, local backend.Backend, reg *provider.Registry) error {
	live, err := p.ReadLive()
	if errors.Is(err, provider.ErrNoLiveLogin) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the live %s login: %w", title(p), err)
	}
	id := p.Describe(live)
	if id.Static {
		return nil
	}
	label, ok := p.Match(reg, live)
	if !ok {
		who := id.Email
		if who == "" {
			who = "an unknown account"
		}
		return fmt.Errorf("the live %s login (%s) matches no saved profile, so switching would lose it — run `ccswitch %s add <label>` first", title(p), who, p.Name())
	}
	key := p.CredKeyPrefix() + label
	if err := b.Write(ctx, key, live); err != nil {
		if local == nil {
			return fmt.Errorf("save the live %s login into %q: %w (no local backend for a fallback copy)", title(p), label, err)
		}
		if lerr := local.Write(ctx, key, live); lerr != nil {
			return fmt.Errorf("save the live %s login into %q: store: %v; local copy: %w", title(p), label, err, lerr)
		}
		fmt.Fprintf(os.Stderr, "Warning: could not save the live %s login into %q in the store (%v); kept a local copy instead.\n", title(p), label, err)
	}
	if id.Email != "" {
		pr := reg.Profiles[label]
		pr.Mode, pr.Email, pr.Plan, pr.AccountID = id.Mode, id.Email, id.Plan, id.AccountID
		reg.Profiles[label] = pr
	}
	return nil
}

func newProviderCurrentCmd(s providerSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the live " + title(s.New()) + " login",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			if s.Unreadable != nil {
				if why := s.Unreadable(); why != "" {
					fmt.Println(why)
					return nil
				}
			}
			data, err := p.ReadLive()
			if errors.Is(err, provider.ErrNoLiveLogin) {
				fmt.Printf("No %s login at %s\n", title(p), p.LiveLocation())
				return nil
			}
			if err != nil {
				return err
			}
			id := p.Describe(data)
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
			// a login outside ccswitch leaves the marker pointing at an
			// account that is no longer live.
			reg, err := loadRegistry(p)
			if err == nil && reg.Active != "" {
				if pr, ok := reg.Profiles[reg.Active]; ok && pr.Email == id.Email {
					fmt.Printf("Profile: %s\n", reg.Active)
				}
			}
			s.warn()
			return nil
		},
	}
}

// revokeRemoved revokes the session of the profile before its removal. A
// failure is a warning. It does not revoke a static login. It does not revoke
// the live login, because that signs the user out of the CLI.
func revokeRemoved(ctx context.Context, p provider.Provider, reg *provider.Registry, label string, store, local backend.Backend) {
	blob, err := fresher(ctx, p, store, local, p.CredKeyPrefix()+label)
	if err != nil || p.Describe(blob).Static {
		return
	}
	live, err := p.ReadLive()
	if err != nil && !errors.Is(err, provider.ErrNoLiveLogin) {
		fmt.Fprintf(os.Stderr, "Warning: could not read the live %s login, so ccswitch did not revoke %q: %v\n", title(p), label, err)
		return
	}
	if liveLabel, ok := p.Match(reg, live); ok && len(live) > 0 && liveLabel == label {
		fmt.Printf("Profile %q is the live login, so ccswitch left it signed in.\n", label)
		return
	}
	if err := p.Revoke(ctx, blob); err != nil && !errors.Is(err, provider.ErrNotSupported) {
		fmt.Fprintf(os.Stderr, "Warning: could not revoke the session of %q: %v\n", label, err)
	}
}

func newProviderRemoveCmd(s providerSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <label>",
		Short: "Forget a captured " + title(s.New()) + " profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			label := args[0]
			reg, err := loadRegistry(p)
			if err != nil {
				return err
			}
			if _, ok := reg.Profiles[label]; !ok {
				return fmt.Errorf("no %s profile named %q", title(p), label)
			}

			ctx := cmd.Context()
			key := p.CredKeyPrefix() + label
			cfg, cfgErr := config.Load(config.DefaultPath())
			if cfgErr == nil {
				if b, bErr := resolveBackend(cfg); bErr == nil {
					local := localOrNil()
					revokeRemoved(ctx, p, reg, label, b, local)
					for _, c := range []backend.Backend{b, local} {
						if c == nil {
							continue
						}
						if delErr := c.Delete(ctx, key); delErr != nil && !errors.Is(delErr, backend.ErrNotFound) {
							fmt.Fprintf(os.Stderr, "Warning: could not delete a stored credential: %v\n", delErr)
						}
					}
				}
			}

			delete(reg.Profiles, label)
			reg.Priority = slices.DeleteFunc(reg.Priority, func(l string) bool { return l == label })
			if reg.Active == label {
				reg.Active = ""
			}
			if err := reg.Save(p.RegistryPath()); err != nil {
				return err
			}
			fmt.Printf("Removed %s profile %q. The live login at %s is untouched.\n", title(p), label, p.LiveLocation())
			return nil
		},
	}
}
