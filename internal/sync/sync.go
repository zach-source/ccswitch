// Package sync implements bi-directional credential synchronisation between a
// local Backend (file/keychain) and a remote Backend (1Password/Vault).
//
// Conflict resolution:
//   - Credentials: newer expiresAt wins.
//   - Sequence metadata: newer lastUpdated wins; local switchLog is preserved.
//   - Local-only → push. Remote-only → pull.
package sync

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

// Options configures Engine behaviour.
type Options struct {
	// ExpiryBuffer is forwarded to credentials.IsExpired for staleness checks.
	ExpiryBuffer time.Duration
	// Logger overrides the default slog.Default() logger.
	Logger *slog.Logger
}

// Result summarises one sync pass.
type Result struct {
	Pushed    int
	Pulled    int
	Unchanged int
	Errors    int
}

// Engine owns one bi-directional sync pass. It is safe for concurrent use
// only when Push/Pull/Run are not called simultaneously; callers (Daemon)
// must serialise calls.
type Engine struct {
	local  backend.Backend
	remote backend.Backend
	seq    *account.Sequence
	opts   Options
	log    *slog.Logger
}

// New constructs a sync Engine. seq must not be nil.
func New(local, remote backend.Backend, seq *account.Sequence, opts Options) *Engine {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		local:  local,
		remote: remote,
		seq:    seq,
		opts:   opts,
		log:    log,
	}
}

// Sequence returns the engine's in-memory sequence pointer. Useful for
// callers that want to persist after Pull/Run mutates it.
func (e *Engine) Sequence() *account.Sequence { return e.seq }

// Run performs one full bi-directional sync pass.
func (e *Engine) Run(ctx context.Context) (Result, error) {
	var res Result

	// --- Sync sequence metadata ---
	seqRes, err := e.syncSequence(ctx)
	if err != nil {
		e.log.Error("sync: sequence metadata failed", "err", err)
		res.Errors++
	} else {
		mergeResult(&res, seqRes)
	}

	// --- Sync each account's credentials ---
	for _, id := range e.seq.IDs() {
		acct, ok := e.seq.Accounts[id]
		if !ok {
			continue
		}
		cr, err := e.syncAccount(ctx, id, acct.Email)
		if err != nil {
			e.log.Error("sync: account failed", "id", id, "email", acct.Email, "err", err)
			res.Errors++
			continue
		}
		mergeResult(&res, cr)
	}

	e.log.Info("sync: pass complete",
		"pushed", res.Pushed,
		"pulled", res.Pulled,
		"unchanged", res.Unchanged,
		"errors", res.Errors,
	)
	return res, nil
}

// Push copies every account from local → remote, overwriting remote state.
func (e *Engine) Push(ctx context.Context) (Result, error) {
	var res Result

	// Push sequence metadata (strip switchLog, matching bash behaviour).
	seqData, err := account.MarshalSequence(e.seq, true)
	if err != nil {
		return res, fmt.Errorf("push: marshal sequence: %w", err)
	}
	if err := e.remote.Write(ctx, account.SequenceKey, seqData); err != nil {
		return res, fmt.Errorf("push: write sequence: %w", err)
	}
	res.Pushed++

	for _, id := range e.seq.IDs() {
		acct, ok := e.seq.Accounts[id]
		if !ok {
			continue
		}
		key := account.BackupCredKey(id, acct.Email)
		data, err := e.local.Read(ctx, key)
		if err != nil {
			if errors.Is(err, backend.ErrNotFound) {
				e.log.Warn("push: no local cred, skipping", "id", id)
				continue
			}
			e.log.Error("push: read local failed", "id", id, "err", err)
			res.Errors++
			continue
		}
		if err := e.remote.Write(ctx, key, data); err != nil {
			e.log.Error("push: write remote failed", "id", id, "err", err)
			res.Errors++
			continue
		}
		res.Pushed++
	}

	e.log.Info("push: complete", "pushed", res.Pushed, "errors", res.Errors)
	return res, nil
}

// Pull copies every account from remote → local, overwriting local state.
// Preserves the local switchLog on the sequence.
func (e *Engine) Pull(ctx context.Context) (Result, error) {
	var res Result

	// Pull sequence metadata, preserving local switchLog.
	remoteSeqData, err := e.remote.Read(ctx, account.SequenceKey)
	if err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			return res, fmt.Errorf("pull: no remote sequence; run push first")
		}
		return res, fmt.Errorf("pull: read remote sequence: %w", err)
	}
	remoteSeq, err := account.ParseSequence(remoteSeqData)
	if err != nil {
		return res, fmt.Errorf("pull: parse remote sequence: %w", err)
	}
	// Preserve local switchLog.
	remoteSeq.SwitchLog = e.seq.SwitchLog
	// Update the live pointer so subsequent credential pulls use the remote account list.
	*e.seq = *remoteSeq
	res.Pulled++

	for _, id := range e.seq.IDs() {
		acct, ok := e.seq.Accounts[id]
		if !ok {
			continue
		}
		key := account.BackupCredKey(id, acct.Email)
		data, err := e.remote.Read(ctx, key)
		if err != nil {
			if errors.Is(err, backend.ErrNotFound) {
				e.log.Warn("pull: no remote cred, skipping", "id", id)
				continue
			}
			e.log.Error("pull: read remote failed", "id", id, "err", err)
			res.Errors++
			continue
		}
		if err := e.local.Write(ctx, key, data); err != nil {
			e.log.Error("pull: write local failed", "id", id, "err", err)
			res.Errors++
			continue
		}
		res.Pulled++
	}

	e.log.Info("pull: complete", "pulled", res.Pulled, "errors", res.Errors)
	return res, nil
}

// syncSequence resolves sequence metadata between local (e.seq) and remote.
// Newest lastUpdated wins; local switchLog is always preserved.
func (e *Engine) syncSequence(ctx context.Context) (Result, error) {
	var res Result

	remoteData, err := e.remote.Read(ctx, account.SequenceKey)
	if err != nil && !errors.Is(err, backend.ErrNotFound) {
		return res, fmt.Errorf("read remote sequence: %w", err)
	}

	if errors.Is(err, backend.ErrNotFound) || len(remoteData) == 0 {
		// Remote has nothing — push local.
		data, err := account.MarshalSequence(e.seq, true)
		if err != nil {
			return res, fmt.Errorf("marshal sequence: %w", err)
		}
		if err := e.remote.Write(ctx, account.SequenceKey, data); err != nil {
			return res, fmt.Errorf("write remote sequence: %w", err)
		}
		e.log.Info("sync: no remote sequence; pushed local state")
		res.Pushed++
		return res, nil
	}

	remoteSeq, err := account.ParseSequence(remoteData)
	if err != nil {
		return res, fmt.Errorf("parse remote sequence: %w", err)
	}

	localUpdated := e.seq.LastUpdated
	remoteUpdated := remoteSeq.LastUpdated

	switch {
	case remoteUpdated > localUpdated:
		// Remote is newer — pull, preserve local switchLog.
		e.log.Info("sync: remote sequence newer; pulling", "remote", remoteUpdated, "local", localUpdated)
		remoteSeq.SwitchLog = e.seq.SwitchLog
		*e.seq = *remoteSeq
		res.Pulled++
	case localUpdated > remoteUpdated:
		// Local is newer — push (strip switchLog).
		e.log.Info("sync: local sequence newer; pushing", "local", localUpdated, "remote", remoteUpdated)
		data, err := account.MarshalSequence(e.seq, true)
		if err != nil {
			return res, fmt.Errorf("marshal sequence: %w", err)
		}
		if err := e.remote.Write(ctx, account.SequenceKey, data); err != nil {
			return res, fmt.Errorf("write remote sequence: %w", err)
		}
		res.Pushed++
	default:
		res.Unchanged++
	}

	return res, nil
}

// syncAccount resolves credentials for one account between local and remote.
// For the active account the local source-of-truth is the active slot
// (account.ActiveCredKey) and pulls write through to BOTH the active slot
// and the per-account backup slot. Non-active accounts use only the backup
// slot. Remote always uses the per-account backup slot.
func (e *Engine) syncAccount(ctx context.Context, id, email string) (Result, error) {
	var res Result
	isActive := id == e.seq.ActiveAccountID
	remoteKey := account.BackupCredKey(id, email)
	localKey := remoteKey
	if isActive {
		localKey = account.ActiveCredKey
	}

	localData, localErr := e.local.Read(ctx, localKey)
	remoteData, remoteErr := e.remote.Read(ctx, remoteKey)

	localMissing := errors.Is(localErr, backend.ErrNotFound)
	remoteMissing := errors.Is(remoteErr, backend.ErrNotFound)

	if !localMissing && localErr != nil {
		return res, fmt.Errorf("read local cred %s: %w", id, localErr)
	}
	if !remoteMissing && remoteErr != nil {
		return res, fmt.Errorf("read remote cred %s: %w", id, remoteErr)
	}

	// writePulled mirrors a freshly-pulled credential into local. For the
	// active account we update the backup slot FIRST so a failure on the
	// active-slot write leaves the backup correct; the next switch-away
	// can then recover. The reverse order would silently lose the new
	// token: a subsequent switch-away would overwrite the active slot
	// from the still-stale backup.
	writePulled := func(data []byte) error {
		if isActive {
			if err := e.local.Write(ctx, remoteKey, data); err != nil {
				return err
			}
		}
		return e.local.Write(ctx, localKey, data)
	}

	switch {
	case !localMissing && remoteMissing:
		if err := e.remote.Write(ctx, remoteKey, localData); err != nil {
			return res, fmt.Errorf("push cred %s: %w", id, err)
		}
		e.log.Debug("sync: pushed (new in remote)", "id", id, "email", email)
		res.Pushed++

	case localMissing && !remoteMissing:
		if err := writePulled(remoteData); err != nil {
			return res, fmt.Errorf("pull cred %s: %w", id, err)
		}
		e.log.Debug("sync: pulled (new locally)", "id", id, "email", email)
		res.Pulled++

	case !localMissing && !remoteMissing:
		localCred, err := credentials.Parse(localData)
		if err != nil {
			return res, fmt.Errorf("parse local cred %s: %w", id, err)
		}
		remoteCred, err := credentials.Parse(remoteData)
		if err != nil {
			return res, fmt.Errorf("parse remote cred %s: %w", id, err)
		}

		localExp := localCred.ClaudeAIOAuth.ExpiresAtMillis
		remoteExp := remoteCred.ClaudeAIOAuth.ExpiresAtMillis

		switch {
		case localExp > remoteExp:
			if err := e.remote.Write(ctx, remoteKey, localData); err != nil {
				return res, fmt.Errorf("push fresher cred %s: %w", id, err)
			}
			e.log.Debug("sync: pushed (local fresher)", "id", id, "email", email)
			res.Pushed++
		case remoteExp > localExp:
			if err := writePulled(remoteData); err != nil {
				return res, fmt.Errorf("pull fresher cred %s: %w", id, err)
			}
			e.log.Debug("sync: pulled (remote fresher)", "id", id, "email", email)
			res.Pulled++
		default:
			res.Unchanged++
		}

	default:
		res.Unchanged++
	}

	return res, nil
}

// mergeResult adds src counters into dst.
func mergeResult(dst *Result, src Result) {
	dst.Pushed += src.Pushed
	dst.Pulled += src.Pulled
	dst.Unchanged += src.Unchanged
	dst.Errors += src.Errors
}
