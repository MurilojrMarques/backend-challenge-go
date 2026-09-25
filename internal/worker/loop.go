package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const maxErrorBackoff = 30 * time.Second

type Loop struct {
	name     string
	logger   *slog.Logger
	interval time.Duration
	tick     func(ctx context.Context) (bool, error)
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewLoop(name string, logger *slog.Logger, interval time.Duration, tick func(ctx context.Context) (bool, error)) *Loop {
	return &Loop{name: name, logger: logger.With("worker", name), interval: interval, tick: tick}
}

func (l *Loop) Start(context.Context) error {
	if l.cancel != nil {
		return fmt.Errorf("worker %s already started", l.name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.done = make(chan struct{})
	go l.run(ctx)
	l.logger.Info("worker started")
	return nil
}

func (l *Loop) run(ctx context.Context) {
	defer close(l.done)
	errorBackoff := l.interval
	for {
		busy, err := l.tick(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := l.interval
		switch {
		case err != nil:
			l.logger.ErrorContext(ctx, "worker tick failed", "err", err, "retryIn", errorBackoff.String())
			delay = errorBackoff
			errorBackoff = min(errorBackoff*2, maxErrorBackoff)
		case busy:
			errorBackoff = l.interval
			delay = 0
		default:
			errorBackoff = l.interval
		}
		if delay <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (l *Loop) Stop(ctx context.Context) error {
	if l.cancel == nil {
		return nil
	}
	l.cancel()
	select {
	case <-l.done:
		l.logger.Info("worker stopped")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker %s did not stop before the deadline: %w", l.name, ctx.Err())
	}
}
