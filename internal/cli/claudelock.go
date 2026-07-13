package cli

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Claude Code (the npm CLI) guards its OAuth refresh and ~/.claude.json
// writes with the `proper-lockfile` package: a directory `<target>.lock`
// created with mkdir (mkdir is atomic — that's the mutex), considered stale
// once its mtime is older than staleLockAge, refreshed by the holder every
// touchInterval while held, and rmdir'd on release. ccswitch has to speak
// the same protocol on the same paths or a swap can race a running Claude
// Code's background token refresh: whichever process writes last wins, and
// a losing ccswitch write can leave the account in a half-swapped state.
const (
	staleLockAge  = 10 * time.Second
	touchInterval = 3 * time.Second
	lockTimeout   = 9 * time.Second
)

// claudeCredsLockPath and claudeConfigLockPath return the two paths Claude
// Code locks, matching proper-lockfile's `<target>.lock` convention.
func claudeCredsLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.lock"), nil
}

func claudeConfigLockPath() string {
	return claudeConfigPath() + ".lock"
}

// withClaudeLocks acquires both the credentials lock and the config lock —
// in that order, matching performSwitch's write order — runs fn, and
// releases both, LIFO, even if fn panics or returns an error. Callers must
// not perform any of steps 2-5 of a switch outside this wrapper; step 1
// (reading the target's stored creds) is safe unlocked because it doesn't
// touch anything Claude Code also writes.
func withClaudeLocks(fn func() error) error {
	credsPath, err := claudeCredsLockPath()
	if err != nil {
		return fmt.Errorf("resolve credentials lock path: %w", err)
	}
	configPath := claudeConfigLockPath()
	return withDirLocks([]string{credsPath, configPath}, fn)
}

// withDirLocks acquires a proper-lockfile-compatible directory lock for
// every path in order, runs fn, and releases every acquired lock in
// reverse order — even if fn panics. Extracted from withClaudeLocks so
// tests can point it at temp directories instead of the real ~/.claude.lock
// and ~/.claude.json.lock.
func withDirLocks(paths []string, fn func() error) error {
	held := make([]*heldLock, 0, len(paths))
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].release()
		}
	}()

	for _, p := range paths {
		lock, err := acquireDirLock(p)
		if err != nil {
			return err
		}
		held = append(held, lock)
	}

	return fn()
}

// heldLock tracks one acquired directory lock and the goroutine that keeps
// its mtime fresh while held, so other proper-lockfile-compatible holders
// (Claude Code) don't consider it stale mid-operation.
type heldLock struct {
	path     string
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// acquireDirLock takes the directory lock at path, retrying past both
// contention (someone else holds it, not yet stale) and staleness (someone
// else holds it, mtime says they died) until lockTimeout elapses.
func acquireDirLock(path string) (*heldLock, error) {
	deadline := time.Now().Add(lockTimeout)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return startTouching(path), nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire lock %s: %w", path, err)
		}

		if isStale(path) {
			// Another holder appears to have died mid-operation. Try to take
			// over; if someone else wins the race, the next mkdir attempt
			// above will just fail with ErrExist again and we loop normally.
			_ = os.Remove(path)
			continue
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"could not acquire %s — Claude Code appears to be refreshing credentials; retry in a few seconds",
				filepath.Base(path))
		}

		// Jittered backoff avoids every waiter retrying in lockstep.
		time.Sleep(250*time.Millisecond + time.Duration(rand.Intn(250))*time.Millisecond)
	}
}

// isStale reports whether the lock directory at path exists and its mtime
// is older than staleLockAge. A missing directory (raced away by its
// holder between our failed mkdir and this stat) is not stale — the next
// mkdir attempt will simply succeed.
func isStale(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > staleLockAge
}

// startTouching begins refreshing path's mtime every touchInterval so
// other holders see this lock as live, and returns the handle used to stop
// touching and remove the directory on release.
func startTouching(path string) *heldLock {
	l := &heldLock{path: path, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(touchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-l.stop:
				return
			case now := <-ticker.C:
				_ = os.Chtimes(l.path, now, now)
			}
		}
	}()
	return l
}

// release stops the touch goroutine and removes the lock directory. Safe
// to call at most once per heldLock (guarded by sync.Once so a defer loop
// over a partially-built slice can't double-release).
func (l *heldLock) release() {
	l.stopOnce.Do(func() {
		close(l.stop)
		<-l.done
		_ = os.Remove(l.path)
	})
}
