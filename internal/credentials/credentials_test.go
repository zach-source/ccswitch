package credentials

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseAndExpiry(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UnixMilli()
	blob := []byte(`{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt","expiresAt":` +
		strconv.FormatInt(future, 10) + `,"scopes":["openid"],"subscriptionType":"max"}}`)

	c, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.ClaudeAIOAuth.AccessToken != "at" || c.ClaudeAIOAuth.RefreshToken != "rt" {
		t.Error("Parse lost a token field")
	}
	if c.IsExpired(time.Minute) {
		t.Error("a token 2h out must not read as expired against a 1m buffer")
	}
	if !c.IsExpired(3 * time.Hour) {
		t.Error("a token 2h out must read as expired against a 3h buffer")
	}
	if hl := c.HoursLeft(); hl < 1.5 || hl > 2.5 {
		t.Errorf("HoursLeft = %.2f, want ~2", hl)
	}
}

// TestMarshalIsLossy pins the central data-loss hazard: Credentials is a
// typed inspection lens, not a faithful mirror of .credentials.json. A
// parse→marshal round-trip silently drops any field the struct does not
// model. This is *why* every storage path (sync, refresh-all, usage-all)
// must persist the original raw bytes — never Marshal output. If this test
// ever fails, the struct gained a catch-all and the raw-bytes rule could be
// revisited; until then, do not store Marshal output.
func TestMarshalIsLossy(t *testing.T) {
	// A blob with a field Claude Code could add tomorrow that the struct
	// does not model.
	blob := []byte(`{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt",` +
		`"expiresAt":0},"futureField":"must-not-vanish-from-storage"}`)

	c, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	round, err := c.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(round), "futureField") {
		t.Fatal("Marshal unexpectedly preserved an unmodeled field — " +
			"the lossiness assumption changed; review storage paths")
	}
	// The unmodeled field is gone after a round-trip: storing Marshal output
	// here would have lost it. Raw-byte storage is mandatory.
}
