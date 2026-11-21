package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/provider"
)

// Values of the login --provider flag. Each provider adds its Name().
const (
	providerAll    = "all"
	providerClaude = "claude"
)

// profileMatches reports whether id equals the label or the email of a
// profile, ignoring case. An unreadable registry matches nothing.
func profileMatches(p provider.Provider, id string) bool {
	reg, err := loadRegistry(p)
	if err != nil {
		return false
	}
	for label, pr := range reg.Profiles {
		if strings.EqualFold(label, id) || strings.EqualFold(pr.Email, id) {
			return true
		}
	}
	return false
}

// providerNames lists the values of --provider: all, claude, then each provider.
func providerNames() []string {
	names := []string{providerAll, providerClaude}
	for _, s := range providerSpecs {
		names = append(names, s.New().Name())
	}
	return names
}

// providerTitles names the providers for help text: "Codex" or "Codex or Grok".
func providerTitles() string {
	var titles []string
	for _, s := range providerSpecs {
		titles = append(titles, title(s.New()))
	}
	return orList(titles)
}

// orList joins words as "a, b or c".
func orList(words []string) string {
	if len(words) == 1 {
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

// selectLogins picks the rows to sign in again. A stale or expired
// login is dead; force adds every login that is not an API key. only narrows
// the rows by label or email. It also returns the labels it skipped because
// their state is unknown.
func selectLogins(rows []usageRow, only string, force bool) (picked []usageRow, unavailable []string) {
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

// loginProvider signs in again every profile of s with a dead login. A failure
// for one profile prints and goes on; the error reports how many failed. A
// registry or store that cannot be read prints one warning and returns nil.
func loginProvider(ctx context.Context, s providerSpec, only string, force, manual bool) error {
	p := s.New()
	name := title(p)
	reg, err := loadRegistry(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s login skipped: %v\n", name, err)
		return nil
	}
	if len(reg.Profiles) == 0 {
		return nil
	}
	store, err := s.store()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s login skipped: %v\n", name, err)
		return nil
	}
	rows, err := loadUsage(ctx, s, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s login skipped: %v\n", name, err)
		return nil
	}
	picked, unavailable := selectLogins(rows, only, force)
	for _, label := range unavailable {
		fmt.Printf("%s%s: %s (unavailable) — not signed in again%s\n", ansiDim, name, label, ansiReset)
	}

	signed, failed := 0, 0
	for i, r := range picked {
		fmt.Printf("\n%s\n", strings.Repeat("━", 56))
		fmt.Printf("[%d/%d] %s login: %s (%s)\n", i+1, len(picked), name, r.Label, r.Email)
		fmt.Printf("%s\n\n", strings.Repeat("━", 56))
		if err := loginProfile(ctx, p, store, reg, r, manual); err != nil {
			fmt.Printf("\n%s login failed for %s: %v\n", name, r.Label, err)
			failed++
			continue
		}
		fmt.Printf("\nCredentials saved for %s profile %s (%s)\n", name, r.Label, r.Email)
		signed++
	}
	fmt.Println(loginSummary(name, signed, len(unavailable), failed, len(picked)))
	if failed > 0 {
		return fmt.Errorf("%d %s sign-in(s) failed", failed, name)
	}
	return nil
}

// loginProfile signs in one profile. A live profile also gets the new
// login in the live slot. It does not call useProfile: that would
// snapshot the dead live login over the new one.
func loginProfile(ctx context.Context, p provider.Provider, store backend.Backend, reg *provider.Registry, r usageRow, manual bool) error {
	if r.Email == "" {
		return errors.New("the profile has no email to sign in as")
	}
	blob, err := signInAndSave(ctx, p, store, reg, r.Email, r.Label, manual)
	if err != nil {
		return err
	}
	if !r.Live {
		return nil
	}
	if err := p.WriteLive(blob); err != nil {
		return err
	}
	reg.Active = r.Label
	return reg.Save(p.RegistryPath())
}

// loginSummary is the last line of a provider pass.
func loginSummary(name string, signed, unavailable, failed, picked int) string {
	if picked == 0 && unavailable == 0 {
		return name + ": all profiles have valid logins."
	}
	parts := []string{fmt.Sprintf("%d %s signed in again", signed, plural(signed, "profile"))}
	if unavailable > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped (unavailable)", unavailable))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	return name + ": " + strings.Join(parts, ", ")
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
	case providerAll:
		var titles []string
		for _, s := range providerSpecs {
			titles = append(titles, title(s.New()))
		}
		return fmt.Errorf("no account or %s profile found matching: %s", strings.Join(titles, "/"), only)
	}
	return fmt.Errorf("no %s profile found matching: %s", titleCase(provider), only)
}
