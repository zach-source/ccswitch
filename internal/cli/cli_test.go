package cli

import (
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeLegacyArgs(t *testing.T) {
	root := Root()
	tests := []struct {
		name string
		in   []string
		want string // expected first arg, "" means empty slice
	}{
		{"legacy switch-to rewritten", []string{"--switch-to", "2"}, "switch-to"},
		{"legacy sync rewritten", []string{"--sync", "--quiet"}, "sync"},
		{"legacy env rewritten", []string{"--env", "2"}, "env"},
		{"cobra-native untouched", []string{"switch-to", "2"}, "switch-to"},
		{"unknown flag untouched", []string{"--help"}, "--help"},
		{"bare flag untouched", []string{"--nonsense"}, "--nonsense"},
		{"empty untouched", []string{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeLegacyArgs(tt.in, root)
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("want empty, got %v", got)
				}
				return
			}
			if len(got) == 0 || got[0] != tt.want {
				t.Fatalf("want first arg %q, got %v", tt.want, got)
			}
		})
	}
}

func TestDisplayOrg(t *testing.T) {
	tests := map[string]string{
		"":                   "Personal",
		"Bob's Organization": "Personal",
		"Acme Inc":           "Acme Inc",
		"Stigen":             "Stigen",
	}
	for in, want := range tests {
		if got := displayOrg(in); got != want {
			t.Errorf("displayOrg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTokenLimit(t *testing.T) {
	ok := map[string]int64{
		"6700M":      6_700_000_000,
		"6.7B":       6_700_000_000,
		"6.7G":       6_700_000_000,
		"1.5K":       1_500,
		"100":        100,
		"6700000000": 6_700_000_000,
		"  500m  ":   500_000_000,
	}
	for in, want := range ok {
		got, err := parseTokenLimit(in)
		if err != nil {
			t.Errorf("parseTokenLimit(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseTokenLimit(%q) = %d, want %d", in, got, want)
		}
	}

	for _, bad := range []string{"abc", "-5", "", "M"} {
		if _, err := parseTokenLimit(bad); err == nil {
			t.Errorf("parseTokenLimit(%q) expected error, got nil", bad)
		}
	}
}

func TestRenderBar(t *testing.T) {
	tests := []struct {
		pct        float64
		wantFilled int
	}{
		{0, 0},
		{100, barWidth},
		{50, barWidth / 2},
		{-10, 0},        // clamped low
		{250, barWidth}, // clamped high
	}
	for _, tt := range tests {
		bar := renderBar(tt.pct)
		if n := utf8.RuneCountInString(bar); n != barWidth {
			t.Errorf("renderBar(%v) width = %d runes, want %d", tt.pct, n, barWidth)
		}
		filled := 0
		for _, r := range bar {
			if r == '█' {
				filled++
			}
		}
		if filled != tt.wantFilled {
			t.Errorf("renderBar(%v) filled = %d, want %d", tt.pct, filled, tt.wantFilled)
		}
	}
}

func TestPctColor(t *testing.T) {
	if pctColor(10) != ansiGreen {
		t.Error("10% should be green")
	}
	if pctColor(60) != ansiYellow {
		t.Error("60% should be yellow")
	}
	if pctColor(90) != ansiRed {
		t.Error("90% should be red")
	}
}

func TestFmtTokens(t *testing.T) {
	tests := map[float64]string{
		1_500_000: "1.5M",
		64_000:    "64K",
		900:       "900",
		0:         "0",
	}
	for in, want := range tests {
		if got := fmtTokens(in); got != want {
			t.Errorf("fmtTokens(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatWithCommas(t *testing.T) {
	tests := map[int64]string{
		0:             "0",
		100:           "100",
		1_000:         "1,000",
		6_700_000_000: "6,700,000,000",
	}
	for in, want := range tests {
		if got := formatWithCommas(in); got != want {
			t.Errorf("formatWithCommas(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShortModels(t *testing.T) {
	in := []string{"claude-opus-4-7-20251101", "claude-haiku-4-5-20251001", "sonnet-4-6"}
	want := []string{"opus-4-7", "haiku-4-5", "sonnet-4-6"}
	got := shortModels(in)
	if len(got) != len(want) {
		t.Fatalf("shortModels len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("shortModels[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTimeUntil(t *testing.T) {
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if got := timeUntil(past); got != "resetting" {
		t.Errorf("timeUntil(past) = %q, want resetting", got)
	}
	if got := timeUntil(""); got != "" {
		t.Errorf("timeUntil(empty) = %q, want empty", got)
	}
	if got := timeUntil("not-a-time"); got != "" {
		t.Errorf("timeUntil(garbage) = %q, want empty", got)
	}
	future := time.Now().Add(3 * time.Hour).Format(time.RFC3339)
	if got := timeUntil(future); got == "" || got == "resetting" {
		t.Errorf("timeUntil(future) = %q, want a duration", got)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	// Missing file: not an error, ok=false.
	m, ok, err := readSettings(path)
	if err != nil {
		t.Fatalf("readSettings(missing) error: %v", err)
	}
	if ok {
		t.Error("readSettings(missing) ok should be false")
	}

	m["env"] = map[string]any{"ANTHROPIC_BASE_URL": "https://example.test"}
	if err := writeSettings(path, m); err != nil {
		t.Fatalf("writeSettings error: %v", err)
	}

	got, ok, err := readSettings(path)
	if err != nil || !ok {
		t.Fatalf("readSettings(written) err=%v ok=%v", err, ok)
	}
	env, _ := got["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://example.test" {
		t.Errorf("round-trip lost env value: %#v", got)
	}
}
