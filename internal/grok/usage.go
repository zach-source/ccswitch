package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zach-source/ccswitch/internal/provider"
)

// usageURL is the billing endpoint of the Grok CLI (checked live: HTTP 200;
// the path without /v1 is 404). Tests replace it.
var usageURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"

// clientVersion is the Grok CLI version the endpoint expects in a header.
const clientVersion = "1.0.77"

type usageResponse struct {
	Config *struct {
		CreditUsagePercent *float64 `json:"creditUsagePercent"`
		CurrentPeriod      *struct {
			Type string    `json:"type"`
			End  time.Time `json:"end"`
		} `json:"currentPeriod"`
	} `json:"config"`
	// SubscriptionTier is a fallback: the live reply has no such field.
	SubscriptionTier string `json:"subscription_tier"`
}

// FetchUsage reads the credit usage of the account. The plan is the "tier"
// claim of the access token, read without verifying the signature: it is
// display text for a token ccswitch already holds. Errors never carry the
// token or the response body.
func FetchUsage(ctx context.Context, accessToken string) (provider.Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return provider.Usage{}, errors.New("grok: build usage request")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "ccswitch")
	req.Header.Set("x-grok-client-mode", "cli")
	req.Header.Set("x-grok-client-version", clientVersion)
	resp, err := httpClient.Do(req)
	if err != nil {
		return provider.Usage{}, errors.New("grok: usage request failed (network error)")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return provider.Usage{}, fmt.Errorf("%w (HTTP %d)", provider.ErrUsageRejected, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return provider.Usage{}, fmt.Errorf("grok: usage request failed (HTTP %d)", resp.StatusCode)
	}
	var body usageResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return provider.Usage{}, errors.New("grok: usage response is not valid JSON")
	}
	u := body.toUsage()
	if tier := jwtTier(accessToken); tier != "" {
		u.Plan = tier
	}
	return u, nil
}

// jwtTier reads the "tier" claim of a JWT, or "" when it is absent.
func jwtTier(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Tier string `json:"tier"`
	}
	_ = json.Unmarshal(payload, &c)
	return c.Tier
}

// toUsage maps the reply to one window named after the billing period.
func (r usageResponse) toUsage() provider.Usage {
	u := provider.Usage{Plan: r.SubscriptionTier}
	if r.Config == nil || r.Config.CreditUsagePercent == nil {
		return u
	}
	w := provider.Window{UsedPercent: *r.Config.CreditUsagePercent, Label: "credits"}
	if p := r.Config.CurrentPeriod; p != nil {
		if name := strings.ToLower(strings.TrimPrefix(p.Type, "USAGE_PERIOD_TYPE_")); name != "" {
			w.Label = name
		}
		switch w.Label {
		case "weekly":
			w.Seconds = 604800
		case "daily":
			w.Seconds = 86400
		}
		w.ResetAt = p.End
	}
	u.Windows = []provider.Window{w}
	return u
}
