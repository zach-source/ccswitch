package cli

import (
	"strings"
	"testing"
)

func TestUsageBarLine_NoResetText(t *testing.T) {
	if got := usageBarLine("5h", 0, ""); strings.Contains(got, "resets") {
		t.Errorf("unknown reset must print no reset text: %q", got)
	}
	if got := usageBarLine("7d", 96, "22h 21m"); !strings.Contains(got, "resets in 22h 21m") {
		t.Errorf("missing reset text: %q", got)
	}
}
