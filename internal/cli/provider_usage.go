package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/provider"
)

// Usage row statuses.
const (
	usageOK          = "ok"
	usageStale       = "stale"       // the access token has expired; no request was made
	usageExpired     = "expired"     // the server rejected the token; sign in again
	usageUnavailable = "unavailable" // network error, 429, 5xx, or no readable credential
	usageAPIKey      = "apikey"      // the login has no usage to show, such as an API key
)

// usageRow is the usage state of one profile.
type usageRow struct {
	Label    string
	Email    string
	Plan     string
	Live     bool
	Status   string
	Windows  []provider.Window
	LastUsed time.Time // last token refresh; set for stale rows
}

// Load is the highest used percentage across the windows.
func (r usageRow) Load() float64 { return provider.Usage{Windows: r.Windows}.Load() }

func newProviderUsageCmd(s providerSpec) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show rate-limit usage for each " + title(s.New()) + " profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := s.New()
			rows, err := loadUsage(cmd.Context(), s, time.Now())
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No %s profiles captured yet. Run `ccswitch %s add <label>` while logged in.\n", title(p), p.Name())
				return nil
			}
			if asJSON {
				return writeUsageJSON(cmd.OutOrStdout(), rows)
			}
			return writeUsageText(cmd.OutOrStdout(), p, rows, time.Now())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// loadUsage reads the registry, the stores and the live login, then
// queries every profile. It returns no rows when no profile exists.
func loadUsage(ctx context.Context, s providerSpec, now time.Time) ([]usageRow, error) {
	p := s.New()
	reg, err := loadRegistry(p)
	if err != nil {
		return nil, err
	}
	if len(reg.Profiles) == 0 {
		return nil, nil
	}
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	local := localOrNil()
	// Renew stale profiles only while no switch can run. If autoswitch or
	// `use` holds the lock, show the rows without a renewal.
	unlock, renew, err := lockFile(switchLock(p))
	if err != nil {
		return nil, err
	}
	if renew {
		defer unlock()
	}
	live, err := p.ReadLive()
	if err != nil && !errors.Is(err, provider.ErrNoLiveLogin) {
		return nil, fmt.Errorf("read the live %s login: %w", title(p), err)
	}
	return collectProviderUsage(ctx, p, reg, store, local, live, now, renew), nil
}

// collectProviderUsage queries every profile at once and returns one row per
// profile in priority order. The live profile uses the live login, since
// its CLI rotates the tokens there; the others use the fresher stored copy.
//
// When renew is true, it renews a stale profile that is not live. The caller
// must then hold the switch lock, so no switch runs at the same time.
func collectProviderUsage(ctx context.Context, p provider.Provider, reg *provider.Registry, store, local backend.Backend, live []byte, now time.Time, renew bool) []usageRow {
	liveLabel, _ := p.Match(reg, live)
	if len(live) == 0 {
		liveLabel = ""
	}
	labels := reg.Ordered()
	rows := make([]usageRow, len(labels))
	var wg sync.WaitGroup
	for i, label := range labels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			blob := live
			var readErr error
			if label != liveLabel {
				blob, readErr = fresher(ctx, p, store, local, p.CredKeyPrefix()+label)
				if renew {
					blob = renewStale(ctx, p, store, local, label, blob, readErr, now)
				}
			}
			row, renewed := usageFor(ctx, p, reg.Profiles[label], label == liveLabel, blob, readErr, now)
			if renewed != nil {
				saveBlob(ctx, store, local, p.CredKeyPrefix()+label, fmt.Sprintf("%s profile %q", title(p), label),
					"ccswitch login --provider "+p.Name()+" --only "+label, renewed)
			}
			rows[i] = row
		}()
	}
	wg.Wait()
	return rows
}

// renewStale renews a stored profile whose access token has expired or
// is near expiry. It returns blob when the renewal fails or is not needed, so
// the row keeps its status. The live profile never comes here: its CLI renews it.
func renewStale(ctx context.Context, p provider.Provider, store, local backend.Backend, label string, blob []byte, readErr error, now time.Time) []byte {
	if readErr != nil {
		return blob
	}
	if exp, ok := p.Expiry(blob); !ok || exp.Sub(now) >= tokenRenewBefore {
		return blob
	}
	if fresh, _, err := renewProfile(ctx, p, store, local, label); err == nil {
		return fresh
	}
	return blob
}

// usageFor builds one row. It makes no request for a stale token or a login
// with no usage. The bytes are the login when the usage call renewed it.
func usageFor(ctx context.Context, p provider.Provider, pr provider.Profile, isLive bool, blob []byte, readErr error, now time.Time) (usageRow, []byte) {
	row := usageRow{Label: pr.Label, Email: pr.Email, Plan: pr.Plan, Live: isLive, Windows: []provider.Window{}}
	if readErr != nil || len(blob) == 0 {
		row.Status = usageUnavailable
		return row, nil
	}
	if exp, ok := p.Expiry(blob); ok && !exp.After(now) {
		row.Status = usageStale
		row.LastUsed, _ = p.LastRefresh(blob)
		if row.LastUsed.IsZero() {
			row.LastUsed = exp
		}
		return row, nil
	}
	u, renewed, err := p.FetchUsage(ctx, blob)
	switch {
	case errors.Is(err, provider.ErrNotSupported):
		row.Status = usageAPIKey
	case errors.Is(err, provider.ErrUsageRejected):
		row.Status = usageExpired
	case err != nil:
		row.Status = usageUnavailable
	default:
		row.Status = usageOK
		row.Windows = append(row.Windows, u.Windows...)
		if u.Plan != "" {
			row.Plan = u.Plan
		}
	}
	return row, renewed
}

type usageWindowJSON struct {
	Seconds     int       `json:"seconds"`
	UsedPercent float64   `json:"usedPercent"`
	ResetAt     time.Time `json:"resetAt,omitzero"`
}

type usageRowJSON struct {
	Label   string            `json:"label"`
	Email   string            `json:"email"`
	Live    bool              `json:"live"`
	Status  string            `json:"status"`
	Plan    string            `json:"plan"`
	Windows []usageWindowJSON `json:"windows"`
}

func writeUsageJSON(w io.Writer, rows []usageRow) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(usageRowsJSON(rows))
}

// usageRowsJSON converts rows to their JSON form.
func usageRowsJSON(rows []usageRow) []usageRowJSON {
	out := make([]usageRowJSON, len(rows))
	for i, r := range rows {
		o := usageRowJSON{Label: r.Label, Email: r.Email, Live: r.Live, Status: r.Status, Plan: r.Plan,
			Windows: make([]usageWindowJSON, len(r.Windows))}
		for j, win := range r.Windows {
			o.Windows[j] = usageWindowJSON{Seconds: win.Seconds, UsedPercent: win.UsedPercent, ResetAt: win.ResetAt}
		}
		out[i] = o
	}
	return out
}

// writeUsageText prints the rows in the style of `usage-all`.
func writeUsageText(w io.Writer, p provider.Provider, rows []usageRow, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s Accounts - Usage%s\n\n", ansiBold, title(p), ansiReset)
	for _, r := range rows {
		marker := ansiDim + "○" + ansiReset
		if r.Live {
			marker = ansiCyan + "◉" + ansiReset
		}
		fmt.Fprintf(&b, "  %s %s%s%s", marker, ansiBold, r.Label, ansiReset)
		if r.Plan != "" {
			fmt.Fprintf(&b, " %s[%s]%s", ansiDim, r.Plan, ansiReset)
		}
		b.WriteString("\n")
		if r.Status == usageOK && len(r.Windows) > 0 {
			for _, win := range r.Windows {
				until := ""
				if !win.ResetAt.IsZero() {
					until = fmtUntil(win.ResetAt.Sub(now))
				}
				b.WriteString(usageBarLine(windowLabel(win), win.UsedPercent, until))
			}
		} else {
			fmt.Fprintf(&b, "    %s%s%s\n", ansiDim, usageNote(p, r), ansiReset)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// windowLabel names a window like the Claude section does: 7d, not weekly.
func windowLabel(w provider.Window) string {
	if w.Seconds == 604800 {
		return "7d"
	}
	return w.Name()
}

// usageNote is the one-line text for a row without bars.
func usageNote(p provider.Provider, r usageRow) string {
	switch r.Status {
	case usageOK:
		return "no usage windows"
	case usageStale:
		return "stale (last used " + r.LastUsed.Local().Format("Jan 02") + ")"
	case usageExpired:
		who := r.Email
		if who == "" {
			who = r.Label
		}
		return "expired — run ccswitch " + p.Name() + " login " + who
	case usageAPIKey:
		return "api key (no usage)"
	}
	return "unavailable"
}

// writeUsageSection prints one provider part of `usage-all`. An error prints
// one dim warning line; it never fails the command.
func writeUsageSection(w io.Writer, p provider.Provider, rows []usageRow, err error, now time.Time) {
	if err != nil {
		fmt.Fprintf(w, "%s%s Accounts - Usage%s\n\n    %s%s usage unavailable: %v%s\n\n", ansiBold, title(p), ansiReset, ansiDim, title(p), err, ansiReset)
		return
	}
	if len(rows) > 0 {
		_ = writeUsageText(w, p, rows, now)
	}
}
