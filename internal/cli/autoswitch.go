package cli

import (
	"fmt"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newAutoswitchCmd)
}

// unusableLoad stands in for the active account's load when it can't be
// measured (missing from the usage rows, or its usage query came back
// expired/error). Treating it as "worse than anything" makes the hysteresis
// gate in decideAutoSwitch trivially satisfied by any usable candidate under
// threshold, instead of needing a separate unmeasurable-active code path.
const unusableLoad = 100.0

func newAutoswitchCmd() *cobra.Command {
	var (
		once       bool
		threshold  float64
		hysteresis float64
		cooldown   time.Duration
		strategy   string
		interval   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "autoswitch",
		Short: "Automatically switch away from the active account when it nears its rate limit",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strategy != "best" && strategy != "next-available" {
				return fmt.Errorf("invalid --strategy %q (want best or next-available)", strategy)
			}

			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			opts := autoswitchOpts{threshold: threshold, hysteresis: hysteresis, cooldown: cooldown, strategy: strategy}

			if once {
				_, err := evaluateAutoswitch(cmd, cfg, opts)
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				if _, err := evaluateAutoswitch(cmd, cfg, opts); err != nil {
					fmt.Fprintf(os.Stderr, "autoswitch: %v\n", err)
				}
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
				}
			}
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Evaluate a single time and exit, instead of looping (for cron/launchd)")
	cmd.Flags().Float64Var(&threshold, "threshold", 80, "Utilization percentage at which the active account is considered over its limit")
	cmd.Flags().Float64Var(&hysteresis, "hysteresis", 10, "Minimum percentage-point improvement a candidate must offer over the active account before switching")
	cmd.Flags().DurationVar(&cooldown, "cooldown", 5*time.Minute, "Minimum time since the last recorded switch before autoswitch will switch again")
	cmd.Flags().StringVar(&strategy, "strategy", "best", "Candidate selection strategy: best (lowest load) or next-available (first under threshold in sequence order)")
	cmd.Flags().DurationVar(&interval, "interval", 60*time.Second, "Evaluation interval when not run with --once")
	return cmd
}

// autoswitchOpts bundles the tunables of one evaluation so
// evaluateAutoswitch's signature doesn't grow every time a flag is added.
type autoswitchOpts struct {
	threshold  float64
	hysteresis float64
	cooldown   time.Duration
	strategy   string
}

// evaluateAutoswitch runs one decision cycle: load state, collect usage,
// decide, and — if decideAutoSwitch picked a target — perform the switch.
// It prints exactly one decision line and returns the chosen target ID (""
// when no switch happened).
func evaluateAutoswitch(cmd *cobra.Command, cfg *config.Config, opts autoswitchOpts) (string, error) {
	seq, err := account.LoadSequence(sequencePath())
	if err != nil {
		return "", err
	}
	if len(seq.Sequence) == 0 {
		return "", fmt.Errorf("no accounts are managed yet")
	}

	rows, err := usageRows(cmd, cfg, seq)
	if err != nil {
		return "", err
	}

	active := activeID(seq)
	cooldownActive := withinCooldown(seq, opts.cooldown)

	targetID, reason := decideAutoSwitch(rows, active, opts.threshold, opts.hysteresis, opts.strategy, cooldownActive)
	if targetID == "" {
		fmt.Printf("no action: %s\n", reason)
		return "", nil
	}

	activeLoad, activeOK := loadOf(rows, active)
	targetLoad, _ := loadOf(rows, targetID)
	activeLabel := "none"
	if activeOK {
		activeLabel = fmt.Sprintf("#%s %.0f%%", active, activeLoad)
	} else if active != "" {
		activeLabel = fmt.Sprintf("#%s unusable", active)
	}
	fmt.Printf("autoswitch: %s -> #%s %.0f%%\n", activeLabel, targetID, targetLoad)

	if err := performSwitch(cmd, cfg, seq, targetID); err != nil {
		return "", fmt.Errorf("switch to %s: %w", targetID, err)
	}
	return targetID, nil
}

// withinCooldown reports whether seq's most recent switch happened less
// than cooldown ago. A missing or unparseable log entry is treated as "not
// in cooldown" — silently refusing to ever switch on bad data would be
// worse than occasionally switching a little early.
func withinCooldown(seq *account.Sequence, cooldown time.Duration) bool {
	if len(seq.SwitchLog) == 0 || cooldown <= 0 {
		return false
	}
	last := seq.SwitchLog[len(seq.SwitchLog)-1]
	ts, err := time.Parse(time.RFC3339, last.Timestamp)
	if err != nil {
		return false
	}
	return time.Since(ts) < cooldown
}

// loadOf returns the load (max of 5h/7d utilization) of the row with the
// given id, and whether that row exists and is usable (Status == "ok").
func loadOf(rows []accountUsage, id string) (float64, bool) {
	for _, r := range rows {
		if r.ID == id {
			return rowLoad(r), r.Status == "ok"
		}
	}
	return 0, false
}

// rowLoad is the pure "how full is this account" figure decideAutoSwitch
// compares against threshold and hysteresis: the worse of the 5-hour and
// 7-day utilization windows. Callers must check Status == "ok" first — an
// expired/error row has no utilization data and rowLoad returns 0 for it,
// which would look artificially safe.
func rowLoad(r accountUsage) float64 {
	load := 0.0
	if r.FiveHour != nil {
		load = math.Max(load, r.FiveHour.Utilization)
	}
	if r.SevenDay != nil {
		load = math.Max(load, r.SevenDay.Utilization)
	}
	return load
}

// decideAutoSwitch is the pure decision core of autoswitch: given the
// current usage snapshot, it decides whether to switch away from the
// active account and, if so, to whom. It touches no I/O so every rule
// (threshold, hysteresis, cooldown, strategy) can be table-tested directly
// without mocking usage collection or performSwitch.
//
// Returns ("", reason) when no switch should happen; (targetID, "") when
// one should.
func decideAutoSwitch(rows []accountUsage, activeID string, threshold, hysteresis float64, strategy string, cooldownActive bool) (targetID, reason string) {
	activeLoad := unusableLoad
	activeUsable := false
	for _, r := range rows {
		if r.ID == activeID && r.Status == "ok" {
			activeLoad = rowLoad(r)
			activeUsable = true
			break
		}
	}

	if activeUsable && activeLoad < threshold {
		return "", fmt.Sprintf("active #%s at %.0f%%", activeID, activeLoad)
	}

	if cooldownActive {
		return "", "cooldown active"
	}

	candidates := make([]accountUsage, 0, len(rows))
	for _, r := range rows {
		if r.ID == activeID || r.Status != "ok" {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return "", "no usable candidate accounts"
	}

	var picked accountUsage
	found := false
	switch strategy {
	case "next-available":
		for _, c := range candidates {
			if rowLoad(c) < threshold {
				picked, found = c, true
				break
			}
		}
	default: // "best"
		bestLoad := math.Inf(1)
		for _, c := range candidates {
			l := rowLoad(c)
			if l < bestLoad {
				bestLoad, picked, found = l, c, true
			}
		}
	}
	if !found {
		return "", "no candidate under threshold"
	}

	targetLoad := rowLoad(picked)
	if targetLoad >= threshold {
		return "", fmt.Sprintf("best candidate #%s still at %.0f%% (>= threshold)", picked.ID, targetLoad)
	}
	if targetLoad > activeLoad-hysteresis {
		return "", fmt.Sprintf("candidate #%s at %.0f%% does not clear hysteresis (active %.0f%%, need <= %.0f%%)",
			picked.ID, targetLoad, activeLoad, activeLoad-hysteresis)
	}

	return picked.ID, ""
}
