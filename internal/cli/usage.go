package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/credentials"
	"github.com/zach-source/ccswitch/internal/refresh"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newUsageCmd)
	subcommandBuilders = append(subcommandBuilders, newUsageAllCmd)
	subcommandBuilders = append(subcommandBuilders, newSetLimitCmd)
}

// ANSI escape codes for usage rendering.
const (
	ansiBold   = "\033[1m"
	ansiDim    = "\033[0;90m"
	ansiCyan   = "\033[0;36m"
	ansiGreen  = "\033[0;32m"
	ansiYellow = "\033[0;33m"
	ansiRed    = "\033[0;31m"
	ansiReset  = "\033[0m"
)

// barWidth is the character width of the rendered usage bar.
const barWidth = 20

// renderBar returns a filled/empty block bar for a 0-100 percentage.
func renderBar(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * barWidth)
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}

// pctColor returns the ANSI color for a utilization percentage: green
// under 50%, yellow under 80%, red at or above 80%.
func pctColor(pct float64) string {
	switch {
	case pct >= 80:
		return ansiRed
	case pct >= 50:
		return ansiYellow
	default:
		return ansiGreen
	}
}

// fmtTokens renders a token count compactly (1.5M, 64K, 900).
func fmtTokens(t float64) string {
	switch {
	case t >= 1_000_000:
		return fmt.Sprintf("%.1fM", t/1_000_000)
	case t >= 1_000:
		return fmt.Sprintf("%.0fK", t/1_000)
	default:
		return fmt.Sprintf("%.0f", t)
	}
}

// ── usage ─────────────────────────────────────────────────────────────────

// ccusage JSON shapes (only the fields ccswitch renders).
type ccusageBlocks struct {
	Blocks []struct {
		TotalTokens float64 `json:"totalTokens"`
		CostUSD     float64 `json:"costUSD"`
		BurnRate    struct {
			CostPerHour float64 `json:"costPerHour"`
		} `json:"burnRate"`
		TokenLimitStatus struct {
			Limit       float64 `json:"limit"`
			PercentUsed float64 `json:"percentUsed"`
		} `json:"tokenLimitStatus"`
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	} `json:"blocks"`
}

type ccusageWeekly struct {
	Weekly []struct {
		Week        string   `json:"week"`
		TotalTokens float64  `json:"totalTokens"`
		TotalCost   float64  `json:"totalCost"`
		ModelsUsed  []string `json:"modelsUsed"`
	} `json:"weekly"`
}

func newUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Show the active account's 5-hour block and weekly token usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("ccusage"); err != nil {
				return errors.New("ccusage is not installed (npm i -g ccusage)")
			}

			acctLabel := activeAccountLabel()
			weeklyLimit := activeWeeklyLimit()

			fmt.Printf("%sClaude Code Usage%s\n", ansiBold, ansiReset)
			fmt.Printf("%sAccount:%s %s%s%s\n", ansiDim, ansiReset, ansiCyan, acctLabel, ansiReset)
			fmt.Println()

			renderBlock(cmd.Context())
			fmt.Println()
			renderWeekly(cmd.Context(), weeklyLimit)
			return nil
		},
	}
}

// renderBlock prints the active 5-hour block section.
func renderBlock(ctx context.Context) {
	fmt.Printf("%s5-Hour Block%s\n", ansiBold, ansiReset)

	out, err := exec.CommandContext(ctx, "ccusage",
		"blocks", "--active", "--json", "--offline", "--token-limit", "max").Output()
	if err != nil {
		fmt.Println("  No active block")
		return
	}
	var data ccusageBlocks
	if err := json.Unmarshal(out, &data); err != nil || len(data.Blocks) == 0 {
		fmt.Println("  No active block")
		return
	}

	b := data.Blocks[0]
	pct := b.TokenLimitStatus.PercentUsed
	c := pctColor(pct)
	fmt.Printf("  %s%s %.0f%%%s  (%s / %s)\n",
		c, renderBar(pct), pct, ansiReset,
		fmtTokens(b.TotalTokens), fmtTokens(b.TokenLimitStatus.Limit))

	timeLeft := "unknown"
	if end, err := time.Parse(time.RFC3339, b.EndTime); err == nil {
		if rem := time.Until(end); rem > 0 {
			timeLeft = fmt.Sprintf("%dh %dm", int(rem.Hours()), int(rem.Minutes())%60)
		} else {
			timeLeft = "0h 0m"
		}
	}
	fmt.Printf("  Cost: $%.2f ($%.2f/hr)  Time left: %s\n",
		b.CostUSD, b.BurnRate.CostPerHour, timeLeft)
}

// renderWeekly prints the weekly usage section, scaling against weeklyLimit
// when one is configured.
func renderWeekly(ctx context.Context, weeklyLimit int64) {
	fmt.Printf("%sWeekly Usage%s\n", ansiBold, ansiReset)

	out, err := exec.CommandContext(ctx, "ccusage", "weekly", "--json", "--offline").Output()
	if err != nil {
		fmt.Println("  This week:  No data")
		return
	}
	var data ccusageWeekly
	if err := json.Unmarshal(out, &data); err != nil || len(data.Weekly) == 0 {
		fmt.Println("  This week:  No data")
		return
	}

	// The most recent entry is this week; the one before it is last week.
	weeks := data.Weekly
	this := weeks[len(weeks)-1]

	if weeklyLimit > 0 {
		pct := minFloat(100, this.TotalTokens/float64(weeklyLimit)*100)
		c := pctColor(pct)
		fmt.Printf("  This week:  %s%s %.0f%%%s  (%s / %s)\n",
			c, renderBar(pct), pct, ansiReset,
			fmtTokens(this.TotalTokens), fmtTokens(float64(weeklyLimit)))
	} else {
		fmt.Printf("  This week:  %s tokens\n", fmtTokens(this.TotalTokens))
	}
	fmt.Printf("  Cost: $%.2f  Models: %s\n", this.TotalCost, strings.Join(shortModels(this.ModelsUsed), ", "))

	if len(weeks) >= 2 {
		last := weeks[len(weeks)-2]
		if weeklyLimit > 0 {
			pct := minFloat(100, last.TotalTokens/float64(weeklyLimit)*100)
			c := pctColor(pct)
			fmt.Printf("  Last week:  %s%s %.0f%%%s  (%s / %s)\n",
				c, renderBar(pct), pct, ansiReset,
				fmtTokens(last.TotalTokens), fmtTokens(float64(weeklyLimit)))
		} else {
			fmt.Printf("  Last week:  %s tokens  $%.2f\n", fmtTokens(last.TotalTokens), last.TotalCost)
		}
	}

	if weeklyLimit == 0 {
		fmt.Printf("%s  Set a weekly limit: ccswitch set-limit <tokens>%s\n", ansiDim, ansiReset)
	}
}

// shortModels trims the date suffix and "claude-" prefix from model names.
func shortModels(models []string) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		m = strings.TrimPrefix(m, "claude-")
		if i := strings.LastIndex(m, "-2025"); i != -1 {
			m = m[:i]
		}
		out = append(out, m)
	}
	return out
}

// ── usage-all ─────────────────────────────────────────────────────────────

// oauthUsage is the shape returned by the Anthropic OAuth usage API.
type oauthUsage struct {
	FiveHour struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	} `json:"five_hour"`
	SevenDay struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	} `json:"seven_day"`
}

func newUsageAllCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "usage-all",
		Short: "Query the OAuth usage API for every managed account",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				fmt.Println("No accounts are managed yet.")
				return nil
			}
			b, err := resolveBackend(cfg)
			if err != nil {
				return fmt.Errorf("backend not available: %w", err)
			}

			results := collectUsage(cmd.Context(), b, seq, cfg.Refresh.ExpiryBuffer, !jsonOut)

			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"accounts": results})
			}
			fmt.Println()
			renderUsageAll(results)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON instead of a rendered table")
	return cmd
}

// accountUsage is one row of `usage-all` output.
type accountUsage struct {
	ID       string      `json:"id"`
	Email    string      `json:"email"`
	Active   bool        `json:"active"`
	Status   string      `json:"status"` // ok | expired | error
	FiveHour *oauthSlice `json:"five_hour,omitempty"`
	SevenDay *oauthSlice `json:"seven_day,omitempty"`
}

type oauthSlice struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// collectUsage queries the OAuth usage API for each account, refreshing an
// expired token once before giving up. progress controls the inline
// "Querying #id…" status line.
func collectUsage(ctx context.Context, b backend.Backend, seq *account.Sequence, buffer time.Duration, progress bool) []accountUsage {
	out := make([]accountUsage, 0, len(seq.Sequence))
	active := activeID(seq)

	for _, id := range seq.Sequence {
		acct := seq.Accounts[id]
		row := accountUsage{ID: id, Email: acct.Email, Active: id == active, Status: "error"}

		if progress {
			fmt.Printf("  %sQuerying #%s %s...%s ", ansiDim, id, acct.Email, ansiReset)
		}

		cred := readAccountCred(ctx, b, id == active, id, acct.Email)
		if cred != nil && cred.IsExpired(buffer) {
			if rawFresh, fresh, err := refresh.RefreshOne(ctx, cred); err == nil {
				// Persist the raw refreshed bytes verbatim — never a
				// re-marshaled struct, which would drop unmodeled fields.
				_ = b.Write(ctx, account.BackupCredKey(id, acct.Email), rawFresh)
				cred = fresh
			}
		}
		if cred == nil || cred.ClaudeAIOAuth.AccessToken == "" {
			if progress {
				fmt.Printf("%s✗ no token%s\n", ansiRed, ansiReset)
			}
			out = append(out, row)
			continue
		}

		usage, err := fetchOAuthUsage(ctx, cred.ClaudeAIOAuth.AccessToken)
		if err != nil {
			row.Status = "expired"
			if progress {
				fmt.Printf("%s✗ %v%s\n", ansiRed, err, ansiReset)
			}
			out = append(out, row)
			continue
		}

		row.Status = "ok"
		row.FiveHour = &oauthSlice{Utilization: usage.FiveHour.Utilization, ResetsAt: usage.FiveHour.ResetsAt}
		row.SevenDay = &oauthSlice{Utilization: usage.SevenDay.Utilization, ResetsAt: usage.SevenDay.ResetsAt}
		if progress {
			fmt.Printf("%s✓%s\n", ansiGreen, ansiReset)
		}
		out = append(out, row)
	}
	return out
}

// readAccountCred reads an account's credentials from the active slot (when
// isActive) or its backup slot. Returns nil on any miss.
func readAccountCred(ctx context.Context, b backend.Backend, isActive bool, id, email string) *credentials.Credentials {
	key := account.BackupCredKey(id, email)
	if isActive {
		key = account.ActiveCredKey
	}
	data, err := b.Read(ctx, key)
	if err != nil || len(data) == 0 {
		return nil
	}
	cred, err := credentials.Parse(data)
	if err != nil {
		return nil
	}
	return cred
}

// fetchOAuthUsage calls the Anthropic OAuth usage endpoint with a bearer token.
func fetchOAuthUsage(ctx context.Context, token string) (*oauthUsage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("API error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var u oauthUsage
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, errors.New("bad response")
	}
	return &u, nil
}

// renderUsageAll prints the per-account 5h/7d utilization table.
func renderUsageAll(rows []accountUsage) {
	fmt.Printf("%sAll Accounts - Usage%s\n\n", ansiBold, ansiReset)
	for _, r := range rows {
		marker := ansiDim + "○" + ansiReset
		if r.Active {
			marker = ansiCyan + "◉" + ansiReset
		}
		fmt.Printf("  %s %s#%s%s %s\n", marker, ansiBold, r.ID, ansiReset, r.Email)

		switch r.Status {
		case "error":
			fmt.Printf("    %sCould not fetch usage%s\n", ansiRed, ansiReset)
		case "expired":
			fmt.Printf("    %sToken expired — switch to this account to refresh%s\n", ansiYellow, ansiReset)
		default:
			h5, d7 := r.FiveHour, r.SevenDay
			fmt.Printf("    5h:  %s%s %.0f%%%s  %sresets in %s%s\n",
				pctColor(h5.Utilization), renderBar(h5.Utilization), h5.Utilization, ansiReset,
				ansiDim, timeUntil(h5.ResetsAt), ansiReset)
			fmt.Printf("    7d:  %s%s %.0f%%%s  %sresets in %s%s\n",
				pctColor(d7.Utilization), renderBar(d7.Utilization), d7.Utilization, ansiReset,
				ansiDim, timeUntil(d7.ResetsAt), ansiReset)
		}
		fmt.Println()
	}
}

// timeUntil renders the duration until an ISO-8601 timestamp ("3h 20m",
// "2d 4h", "resetting" if already past). Empty string on parse failure.
func timeUntil(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	d := time.Until(t)
	if d <= 0 {
		return "resetting"
	}
	hours := int(d.Hours())
	if hours >= 24 {
		return fmt.Sprintf("%dd %dh", hours/24, hours%24)
	}
	return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())%60)
}

// ── set-limit ─────────────────────────────────────────────────────────────

func newSetLimitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-limit <weekly-token-limit>",
		Short: "Set the weekly token limit for the active account (accepts 6700M, 6.7B, …)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, err := parseTokenLimit(args[0])
			if err != nil {
				return fmt.Errorf("invalid token limit %q: %w", args[0], err)
			}
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			id := activeID(seq)
			if id == "" {
				return errors.New("no active account")
			}
			acct, ok := seq.Accounts[id]
			if !ok {
				return errors.New("active account not found in sequence.json")
			}

			acct.WeeklyTokenLimit = limit
			seq.Accounts[id] = acct
			if err := seq.Save(sequencePath()); err != nil {
				return err
			}

			fmt.Printf("Set weekly limit for %s to %s tokens (%.0fM)\n",
				acct.Email, formatWithCommas(limit), float64(limit)/1_000_000)
			return nil
		},
	}
}

// parseTokenLimit converts a human-friendly token count ("6700M", "6.7B",
// "1.5K", "6700000000") into an absolute integer.
func parseTokenLimit(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := float64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1_000, s[:len(s)-1]
	case strings.HasSuffix(s, "M"):
		mult, s = 1_000_000, s[:len(s)-1]
	case strings.HasSuffix(s, "B"), strings.HasSuffix(s, "G"):
		mult, s = 1_000_000_000, s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, errors.New("must be non-negative")
	}
	return int64(f * mult), nil
}

// ── shared helpers ────────────────────────────────────────────────────────

// activeAccountLabel returns the active account's "email (org)" label, or
// "unknown" when nothing is active.
func activeAccountLabel() string {
	email := currentEmail()
	if email == "" {
		return "unknown"
	}
	seq, err := account.LoadSequence(sequencePath())
	if err != nil {
		return email
	}
	id := account.HashEmail(email)
	acct, ok := seq.Accounts[id]
	if !ok {
		return email
	}
	return fmt.Sprintf("%s (%s)", email, displayOrg(acct.OrgName))
}

// activeWeeklyLimit returns the active account's configured weekly token
// limit, or 0 when none is set.
func activeWeeklyLimit() int64 {
	seq, err := account.LoadSequence(sequencePath())
	if err != nil {
		return 0
	}
	id := activeID(seq)
	if id == "" {
		return 0
	}
	return seq.Accounts[id].WeeklyTokenLimit
}

// formatWithCommas renders an integer with thousands separators.
func formatWithCommas(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// minFloat returns the smaller of two float64 values.
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
