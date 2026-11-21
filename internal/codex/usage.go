package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
)

// defaultUsageURL is the endpoint the ChatGPT web app and Codex CLI use to
// show rate-limit usage. It is not a public API, so every field is optional
// on our side and a change upstream shows as "unavailable", not a crash.
// CCSWITCH_CODEX_USAGE_URL overrides it for tests.
const defaultUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// ErrUsageRejected means the server refused the access token (HTTP 401 or
// 403). The user must sign in again; a retry cannot help. Every other
// failure is a plain error, because the account may be fine.
var ErrUsageRejected error = &kindError{"codex: usage request rejected, sign in again", provider.ErrUsageRejected}

// usageWindow is the wire form of a window. A pointer in the parent tells
// null from present.
type usageWindow struct {
	UsedPercent float64 `json:"used_percent"`
	Seconds     int     `json:"limit_window_seconds"`
	ResetAt     int64   `json:"reset_at"`
}

type usageResponse struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Primary   *usageWindow `json:"primary_window"`
		Secondary *usageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

// FetchUsage reads the account's current usage. accountID goes in the
// ChatGPT-Account-Id header when it is not empty. Errors never carry the
// token or the response body.
func FetchUsage(ctx context.Context, accessToken, accountID string) (Usage, error) {
	url := defaultUsageURL
	if v := os.Getenv("CCSWITCH_CODEX_USAGE_URL"); v != "" {
		url = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Usage{}, errors.New("codex: build usage request")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "ccswitch")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		// The *url.Error text holds the URL, not the headers, but a generic
		// message keeps any future change from leaking more.
		return Usage{}, errors.New("codex: usage request failed (network error)")
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return Usage{}, fmt.Errorf("%w (HTTP %d)", ErrUsageRejected, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return Usage{}, fmt.Errorf("codex: usage request failed (HTTP %d)", resp.StatusCode)
	}

	var body usageResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Usage{}, errors.New("codex: usage response is not valid JSON")
	}
	return body.toUsage(), nil
}

// toUsage converts the wire form, skipping null windows.
func (r usageResponse) toUsage() Usage {
	u := Usage{Plan: r.PlanType}
	if r.RateLimit == nil {
		return u
	}
	for _, w := range []*usageWindow{r.RateLimit.Primary, r.RateLimit.Secondary} {
		if w == nil {
			continue
		}
		win := Window{Seconds: w.Seconds, UsedPercent: w.UsedPercent}
		if w.ResetAt > 0 {
			win.ResetAt = time.Unix(w.ResetAt, 0)
		}
		u.Windows = append(u.Windows, win)
	}
	return u
}
