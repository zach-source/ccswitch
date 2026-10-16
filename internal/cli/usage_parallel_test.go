package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
)

// TestCollectUsage_ParallelAndOrdered checks accounts are queried
// concurrently (wall time ≈ one slow request, not the sum) and rows still
// come back in sequence order.
func TestCollectUsage_ParallelAndOrdered(t *testing.T) {
	newTestHome(t)
	const delay = 300 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer AT-")
		_, _ = fmt.Fprintf(w, `{"five_hour":{"utilization":%s},"seven_day":{"utilization":0}}`, tok)
	}))
	defer srv.Close()
	prev := anthropicUsageURL
	anthropicUsageURL = srv.URL
	defer func() { anthropicUsageURL = prev }()

	store := inmem.New()
	seq := &account.Sequence{Version: account.SchemaVersion, Accounts: map[string]account.Account{}}
	future := time.Now().Add(2 * time.Hour).UnixMilli()
	for i := 1; i <= 4; i++ {
		id, email := fmt.Sprint(i), fmt.Sprintf("u%d@example.com", i)
		seq.Accounts[id] = account.Account{Email: email}
		seq.Sequence = append(seq.Sequence, id)
		cred := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT-%d","refreshToken":"RT","expiresAt":%d}}`, i*10, future)
		if err := store.Write(context.Background(), account.BackupCredKey(id, email), []byte(cred)); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	rows := collectUsage(context.Background(), store, inmem.New(), seq, 0, false)
	if el := time.Since(start); el > 3*delay {
		t.Errorf("collectUsage took %v for 4 accounts; want ~%v (parallel)", el, delay)
	}
	for i, r := range rows {
		if r.Status != "ok" || r.FiveHour.Utilization != float64((i+1)*10) {
			t.Errorf("row %d = %+v, want ok with utilization %d", i, r, (i+1)*10)
		}
	}
}

// TestUsageForAccount_StatusByHTTPCode pins that only a rejected token reads
// as "expired"; a 429 is "unavailable", which autoswitch must not act on.
func TestUsageForAccount_StatusByHTTPCode(t *testing.T) {
	for code, want := range map[int]string{401: "expired", 403: "expired", 429: "unavailable", 500: "unavailable"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		prev := anthropicUsageURL
		anthropicUsageURL = srv.URL

		store := inmem.New()
		email := "a@example.com"
		cred := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT","refreshToken":"RT","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
		if err := store.Write(context.Background(), account.BackupCredKey("a", email), []byte(cred)); err != nil {
			t.Fatal(err)
		}
		row, _ := usageForAccount(context.Background(), store, inmem.New(), "a", account.Account{Email: email}, false, 0)

		anthropicUsageURL = prev
		srv.Close()
		if row.Status != want {
			t.Errorf("HTTP %d: status %q, want %q", code, row.Status, want)
		}
	}
}
