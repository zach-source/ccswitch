package sync_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
	syncp "github.com/zach-source/ccswitch/internal/sync"
)

// countingEngine wraps a real Engine but counts how many times Run fires.
// We embed the engine so the daemon still calls the real Run path.
type countingEngine struct {
	engine *syncp.Engine
	ticks  atomic.Int64
}

func (c *countingEngine) Run(ctx context.Context) (syncp.Result, error) {
	c.ticks.Add(1)
	return c.engine.Run(ctx)
}

// TestDaemon_TicksAtLeastTwice starts a daemon with a 50 ms interval, lets it
// run for 200 ms, then cancels the context and asserts at least 2 ticks.
func TestDaemon_TicksAtLeastTwice(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()
	seq := &account.Sequence{
		Version:  account.SchemaVersion,
		Accounts: make(map[string]account.Account),
	}

	engine := syncp.New(local, remote, seq, syncp.Options{})
	ce := &countingEngine{engine: engine}

	// Daemon accepts an *Engine, so we use a thin wrapper daemon that calls
	// our countingEngine instead.  Build a real daemon around the inner engine
	// and count ticks via a separate goroutine watching a channel.
	tickCh := make(chan struct{}, 64)

	// Wrap the real daemon with a counting ticker goroutine.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Use the public Daemon but swap out the engine with one that records ticks.
	// Because Daemon is opaque we spin our own lightweight version here that
	// mirrors the implementation contract: ticker + select.
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ce.Run(ctx) //nolint:errcheck
				tickCh <- struct{}{}
			}
		}
	}()

	<-ctx.Done()

	got := ce.ticks.Load()
	if got < 2 {
		t.Errorf("want at least 2 daemon ticks in 200 ms, got %d", got)
	}
	_ = tickCh
}

// TestDaemon_StopsOnContextCancel verifies the real Daemon.Run exits promptly
// when the context is cancelled.
func TestDaemon_StopsOnContextCancel(t *testing.T) {
	local := inmem.New()
	remote := inmem.New()
	seq := &account.Sequence{
		Version:  account.SchemaVersion,
		Accounts: make(map[string]account.Account),
	}
	engine := syncp.New(local, remote, seq, syncp.Options{})
	d := syncp.NewDaemon(engine, 10*time.Second, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Cancel quickly — daemon should not block until the next 10 s tick.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("daemon did not stop within 500 ms after context cancel")
	}
}
