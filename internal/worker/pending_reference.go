package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
)

type PendingOptions struct {
	PollInterval time.Duration
	BatchSize    int
}

type PendingResolver struct {
	wagers *wagering.Service
	opts   PendingOptions
	logger *slog.Logger
	loop   *Loop
}

func NewPendingResolver(wagers *wagering.Service, opts PendingOptions, logger *slog.Logger) *PendingResolver {
	p := &PendingResolver{wagers: wagers, opts: opts, logger: logger.With("worker", "pending-reference")}
	p.loop = NewLoop("pending-reference", logger, opts.PollInterval, p.tick)
	return p
}

func (p *PendingResolver) Start(ctx context.Context) error { return p.loop.Start(ctx) }
func (p *PendingResolver) Stop(ctx context.Context) error  { return p.loop.Stop(ctx) }

func (p *PendingResolver) tick(ctx context.Context) (bool, error) {
	n, err := p.wagers.ResolveDue(ctx, p.opts.BatchSize)
	if n > 0 {
		p.logger.InfoContext(ctx, "pending references resolved", "count", n)
	}
	return n > 0, err
}
