// Package onepassword implements a 1Password Connect credential backend.
// This file defines the custom http.RoundTripper that injects authentication
// headers so that tokens never appear in URLs or query strings.
package onepassword

import (
	"net/http"
)

// authTransport is an http.RoundTripper that adds 1Password Connect and
// optional Cloudflare Access headers to every outbound request.
type authTransport struct {
	base                 http.RoundTripper
	bearerToken          string
	cfAccessClientID     string
	cfAccessClientSecret string
}

// RoundTrip clones the request, injects auth headers, and delegates to the
// underlying transport. Tokens are never placed in the URL or query string.
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so we never mutate the caller's request.
	r := req.Clone(req.Context())
	r.Header.Set("X-OP-Token", t.bearerToken)
	if t.cfAccessClientID != "" {
		r.Header.Set("CF-Access-Client-Id", t.cfAccessClientID)
	}
	if t.cfAccessClientSecret != "" {
		r.Header.Set("CF-Access-Client-Secret", t.cfAccessClientSecret)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}
