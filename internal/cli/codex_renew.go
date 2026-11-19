package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/codex"
)

// renewCodexProfile returns the stored credential of label. It renews the
// tokens when the access token has less than codexTokenRenewBefore left.
// renewed tells whether it did. It does not renew a credential with no
// readable expiry, such as an API key. Do not pass the live profile.
func renewCodexProfile(ctx context.Context, store, local backend.Backend, label string) (blob []byte, renewed bool, err error) {
	return renewCodexBlob(ctx, store, local, codexRenewTarget{
		lock:        "codex-profile-" + strings.ReplaceAll(label, "/", "_") + ".lock",
		key:         codex.CredKey(label),
		what:        fmt.Sprintf("Codex profile %q", label),
		again:       "ccswitch login --provider codex --only " + label,
		skipUnknown: true,
	})
}

// renewCodexProfiles renews every stored Codex profile that is near expiry.
// It skips API-key profiles and the live login, which Codex renews itself.
// A profile whose session ended is a warning, not a failure.
func renewCodexProfiles(ctx context.Context, store, local backend.Backend, log *slog.Logger) (int, error) {
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		return 0, err
	}
	// An unreadable live login could be any profile, so renew none.
	liveLabel := ""
	live, err := codex.ReadLive()
	switch {
	case err == nil:
		liveLabel, _ = codex.MatchProfile(reg, live)
	case !errors.Is(err, codex.ErrNoLiveLogin):
		return 0, fmt.Errorf("read the live Codex login: %w", err)
	}
	renewed, failed := 0, 0
	for _, label := range reg.Ordered() {
		if label == liveLabel || reg.Profiles[label].Mode == "apikey" {
			continue
		}
		_, did, err := renewCodexProfile(ctx, store, local, label)
		switch {
		case errors.Is(err, backend.ErrNotFound):
		case errors.Is(err, codex.ErrSessionEnded):
			log.Warn("Codex profile session ended; run `ccswitch login --provider codex --only "+label+"`", "label", label)
		case err != nil:
			failed++
			log.Error("Codex profile not renewed", "label", label, "err", err)
		case did:
			renewed++
			log.Info("Codex profile renewed", "label", label)
		}
	}
	if failed > 0 {
		return renewed, fmt.Errorf("%d Codex profile(s) failed to renew", failed)
	}
	return renewed, nil
}

// syncLiveCodex copies the live auth.json into its profile, as
// syncActiveAccount does for Claude. It ignores a live login that has no
// profile. A failure is a warning.
func syncLiveCodex(ctx context.Context, store, local backend.Backend) {
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read the Codex registry: %v\n", err)
		return
	}
	live, err := codex.ReadLive()
	if err != nil {
		return
	}
	label, ok := codex.MatchProfile(reg, live)
	if !ok {
		return
	}
	// Write only a change: each store write can show a 1Password prompt.
	if stored, err := codexFresher(ctx, store, local, codex.CredKey(label)); err == nil && bytes.Equal(stored, live) {
		return
	}
	before := reg.Profiles[label]
	if err := snapshotLiveCodex(ctx, store, local, reg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save the live Codex login: %v\n", err)
		return
	}
	if reg.Profiles[label] != before {
		if err := reg.Save(codexRegistryPath()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save the Codex registry: %v\n", err)
		}
	}
}
