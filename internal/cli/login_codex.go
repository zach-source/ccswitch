package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/codex"
)

// Values of the login --provider flag.
const (
	providerAll    = "all"
	providerClaude = "claude"
	providerCodex  = "codex"
)

// codexProfileMatches reports whether id equals the label or the email of a
// Codex profile, ignoring case. An unreadable registry matches nothing.
func codexProfileMatches(id string) bool {
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		return false
	}
	for label, p := range reg.Profiles {
		if strings.EqualFold(label, id) || strings.EqualFold(p.Email, id) {
			return true
		}
	}
	return false
}

// selectCodexLogins picks the rows to sign in again. A stale or expired
// login is dead; force adds every login that is not an API key. only narrows
// the rows by label or email. It also returns the labels it skipped because
// their state is unknown.
func selectCodexLogins(rows []codexUsageRow, only string, force bool) (picked []codexUsageRow, unavailable []string) {
	for _, r := range rows {
		if only != "" && !strings.EqualFold(r.Label, only) && !strings.EqualFold(r.Email, only) {
			continue
		}
		switch {
		case r.Status == usageAPIKey:
		case force, r.Status == usageStale, r.Status == usageExpired:
			picked = append(picked, r)
		case r.Status == usageUnavailable:
			unavailable = append(unavailable, r.Label)
		}
	}
	return picked, unavailable
}

// loginCodex signs in again every Codex profile with a dead login. A failure
// for one profile prints and goes on; the error reports how many failed. A
// Codex registry or store that cannot be read prints one warning and returns nil.
func loginCodex(ctx context.Context, only string, force, manual bool) error {
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Codex login skipped: %v\n", err)
		return nil
	}
	if len(reg.Profiles) == 0 {
		return nil
	}
	store, err := codexStoreFunc()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Codex login skipped: %v\n", err)
		return nil
	}
	rows, err := loadCodexUsage(ctx, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Codex login skipped: %v\n", err)
		return nil
	}
	picked, unavailable := selectCodexLogins(rows, only, force)
	for _, label := range unavailable {
		fmt.Printf("%sCodex: %s (unavailable) — not signed in again%s\n", ansiDim, label, ansiReset)
	}

	signed, failed := 0, 0
	for i, r := range picked {
		fmt.Printf("\n%s\n", strings.Repeat("━", 56))
		fmt.Printf("[%d/%d] Codex login: %s (%s)\n", i+1, len(picked), r.Label, r.Email)
		fmt.Printf("%s\n\n", strings.Repeat("━", 56))
		if err := loginCodexProfile(ctx, store, reg, r, manual); err != nil {
			fmt.Printf("\nCodex login failed for %s: %v\n", r.Label, err)
			failed++
			continue
		}
		fmt.Printf("\nCredentials saved for Codex profile %s (%s)\n", r.Label, r.Email)
		signed++
	}
	fmt.Println(codexLoginSummary(signed, len(unavailable), failed, len(picked)))
	if failed > 0 {
		return fmt.Errorf("%d Codex sign-in(s) failed", failed)
	}
	return nil
}

// loginCodexProfile signs in one profile. A live profile also gets the new
// login in the live auth.json. It does not call useCodexProfile: that would
// snapshot the dead live login over the new one.
func loginCodexProfile(ctx context.Context, store backend.Backend, reg *codex.Registry, r codexUsageRow, manual bool) error {
	if r.Email == "" {
		return errors.New("the profile has no email to sign in as")
	}
	blob, err := codexSignInAndSave(ctx, store, reg, r.Email, r.Label, manual)
	if err != nil {
		return err
	}
	if !r.Live {
		return nil
	}
	if err := codex.WriteLive(blob); err != nil {
		return err
	}
	reg.Active = r.Label
	return reg.Save(codexRegistryPath())
}

// codexLoginSummary is the last line of the Codex pass.
func codexLoginSummary(signed, unavailable, failed, picked int) string {
	if picked == 0 && unavailable == 0 {
		return "Codex: all profiles have valid logins."
	}
	parts := []string{fmt.Sprintf("%d %s signed in again", signed, plural(signed, "profile"))}
	if unavailable > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped (unavailable)", unavailable))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	return "Codex: " + strings.Join(parts, ", ")
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// loginNoMatch is the error for a --only value that no selected pass matches.
func loginNoMatch(provider, only string) error {
	switch provider {
	case providerClaude:
		return fmt.Errorf("no account found matching: %s", only)
	case providerCodex:
		return fmt.Errorf("no Codex profile found matching: %s", only)
	}
	return fmt.Errorf("no account or Codex profile found matching: %s", only)
}
