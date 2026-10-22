package cli

import (
	"testing"

	"github.com/zach-source/ccswitch/internal/codex"
)

func TestDecideCodexSwitch(t *testing.T) {
	row := func(label, status string, pct float64) codexUsageRow {
		r := codexUsageRow{Label: label, Status: status}
		if status == usageOK {
			r.Windows = []codex.Window{{Seconds: 604800, UsedPercent: pct}}
		}
		return r
	}
	prio := []string{"a", "b", "c"}
	tests := []struct {
		name       string
		rows       []codexUsageRow
		live       string
		cooldown   bool
		wantTarget string
		wantReason string
	}{
		{"switch to first under threshold",
			[]codexUsageRow{row("a", usageOK, 90), row("b", usageOK, 85), row("c", usageOK, 10)},
			"a", false, "c", ""},
		{"switch to higher priority profile",
			[]codexUsageRow{row("a", usageOK, 10), row("b", usageOK, 50), row("c", usageOK, 10)},
			"b", false, "a", ""},
		{"skip profiles that are not ok",
			[]codexUsageRow{row("a", usageOK, 90), row("b", usageStale, 0), row("c", usageOK, 10)},
			"a", false, "c", ""},
		{"threshold is exclusive",
			[]codexUsageRow{row("a", usageOK, 90), row("b", usageOK, 80), row("c", usageOK, 79)},
			"a", false, "c", ""},
		{"live usage unavailable",
			[]codexUsageRow{row("a", usageUnavailable, 0), row("b", usageOK, 10)},
			"a", false, "", "live usage unavailable"},
		{"live token expired",
			[]codexUsageRow{row("a", usageExpired, 0), row("b", usageOK, 10)},
			"a", false, "", "live usage unavailable"},
		{"live profile missing from rows",
			[]codexUsageRow{row("b", usageOK, 10)},
			"a", false, "", "live usage unavailable"},
		{"cooldown active",
			[]codexUsageRow{row("a", usageOK, 90), row("b", usageOK, 10)},
			"a", true, "", "cooldown active"},
		{"no profile under threshold",
			[]codexUsageRow{row("a", usageOK, 90), row("b", usageOK, 95), row("c", usageUnavailable, 0)},
			"a", false, "", "no profile under threshold"},
		{"live is the best choice",
			[]codexUsageRow{row("a", usageOK, 10), row("b", usageOK, 5)},
			"a", false, "", "live profile is the best choice"},
		{"live login matches no profile",
			[]codexUsageRow{row("a", usageOK, 10)},
			"", false, "", "live login matches no profile"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, reason := decideCodexSwitch(tc.rows, tc.live, prio, 80, tc.cooldown)
			if target != tc.wantTarget || reason != tc.wantReason {
				t.Errorf("got (%q, %q), want (%q, %q)", target, reason, tc.wantTarget, tc.wantReason)
			}
		})
	}
}
