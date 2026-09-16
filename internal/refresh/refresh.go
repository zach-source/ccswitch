// Package refresh renews Claude Code OAuth credentials.
//
// Two paths, deliberately different:
//
//   - RefreshOne / RefreshAll are non-interactive. They perform the OAuth2
//     refresh_token grant over HTTP directly (see oauth.go), so they need no
//     `claude` binary, no subprocess, and no keychain access beyond reading
//     and writing the account's own stored blob.
//   - LoginRotate is interactive. A genuine re-login needs a browser, so it
//     drives `claude auth login` and captures whatever credential that wrote
//     (see login.go).
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/credentials"
)

// seedJSON is written to the isolated config dir so that `claude auth login`
// sees a plausible existing installation and skips interactive onboarding.
const seedJSON = `{"hasCompletedOnboarding":true}`

// RefreshOne exchanges the refresh token in blob for a fresh credential blob,
// by performing the OAuth2 refresh_token grant directly against the token
// endpoint — the same request `claude` makes internally.
//
// blob is the raw stored credential; the returned bytes are that blob with
// only the token fields replaced, so every key this package does not model —
// inside claudeAiOauth and beside it — is preserved verbatim.
//
// Doing the grant in-process replaced an earlier design that shelled out to
// `claude auth login` with the token in the environment and then hunted for
// wherever claude had written the result: a tmpdir, the active keychain slot,
// or a per-CLAUDE_CONFIG_DIR "Claude Code-credentials-<hash>" record found by
// enumerating the keychain and filtering on modification date. That hunt was
// unavoidably racy — a concurrent `claude` refreshing a different account
// could win the date filter — and it orphaned a keychain record per run.
//
// Refresh tokens rotate. A successful exchange can invalidate the token that
// was sent, so the caller must persist the returned bytes; dropping them
// strands the account until an interactive re-login.
func RefreshOne(ctx context.Context, blob []byte) ([]byte, *credentials.Credentials, error) {
	cred, err := credentials.Parse(blob)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: %w", err)
	}
	refreshToken := cred.ClaudeAIOAuth.RefreshToken
	if refreshToken == "" {
		return nil, nil, errors.New("refresh: no refresh token present in credentials")
	}

	tr, err := exchangeRefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: %w", err)
	}

	newData, err := applyToken(blob, tr, time.Now())
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: %w", err)
	}
	newCred, err := credentials.Parse(newData)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: parse refreshed credentials: %w", err)
	}
	return newData, newCred, nil
}

// RefreshAll iterates every account in seq, refreshes those whose credentials
// are within expiryBuffer of expiry, and writes the result to b.
//
// It returns the number of accounts successfully refreshed. If one or more
// accounts hit a hard error (read/parse/refresh/write failure) it also
// returns a non-nil error so a non-interactive caller — a cron or launchd
// wrapper — can detect the partial failure via a non-zero exit code. An
// account with no stored credentials is a skip, not a failure.
func RefreshAll(
	ctx context.Context,
	seq *account.Sequence,
	b backend.Backend,
	expiryBuffer time.Duration,
	log *slog.Logger,
) (int, error) {
	if log == nil {
		log = slog.Default()
	}

	refreshed := 0
	failed := 0
	for _, id := range seq.IDs() {
		acct, ok := seq.Accounts[id]
		if !ok {
			continue
		}

		key := account.BackupCredKey(id, acct.Email)
		data, err := b.Read(ctx, key)
		if err != nil {
			if errors.Is(err, backend.ErrNotFound) {
				log.Warn("refresh-all: no credentials for account", "id", id, "email", acct.Email)
				continue
			}
			log.Error("refresh-all: read failed", "id", id, "err", err)
			failed++
			continue
		}

		cred, err := credentials.Parse(data)
		if err != nil {
			log.Error("refresh-all: parse failed", "id", id, "err", err)
			failed++
			continue
		}

		if !cred.IsExpired(expiryBuffer) {
			log.Info("refresh-all: token still valid", "id", id, "hours_left", fmt.Sprintf("%.1f", cred.HoursLeft()))
			continue
		}

		log.Info("refresh-all: refreshing", "id", id, "email", acct.Email)
		newData, newCred, err := RefreshOne(ctx, data)
		if err != nil {
			log.Error("refresh-all: refresh failed", "id", id, "err", err)
			failed++
			continue
		}

		// Persist the raw credential bytes verbatim — no struct round-trip.
		if err := b.Write(ctx, key, newData); err != nil {
			log.Error("refresh-all: write failed", "id", id, "err", err)
			failed++
			continue
		}

		log.Info("refresh-all: refreshed", "id", id, "email", acct.Email, "hours_left", fmt.Sprintf("%.1f", newCred.HoursLeft()))
		refreshed++
	}

	if failed > 0 {
		return refreshed, fmt.Errorf("refresh-all: %d account(s) failed to refresh", failed)
	}
	return refreshed, nil
}
