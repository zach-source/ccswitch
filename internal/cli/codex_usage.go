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
	"github.com/zach-source/ccswitch/internal/codex"
)

// Usage row statuses.
const (
	usageOK          = "ok"
	usageStale       = "stale"       // the access token has expired; no request was made
	usageExpired     = "expired"     // the server rejected the token; sign in again
	usageUnavailable = "unavailable" // network error, 429, 5xx, or no readable credential
	usageAPIKey      = "apikey"      // an API-key login has no ChatGPT usage
)

// codexUsageRow is the usage state of one profile.
type codexUsageRow struct {
	Label    string
	Email    string
	Plan     string
	Live     bool
	Status   string
	Windows  []codex.Window
	LastUsed time.Time // last token refresh; set for stale rows
}

// Load is the highest used percentage across the windows.
func (r codexUsageRow) Load() float64 { return codex.Usage{Windows: r.Windows}.Load() }

func newCodexUsageCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show rate-limit usage for each Codex profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := loadCodexUsage(cmd.Context(), time.Now())
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No Codex profiles captured yet. Run `ccswitch codex add <label>` while logged in.")
				return nil
			}
			if asJSON {
				return writeCodexUsageJSON(cmd.OutOrStdout(), rows)
			}
			return writeCodexUsageText(cmd.OutOrStdout(), rows, time.Now())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// loadCodexUsage reads the registry, the stores and the live login, then
// queries every profile. It returns no rows when no profile exists.
func loadCodexUsage(ctx context.Context, now time.Time) ([]codexUsageRow, error) {
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		return nil, err
	}
	if len(reg.Profiles) == 0 {
		return nil, nil
	}
	store, err := codexStoreFunc()
	if err != nil {
		return nil, err
	}
	local, localErr := resolveLocalBackend()
	if localErr != nil {
		local = nil
	}
	live, err := codex.ReadLive()
	if err != nil && !errors.Is(err, codex.ErrNoLiveLogin) {
		return nil, fmt.Errorf("read the live Codex login: %w", err)
	}
	return collectCodexUsage(ctx, reg, store, local, live, now), nil
}

// collectCodexUsage queries every profile at once and returns one row per
// profile in priority order. The live profile uses the live auth.json, since
// Codex rotates its tokens there; the others use the fresher stored copy.
func collectCodexUsage(ctx context.Context, reg *codex.Registry, store, local backend.Backend, live []byte, now time.Time) []codexUsageRow {
	liveLabel, _ := codex.MatchProfile(reg, live)
	if len(live) == 0 {
		liveLabel = ""
	}
	labels := reg.Ordered()
	rows := make([]codexUsageRow, len(labels))
	var wg sync.WaitGroup
	for i, label := range labels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			blob := live
			var readErr error
			if label != liveLabel {
				blob, readErr = codexFresher(ctx, store, local, codex.CredKey(label))
			}
			rows[i] = codexUsageFor(ctx, reg.Profiles[label], label == liveLabel, blob, readErr, now)
		}()
	}
	wg.Wait()
	return rows
}

// codexUsageFor builds one row. It makes no request for a stale token or an
// API-key login.
func codexUsageFor(ctx context.Context, p codex.Profile, isLive bool, blob []byte, readErr error, now time.Time) codexUsageRow {
	row := codexUsageRow{Label: p.Label, Email: p.Email, Plan: p.Plan, Live: isLive, Windows: []codex.Window{}}
	if readErr != nil || len(blob) == 0 {
		row.Status = usageUnavailable
		return row
	}
	id := codex.Describe(blob)
	if id.Mode == "apikey" {
		row.Status = usageAPIKey
		return row
	}
	if exp, ok := codex.AccessTokenExpiry(blob); ok && !exp.After(now) {
		row.Status = usageStale
		row.LastUsed, _ = codex.LastRefresh(blob)
		if row.LastUsed.IsZero() {
			row.LastUsed = exp
		}
		return row
	}
	var tokens struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(blob, &tokens)
	u, err := codex.FetchUsage(ctx, tokens.Tokens.AccessToken, id.AccountID)
	switch {
	case errors.Is(err, codex.ErrUsageRejected):
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
	return row
}

type codexUsageWindowJSON struct {
	Seconds     int       `json:"seconds"`
	UsedPercent float64   `json:"usedPercent"`
	ResetAt     time.Time `json:"resetAt,omitzero"`
}

type codexUsageRowJSON struct {
	Label   string                 `json:"label"`
	Email   string                 `json:"email"`
	Live    bool                   `json:"live"`
	Status  string                 `json:"status"`
	Plan    string                 `json:"plan"`
	Windows []codexUsageWindowJSON `json:"windows"`
}

func writeCodexUsageJSON(w io.Writer, rows []codexUsageRow) error {
	out := make([]codexUsageRowJSON, len(rows))
	for i, r := range rows {
		o := codexUsageRowJSON{Label: r.Label, Email: r.Email, Live: r.Live, Status: r.Status, Plan: r.Plan,
			Windows: make([]codexUsageWindowJSON, len(r.Windows))}
		for j, win := range r.Windows {
			o.Windows[j] = codexUsageWindowJSON{Seconds: win.Seconds, UsedPercent: win.UsedPercent, ResetAt: win.ResetAt}
		}
		out[i] = o
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// writeCodexUsageText prints the rows in the style of `usage-all`.
func writeCodexUsageText(w io.Writer, rows []codexUsageRow, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%sCodex Accounts - Usage%s\n\n", ansiBold, ansiReset)
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
				b.WriteString(usageBarLine(codexWindowLabel(win), win.UsedPercent, until))
			}
		} else {
			fmt.Fprintf(&b, "    %s%s%s\n", ansiDim, codexUsageNote(r), ansiReset)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// codexWindowLabel names a window like the Claude section does: 7d, not weekly.
func codexWindowLabel(w codex.Window) string {
	if w.Seconds == 604800 {
		return "7d"
	}
	return w.Name()
}

// codexUsageNote is the one-line text for a row without bars.
func codexUsageNote(r codexUsageRow) string {
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
		return "expired — run ccswitch codex login " + who
	case usageAPIKey:
		return "api key (no usage)"
	}
	return "unavailable"
}
