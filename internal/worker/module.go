package worker

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

var Module = fx.Module("worker",
	fx.Provide(func(cfg config.Config, logger *slog.Logger) *Fault {
		return NewFault(cfg.Fault.InjectPoint, logger)
	}),
)

var ConsumerModule = fx.Module("worker.consumer",
	fx.Provide(func(queue application.Queue, wagers *wagering.Service, cfg config.Config, metrics application.Metrics, logger *slog.Logger, fault *Fault) *Consumer {
		return NewConsumer(queue, wagers, ConsumerOptions{
			ConsumerName:      cfg.SQS.ConsumerName,
			VisibilityTimeout: cfg.SQS.VisibilityTimeout,
			RetryBackoff:      wagering.Backoff{Base: cfg.SQS.RetryBackoffBase, Max: cfg.SQS.RetryBackoffMax},
		}, metrics, logger, fault)
	}),
	fx.Invoke(func(lc fx.Lifecycle, c *Consumer) {
		lc.Append(fx.Hook{OnStart: c.Start, OnStop: c.Stop})
	}),
)

var OutboxModule = fx.Module("worker.outbox",
	fx.Provide(func(uow application.UnitOfWork, publisher application.EventPublisher, clock application.Clock, cfg config.Config, metrics application.Metrics, logger *slog.Logger, fault *Fault) *OutboxRelay {
		return NewOutboxRelay(uow, publisher, clock, OutboxOptions{
			PollInterval: cfg.Outbox.PollInterval,
			BatchSize:    cfg.Outbox.BatchSize,
			Lease:        cfg.Outbox.LockTTL,
			RetryBackoff: wagering.Backoff{Base: cfg.Outbox.BackoffBase, Max: cfg.Outbox.BackoffMax},
		}, metrics, logger, fault)
	}),
	fx.Invoke(func(lc fx.Lifecycle, r *OutboxRelay) {
		lc.Append(fx.Hook{OnStart: r.Start, OnStop: r.Stop})
	}),
)

var PendingModule = fx.Module("worker.pending",
	fx.Provide(func(wagers *wagering.Service, cfg config.Config, logger *slog.Logger) *PendingResolver {
		return NewPendingResolver(wagers, PendingOptions{
			PollInterval: cfg.Pending.PollInterval,
			BatchSize:    cfg.Pending.BatchSize,
		}, logger)
	}),
	fx.Invoke(func(lc fx.Lifecycle, p *PendingResolver) {
		lc.Append(fx.Hook{OnStart: p.Start, OnStop: p.Stop})
	}),
)
