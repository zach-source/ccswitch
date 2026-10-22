package cli

// Reasons decideCodexSwitch gives when it takes no action. The strings are
// part of the autoswitch output contract.
const (
	noActionNoMatch  = "live login matches no profile"
	noActionLiveDown = "live usage unavailable"
	noActionCooldown = "cooldown active"
	noActionNoTarget = "no profile under threshold"
	noActionBest     = "live profile is the best choice"
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
