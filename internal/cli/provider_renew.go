package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/provider"
)

// renewProfile returns the stored credential of label. It renews the
// tokens when the access token has less than tokenRenewBefore left.
// renewed tells whether it did. It does not renew a credential with no
// readable expiry, such as an API key. Do not pass the live profile.
func renewProfile(ctx context.Context, p provider.Provider, store, local backend.Backend, label string) (blob []byte, renewed bool, err error) {
	return renewBlob(ctx, p, store, local, renewTarget{
		lock:        profileLock(p, label),
		key:         p.CredKeyPrefix() + label,
		what:        fmt.Sprintf("%s profile %q", title(p), label),
		again:       "ccswitch login --provider " + p.Name() + " --only " + label,
		skipUnknown: true,
	})
}

// renewProfiles renews every stored profile that is near expiry. It skips
// the live login, which its CLI renews itself, and logins that cannot renew.
// A profile whose session ended is a warning, not a failure.
func renewProfiles(ctx context.Context, p provider.Provider, store, local backend.Backend, log *slog.Logger) (int, error) {
	reg, err := loadRegistry(p)
	if err != nil {
		return 0, err
	}
	// An unreadable live login could be any profile, so renew none.
	var liveLabels map[string]bool
	live, err := p.ReadLive()
	switch {
	case err == nil:
		liveLabels = liveCandidates(p, reg, live)
	case !errors.Is(err, provider.ErrNoLiveLogin):
		return 0, fmt.Errorf("read the live %s login: %w", title(p), err)
	}
	renewed, failed := 0, 0
	for _, label := range reg.Ordered() {
		if liveLabels[label] {
			continue
		}
		_, did, err := renewProfile(ctx, p, store, local, label)
		switch {
		case errors.Is(err, backend.ErrNotFound):
		case errors.Is(err, provider.ErrSessionEnded):
			log.Warn(title(p)+" profile session ended; run `ccswitch login --provider "+p.Name()+" --only "+label+"`", "label", label)
		case err != nil:
			failed++
			log.Error(title(p)+" profile not renewed", "label", label, "err", err)
		case did:
			renewed++
			log.Info(title(p)+" profile renewed", "label", label)
		}
	}
	if failed > 0 {
		return renewed, fmt.Errorf("%d %s profile(s) failed to renew", failed, title(p))
	}
	return renewed, nil
}

// refreshProfiles is the provider pass of refresh-all. It saves the live
// login first, so the stored copy is not older than the file the CLI keeps
// up to date. The switch lock keeps a switch from reading a profile while its
// refresh token rotates.
func refreshProfiles(ctx context.Context, p provider.Provider, store backend.Backend, log *slog.Logger) error {
	unlock, err := lockFileWait(switchLock(p), 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: skipped the %s profiles: %v\n", title(p), err)
		return nil
	}
	defer unlock()
	local := localOrNil()
	syncLive(ctx, p, store, local)
	n, err := renewProfiles(ctx, p, store, local, log)
	if n > 0 {
		fmt.Printf("refresh-all: %d %s profile(s) renewed.\n", n, title(p))
	}
	return err
}

// syncLive copies the live login into its profile, as syncActiveAccount does
// for Claude. It ignores a live login that has no profile. A failure is a
// warning.
func syncLive(ctx context.Context, p provider.Provider, store, local backend.Backend) {
	reg, err := loadRegistry(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read the %s registry: %v\n", title(p), err)
		return
	}
	live, err := p.ReadLive()
	if err != nil {
		return
	}
	label, ok := p.Match(reg, live)
	if !ok {
		return
	}
	// Write only a change: each store write can show a 1Password prompt.
	if stored, err := fresher(ctx, p, store, local, p.CredKeyPrefix()+label); err == nil && bytes.Equal(stored, live) {
		return
	}
	before := reg.Profiles[label]
	if err := snapshotLive(ctx, p, store, local, reg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save the live %s login: %v\n", title(p), err)
		return
	}
	if reg.Profiles[label] != before {
		if err := reg.Save(p.RegistryPath()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save the %s registry: %v\n", title(p), err)
		}
	}
}

// liveCandidates returns the profiles that can hold the live login. It is the
// matching profile. When Match finds none, because two profiles share the
// email, it is every profile with the email of the live login: any of them
// could be live, and a renewal can rotate the live refresh token. It uses the
// emails in the registry and reads no stored login.
func liveCandidates(p provider.Provider, reg *provider.Registry, live []byte) map[string]bool {
	if len(live) == 0 {
		return nil
	}
	if label, ok := p.Match(reg, live); ok {
		return map[string]bool{label: true}
	}
	email := p.Describe(live).Email
	if email == "" {
		return nil
	}
	out := map[string]bool{}
	for label, pr := range reg.Profiles {
		if strings.EqualFold(pr.Email, email) {
			out[label] = true
		}
	}
	return out
}
