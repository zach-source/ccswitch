package cli

import "testing"

// TestIsLimited table-tests the pure --skip-limited gate used by both
// switch and switch-to.
func TestIsLimited(t *testing.T) {
	tests := []struct {
		name      string
		row       accountUsage
		threshold float64
		want      bool
	}{
		{
			name:      "well under threshold",
			row:       accountUsage{Status: "ok", FiveHour: &oauthSlice{Utilization: 10}, SevenDay: &oauthSlice{Utilization: 20}},
			threshold: 95,
			want:      false,
		},
		{
			name:      "five hour at threshold",
			row:       accountUsage{Status: "ok", FiveHour: &oauthSlice{Utilization: 95}, SevenDay: &oauthSlice{Utilization: 10}},
			threshold: 95,
			want:      true,
		},
		{
			name:      "seven day above threshold",
			row:       accountUsage{Status: "ok", FiveHour: &oauthSlice{Utilization: 10}, SevenDay: &oauthSlice{Utilization: 99}},
			threshold: 95,
			want:      true,
		},
		{
			name:      "expired status is never limited (no data to judge)",
			row:       accountUsage{Status: "expired"},
			threshold: 95,
			want:      false,
		},
		{
			name:      "error status is never limited",
			row:       accountUsage{Status: "error"},
			threshold: 95,
			want:      false,
		},
		{
			name:      "custom lower threshold",
			row:       accountUsage{Status: "ok", FiveHour: &oauthSlice{Utilization: 60}},
			threshold: 50,
			want:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLimited(tt.row, tt.threshold); got != tt.want {
				t.Errorf("isLimited() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDecideAutoSwitch table-tests the pure decision core of `autoswitch`.
func TestDecideAutoSwitch(t *testing.T) {
	ok := func(id string, fiveHour, sevenDay float64) accountUsage {
		return accountUsage{ID: id, Status: "ok", FiveHour: &oauthSlice{Utilization: fiveHour}, SevenDay: &oauthSlice{Utilization: sevenDay}}
	}
	bad := func(id, status string) accountUsage {
		return accountUsage{ID: id, Status: status}
	}

	tests := []struct {
		name           string
		rows           []accountUsage
		activeID       string
		threshold      float64
		hysteresis     float64
		strategy       string
		cooldownActive bool
		wantTarget     string
	}{
		{
			name:       "active under threshold stays put",
			rows:       []accountUsage{ok("a", 40, 10), ok("b", 5, 5)},
			activeID:   "a",
			threshold:  80,
			hysteresis: 10,
			strategy:   "best",
			wantTarget: "",
		},
		{
			name:       "active over threshold picks best (lowest load) candidate",
			rows:       []accountUsage{ok("a", 90, 20), ok("b", 50, 10), ok("c", 20, 5)},
			activeID:   "a",
			threshold:  80,
			hysteresis: 10,
			strategy:   "best",
			wantTarget: "c",
		},
		{
			name:       "hysteresis blocks a marginal gain",
			rows:       []accountUsage{ok("a", 85, 20), ok("b", 80, 10)},
			activeID:   "a",
			threshold:  90,
			hysteresis: 10,
			strategy:   "best",
			// b's load (80) is not <= a's load (85) - hysteresis (75), so blocked.
			wantTarget: "",
		},
		{
			name:           "cooldown blocks switching even when over threshold",
			rows:           []accountUsage{ok("a", 95, 20), ok("b", 10, 5)},
			activeID:       "a",
			threshold:      80,
			hysteresis:     10,
			strategy:       "best",
			cooldownActive: true,
			wantTarget:     "",
		},
		{
			name:       "next-available picks first in order under threshold, not the lowest",
			rows:       []accountUsage{ok("a", 90, 20), ok("b", 79, 10), ok("c", 5, 5)},
			activeID:   "a",
			threshold:  80,
			hysteresis: 5,
			strategy:   "next-available",
			wantTarget: "b",
		},
		{
			name:       "best differs from next-available when order and load diverge",
			rows:       []accountUsage{ok("a", 90, 20), ok("b", 79, 10), ok("c", 5, 5)},
			activeID:   "a",
			threshold:  80,
			hysteresis: 5,
			strategy:   "best",
			wantTarget: "c",
		},
		{
			name:       "no ok candidates means no switch even though active is over threshold",
			rows:       []accountUsage{ok("a", 95, 20), bad("b", "expired"), bad("c", "error")},
			activeID:   "a",
			threshold:  80,
			hysteresis: 10,
			strategy:   "best",
			wantTarget: "",
		},
		{
			name:       "active missing/unusable forces a switch consideration",
			rows:       []accountUsage{bad("a", "expired"), ok("b", 10, 5)},
			activeID:   "a",
			threshold:  80,
			hysteresis: 10,
			strategy:   "best",
			wantTarget: "b",
		},
		{
			name:       "candidate at or above threshold is never picked",
			rows:       []accountUsage{ok("a", 95, 20), ok("b", 85, 30)},
			activeID:   "a",
			threshold:  80,
			hysteresis: 5,
			strategy:   "best",
			wantTarget: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTarget, reason := decideAutoSwitch(tt.rows, tt.activeID, tt.threshold, tt.hysteresis, tt.strategy, tt.cooldownActive)
			if gotTarget != tt.wantTarget {
				t.Errorf("decideAutoSwitch() target = %q (reason %q), want %q", gotTarget, reason, tt.wantTarget)
			}
			if gotTarget == "" && reason == "" {
				t.Errorf("decideAutoSwitch() returned no target and no reason")
			}
		})
	}
}
