package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
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
			reg, err := codex.LoadRegistry(codexRegistryPath())
			if err != nil {
				return err
			}
			if len(reg.Profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No Codex profiles captured yet. Run `ccswitch codex add <label>` while logged in.")
				return nil
			}
			store, err := codexStoreFunc()
			if err != nil {
				return err
			}
			local, localErr := resolveLocalBackend()
			if localErr != nil {
				local = nil
			}
			live, err := codex.ReadLive()
			if err != nil && !errors.Is(err, codex.ErrNoLiveLogin) {
				return fmt.Errorf("read the live Codex login: %w", err)
			}
			rows := collectCodexUsage(cmd.Context(), reg, store, local, live, time.Now())
			if asJSON {
				return writeCodexUsageJSON(cmd.OutOrStdout(), rows)
			}
			return writeCodexUsageText(cmd.OutOrStdout(), rows, time.Now())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
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

func writeCodexUsageText(w io.Writer, rows []codexUsageRow, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		mark := "  "
		if r.Live {
			mark = "* "
		}
		plan := r.Plan
		if plan == "" {
			plan = "-"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\n", mark, r.Label, plan, codexUsageDetail(r, now))
	}
	return tw.Flush()
}

// codexUsageDetail is the last column of a text row.
func codexUsageDetail(r codexUsageRow, now time.Time) string {
	switch r.Status {
	case usageOK:
		if len(r.Windows) == 0 {
			return "no usage windows"
		}
		parts := make([]string, len(r.Windows))
		for i, win := range r.Windows {
			parts[i] = fmt.Sprintf("%s %.0f%%", win.Name(), win.UsedPercent)
			if !win.ResetAt.IsZero() {
				parts[i] += " (resets " + fmtReset(win.ResetAt, now) + ")"
			}
		}
		return strings.Join(parts, "  ")
	case usageStale:
		return "stale (last used " + r.LastUsed.Local().Format("Jan 02") + ")"
	case usageExpired:
		return "expired (sign in again with `ccswitch codex login`)"
	case usageAPIKey:
		return "api key (no usage)"
	}
	return "unavailable"
}

// fmtReset prints a time of day for a reset today and the date and time
// for a later one.
func fmtReset(t, now time.Time) string {
	t = t.Local()
	if y1, m1, d1 := t.Date(); y1 == now.Year() && m1 == now.Month() && d1 == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 02 15:04")
}
