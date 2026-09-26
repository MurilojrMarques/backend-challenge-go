package worker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
)

const (
	markTimeout       = 5 * time.Second
	maxPublishTimeout = 10 * time.Second
)

type OutboxOptions struct {
	PublisherID  string
	PollInterval time.Duration
	BatchSize    int
	Lease        time.Duration
	RetryBackoff wagering.Backoff
}

type OutboxRelay struct {
	uow       application.UnitOfWork
	publisher application.EventPublisher
	clock     application.Clock
	opts      OutboxOptions
	metrics   application.Metrics
	logger    *slog.Logger
	fault     *Fault
	loop      *Loop
}

func PublisherID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return host + "/" + uuid.Must(uuid.NewV7()).String()
}

func NewOutboxRelay(uow application.UnitOfWork, publisher application.EventPublisher, clock application.Clock, opts OutboxOptions, metrics application.Metrics, logger *slog.Logger, fault *Fault) *OutboxRelay {
	if opts.PublisherID == "" {
		opts.PublisherID = PublisherID()
	}
	r := &OutboxRelay{uow: uow, publisher: publisher, clock: clock, opts: opts, metrics: metrics, logger: logger.With("worker", "outbox", "publisherId", opts.PublisherID), fault: fault}
	r.loop = NewLoop("outbox", logger, opts.PollInterval, r.tick)
	return r
}

func (r *OutboxRelay) Start(ctx context.Context) error { return r.loop.Start(ctx) }
func (r *OutboxRelay) Stop(ctx context.Context) error  { return r.loop.Stop(ctx) }

func (r *OutboxRelay) tick(ctx context.Context) (bool, error) {
	now := r.clock.Now()
	var claimed []application.OutboxRecord
	err := r.uow.Do(ctx, application.TxOptions{}, func(ctx context.Context, repos application.Repos) error {
		oldest, pending, err := repos.Outbox.OldestPending(ctx)
		if err != nil {
			return err
		}
		if pending == 0 {
			r.metrics.OutboxLag(0, 0)
		} else {
			r.metrics.OutboxLag(now.Sub(oldest), pending)
		}
		claimed, err = repos.Outbox.Claim(ctx, r.opts.PublisherID, now, r.opts.Lease, r.opts.BatchSize)
		return err
	})
	if err != nil {
		return false, err
	}

	for i, rec := range claimed {
		if ctx.Err() != nil {
			r.abandon(ctx, claimed[i:])
			return true, nil
		}
		r.publishOne(ctx, rec)
	}
	return len(claimed) > 0, nil
}

func (r *OutboxRelay) publishTimeout() time.Duration {
	if r.opts.Lease <= 0 || r.opts.Lease/4 > maxPublishTimeout {
		return maxPublishTimeout
	}
	return r.opts.Lease / 4
}

func (r *OutboxRelay) publishOne(ctx context.Context, rec application.OutboxRecord) {
	log := r.logger.With("eventId", rec.EventID, "eventType", rec.EventType, "aggregateId", rec.AggregateID, "attempt", rec.Attempts+1)
	if r.clock.Now().After(rec.LockedUntil) {
		log.WarnContext(ctx, "lease expired before publishing; the event stays for the next claim")
		return
	}

	pctx, cancel := context.WithTimeout(ctx, r.publishTimeout())
	err := r.publisher.Publish(pctx, rec)
	cancel()
	if err != nil {
		r.release(ctx, log, rec, err)
		return
	}
	r.fault.Trigger(FaultOutboxAfterPublish)

	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markTimeout)
	defer cancel()
	err = r.uow.Do(mctx, application.TxOptions{}, func(ctx context.Context, repos application.Repos) error {
		return repos.Outbox.MarkPublished(ctx, rec.EventID, r.opts.PublisherID, r.clock.Now())
	})
	switch {
	case err == nil:
		r.metrics.OutboxPublished(rec.EventType)
		log.InfoContext(ctx, "event published")
	case errors.Is(err, application.ErrConcurrentModification):
		log.WarnContext(ctx, "lease lost after publishing; another publisher may republish with the same eventId")
	default:
		log.ErrorContext(ctx, "event published but not marked; it will be republished with the same eventId", "err", err)
	}
}

func (r *OutboxRelay) abandon(ctx context.Context, recs []application.OutboxRecord) {
	now := r.clock.Now()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markTimeout)
	defer cancel()
	err := r.uow.Do(rctx, application.TxOptions{}, func(ctx context.Context, repos application.Repos) error {
		for _, rec := range recs {
			if err := repos.Outbox.Release(ctx, rec.EventID, r.opts.PublisherID, now, "shutdown before publish"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.logger.ErrorContext(rctx, "claimed events not released on shutdown; their leases will expire", "count", len(recs), "err", err)
		return
	}
	r.logger.InfoContext(rctx, "released claimed events on shutdown", "count", len(recs))
}

func (r *OutboxRelay) release(ctx context.Context, log *slog.Logger, rec application.OutboxRecord, cause error) {
	next := r.clock.Now().Add(r.opts.RetryBackoff.Next(rec.Attempts + 1))
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markTimeout)
	defer cancel()
	err := r.uow.Do(rctx, application.TxOptions{}, func(ctx context.Context, repos application.Repos) error {
		return repos.Outbox.Release(ctx, rec.EventID, r.opts.PublisherID, next, cause.Error())
	})
	if err != nil {
		log.ErrorContext(ctx, "publish failed and release failed; lease will expire", "cause", cause, "err", err)
		return
	}
	r.metrics.OutboxRetried(rec.EventType)
	log.WarnContext(ctx, "publish failed; scheduled for retry", "cause", cause, "nextAttemptAt", next)
}
