package cli

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestAcquireReleaseRemovesDir verifies the basic lock lifecycle: acquiring
// creates the lock directory, releasing removes it.
func TestAcquireReleaseRemovesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x.lock")
	lock, err := acquireDirLock(dir)
	if err != nil {
		t.Fatalf("acquireDirLock: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("lock dir should exist while held: %v", err)
	}
	lock.release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("lock dir should be removed after release, stat err = %v", err)
	}
}

// TestSecondAcquireBlocksThenSucceeds verifies that a second acquirer waits
// for the first to release rather than stealing a live (non-stale) lock.
func TestSecondAcquireBlocksThenSucceeds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x.lock")
	first, err := acquireDirLock(dir)
	if err != nil {
		t.Fatalf("first acquireDirLock: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	acquiredAt := make(chan time.Time, 1)
	go func() {
		defer wg.Done()
		second, err := acquireDirLock(dir)
		if err != nil {
			t.Errorf("second acquireDirLock: %v", err)
			return
		}
		acquiredAt <- time.Now()
		second.release()
	}()

	// Give the goroutine time to block on the held lock, then release it.
	time.Sleep(300 * time.Millisecond)
	releasedAt := time.Now()
	first.release()

	wg.Wait()
	got := <-acquiredAt
	if got.Before(releasedAt) {
		t.Fatalf("second acquirer succeeded before first released")
	}
}

// TestStaleLockTakenOver verifies that a hand-created lock directory whose
// mtime is older than staleLockAge is treated as abandoned and taken over
// instead of blocking until timeout.
func TestStaleLockTakenOver(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x.lock")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old := time.Now().Add(-2 * staleLockAge)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	start := time.Now()
	lock, err := acquireDirLock(dir)
	if err != nil {
		t.Fatalf("acquireDirLock on stale lock: %v", err)
	}
	defer lock.release()

	if elapsed := time.Since(start); elapsed >= lockTimeout {
		t.Fatalf("stale takeover took as long as a full timeout (%v) — did not take over", elapsed)
	}
}

// TestTimeoutAgainstFreshLock verifies that acquiring against a lock that
// stays live (its holder keeps touching it) fails with a clear error once
// lockTimeout elapses, rather than blocking forever.
func TestTimeoutAgainstFreshLock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in -short mode")
	}
	dir := filepath.Join(t.TempDir(), "x.lock")
	holder, err := acquireDirLock(dir)
	if err != nil {
		t.Fatalf("acquireDirLock: %v", err)
	}
	defer holder.release()

	start := time.Now()
	_, err = acquireDirLock(dir)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if elapsed < lockTimeout {
		t.Fatalf("acquireDirLock returned early (%v) before lockTimeout (%v)", elapsed, lockTimeout)
	}
}

// TestWithDirLocksReleasesOnError verifies that withDirLocks releases every
// acquired lock even when fn returns an error, so a failed switch never
// leaves ccswitch holding a lock that blocks Claude Code.
func TestWithDirLocksReleasesOnError(t *testing.T) {
	tmp := t.TempDir()
	paths := []string{filepath.Join(tmp, "a.lock"), filepath.Join(tmp, "b.lock")}

	err := withDirLocks(paths, func() error {
		for _, p := range paths {
			if _, statErr := os.Stat(p); statErr != nil {
				t.Fatalf("lock %s should be held inside fn: %v", p, statErr)
			}
		}
		return errFake
	})
	if err != errFake {
		t.Fatalf("withDirLocks error = %v, want errFake", err)
	}
	for _, p := range paths {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Fatalf("lock %s should be released after fn errors", p)
		}
	}
}

// TestWithDirLocksReleasesOnPanic verifies locks are released even when fn
// panics, so a bug mid-switch can't leave a stuck lock (the 10s staleness
// window would recover eventually, but releasing promptly is strictly
// better for anything racing us).
func TestWithDirLocksReleasesOnPanic(t *testing.T) {
	tmp := t.TempDir()
	paths := []string{filepath.Join(tmp, "a.lock")}

	func() {
		defer func() { recover() }()
		_ = withDirLocks(paths, func() error {
			panic("boom")
		})
	}()

	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("lock should be released after fn panics")
	}
}

type fakeErr struct{}

func (fakeErr) Error() string { return "fake error" }

var errFake = fakeErr{}
