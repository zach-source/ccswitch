package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/codex"
)

// Reasons decideCodexSwitch gives when it takes no action. The strings are
// part of the autoswitch output contract.
const (
	noActionNoMatch  = "live login matches no profile"
	noActionLiveDown = "live usage unavailable"
	noActionCooldown = "cooldown active"
	noActionNoTarget = "no profile under threshold"
	noActionBest     = "live profile is the best choice"
	noActionLocked   = "another codex autoswitch is running"
)

// decideCodexSwitch picks the profile autoswitch moves to. It returns the
// target label, or an empty target and the reason for no action. A profile
// qualifies when its status is ok and its load is under threshold; the first
// one in priority order wins.
func decideCodexSwitch(rows []codexUsageRow, liveLabel string, priority []string, threshold float64, cooldownActive bool) (target, reason string) {
	if liveLabel == "" {
		return "", noActionNoMatch
	}
	byLabel := make(map[string]codexUsageRow, len(rows))
	for _, r := range rows {
		byLabel[r.Label] = r
	}
	if live, ok := byLabel[liveLabel]; !ok || live.Status != usageOK {
		return "", noActionLiveDown
	}
	if cooldownActive {
		return "", noActionCooldown
	}
	for _, label := range priority {
		if r, ok := byLabel[label]; ok && r.Status == usageOK && r.Load() < threshold {
			if label == liveLabel {
				return "", noActionBest
			}
			return label, ""
		}
	}
	return "", noActionNoTarget
}

func newCodexAutoswitchCmd() *cobra.Command {
	var once bool
	var threshold float64
	var cooldown time.Duration
	cmd := &cobra.Command{
		Use:   "autoswitch --once",
		Short: "Switch the live Codex login when it nears its rate limit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("autoswitch has no loop mode; pass --once")
			}
			out := cmd.OutOrStdout()
			// One run at a time: a hook starts one per session.
			unlock, locked, err := lockFile("codex-autoswitch.lock")
			if err != nil {
				return err
			}
			if !locked {
				fmt.Fprintln(out, "no action: "+noActionLocked)
				return nil
			}
			defer unlock()
			return runCodexAutoswitch(cmd.Context(), out, threshold, cooldown)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Evaluate a single time and exit")
	cmd.Flags().Float64Var(&threshold, "threshold", 80, "Usage percentage at which a profile counts as over its limit")
	cmd.Flags().DurationVar(&cooldown, "cooldown", 5*time.Minute, "Minimum time since the last switch before switching again")
	return cmd
}

// runCodexAutoswitch decides once and switches when decideCodexSwitch names a
// target. It prints one line.
func runCodexAutoswitch(ctx context.Context, out io.Writer, threshold float64, cooldown time.Duration) error {
	b, err := codexStoreFunc()
	if err != nil {
		return err
	}
	local, localErr := resolveLocalBackend()
	if localErr != nil {
		local = nil
	}
	reg, err := codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		return err
	}
	live, err := codex.ReadLive()
	if err != nil && !errors.Is(err, codex.ErrNoLiveLogin) {
		return fmt.Errorf("read the live Codex login: %w", err)
	}
	liveLabel := ""
	if len(live) > 0 {
		liveLabel, _ = codex.MatchProfile(reg, live)
	}
	now := time.Now()
	rows := collectCodexUsage(ctx, reg, b, local, live, now)
	cooldownActive := now.Sub(reg.LastSwitch) < cooldown
	target, reason := decideCodexSwitch(rows, liveLabel, reg.Ordered(), threshold, cooldownActive)
	if target == "" {
		fmt.Fprintln(out, "no action: "+reason)
		return nil
	}
	if err := useCodexProfile(ctx, b, local, reg, target); err != nil {
		return err
	}
	// useCodexProfile saved the registry. Reload it so this save keeps what
	// the switch wrote.
	reg, err = codex.LoadRegistry(codexRegistryPath())
	if err != nil {
		return err
	}
	reg.LastSwitch = now
	if err := reg.Save(codexRegistryPath()); err != nil {
		return err
	}
	fmt.Fprintf(out, "autoswitch: %s %.0f%% -> %s %.0f%%\n", liveLabel, codexLoadOf(rows, liveLabel), target, codexLoadOf(rows, target))
	return nil
}

// codexLoadOf returns the load of the row named label.
func codexLoadOf(rows []codexUsageRow, label string) float64 {
	for _, r := range rows {
		if r.Label == label {
			return r.Load()
		}
	}
	return 0
}
