package sync

import (
	"context"
	"log/slog"
	"time"
)

// Daemon runs Engine.Run on a fixed ticker until the context is cancelled.
// It is designed to be started once (as a long-running goroutine or process)
// and never panics on engine errors — it logs and continues.
type Daemon struct {
	engine   *Engine
	interval time.Duration
	log      *slog.Logger
}

// DefaultInterval is used when NewDaemon is called with a non-positive
// interval (config default also picks this; the guard here protects
// against a hand-edited TOML with `interval = 0` which would otherwise
// panic time.NewTicker).
const DefaultInterval = 5 * time.Minute

// NewDaemon constructs a Daemon. A non-positive interval is replaced
// with DefaultInterval so time.NewTicker doesn't panic.
func NewDaemon(engine *Engine, interval time.Duration, log *slog.Logger) *Daemon {
	if log == nil {
		log = slog.Default()
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Daemon{engine: engine, interval: interval, log: log}
}

// Run blocks until ctx is cancelled, firing Engine.Run on each tick.
// It returns ctx.Err() when the context is done, or nil on clean shutdown.
func (d *Daemon) Run(ctx context.Context) error {
	d.log.Info("ccswitch daemon started", "interval", d.interval)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("ccswitch daemon stopped")
			return ctx.Err()
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Daemon) tick(ctx context.Context) {
	d.log.Info("sync tick: starting")
	res, err := d.engine.Run(ctx)
	if err != nil {
		d.log.Error("sync tick: engine error", "err", err)
		return
	}
	d.log.Info("sync tick: done",
		"pushed", res.Pushed,
		"pulled", res.Pulled,
		"unchanged", res.Unchanged,
		"errors", res.Errors,
	)
}
