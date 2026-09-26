package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

const (
	consumerIdleInterval = 100 * time.Millisecond
	detachedTimeout      = 5 * time.Second
)

type ConsumerOptions struct {
	ConsumerName      string
	VisibilityTimeout time.Duration
	RetryBackoff      wagering.Backoff
}

type Consumer struct {
	queue   application.Queue
	wagers  *wagering.Service
	opts    ConsumerOptions
	metrics application.Metrics
	logger  *slog.Logger
	fault   *Fault
	loop    *Loop
	now     func() time.Time
}

func NewConsumer(queue application.Queue, wagers *wagering.Service, opts ConsumerOptions, metrics application.Metrics, logger *slog.Logger, fault *Fault) *Consumer {
	c := &Consumer{queue: queue, wagers: wagers, opts: opts, metrics: metrics, logger: logger.With("worker", "consumer"), fault: fault, now: time.Now}
	c.loop = NewLoop("consumer", logger, consumerIdleInterval, c.tick)
	return c
}

func (c *Consumer) Start(ctx context.Context) error { return c.loop.Start(ctx) }
func (c *Consumer) Stop(ctx context.Context) error  { return c.loop.Stop(ctx) }

type batch struct {
	deadline time.Time
	held     map[string]time.Duration
}

func (b *batch) hold(groupID string, delay time.Duration) {
	if groupID != "" {
		b.held[groupID] = delay
	}
}

func (c *Consumer) tick(ctx context.Context) (bool, error) {
	msgs, err := c.queue.Receive(ctx)
	if err != nil {
		return false, err
	}
	b := &batch{
		deadline: c.now().Add(c.opts.VisibilityTimeout - c.opts.VisibilityTimeout/10),
		held:     map[string]time.Duration{},
	}
	for i, m := range msgs {
		budget := b.deadline.Sub(c.now())
		if ctx.Err() != nil || budget <= 0 {
			c.giveBack(ctx, msgs[i:])
			return true, nil
		}
		if delay, held := b.held[m.GroupID]; held {
			c.holdBack(ctx, m, delay)
			continue
		}
		c.handle(ctx, m, budget, b)
	}
	return len(msgs) > 0, nil
}

func (c *Consumer) handle(ctx context.Context, m application.Message, budget time.Duration, b *batch) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	started := c.now()
	observe := func(outcome string) { c.metrics.MessageHandled(outcome, c.now().Sub(started)) }
	log := c.logger.With("messageId", m.ID, "groupId", m.GroupID, "receiveCount", m.ReceiveCount)

	inbound, err := wagering.ParseEnvelope(c.opts.ConsumerName, m.Body)
	if err != nil {
		c.deadLetter(ctx, log, m, "invalid_envelope", err, observe)
		return
	}
	log = log.With("providerId", inbound.Command.ProviderID, "externalTransactionId", inbound.Command.ExternalTransactionID)

	res, err := c.wagers.Consume(ctx, inbound)
	switch {
	case err == nil:
		c.fault.Trigger(FaultConsumerAfterCommit)
		c.ack(ctx, log, m, res, observe)
	case errors.Is(err, application.ErrMessageInFlight):
		observe("in_flight")
		log.InfoContext(ctx, "message already being processed by another consumer")
		b.hold(m.GroupID, c.opts.RetryBackoff.Next(m.ReceiveCount))
	case isPermanent(err):
		c.deadLetter(ctx, log, m, reasonFor(err), err, observe)
	default:
		b.hold(m.GroupID, c.retryLater(ctx, log, m, err, observe))
	}
}

func (c *Consumer) ack(ctx context.Context, log *slog.Logger, m application.Message, res wagering.Result, observe func(string)) {
	outcome := "processed"
	switch {
	case res.IdempotentReplay:
		outcome = "replay"
	case res.Status == wager.Rejected:
		outcome = "rejected"
	case res.Status == wager.PendingReference:
		outcome = "pending_reference"
	}
	if err := c.queue.Delete(ctx, m.ReceiptHandle); err != nil {
		log.WarnContext(ctx, "message handled but not deleted; redelivery will replay", "err", err)
		observe("ack_failed")
		return
	}
	observe(outcome)
	log.InfoContext(ctx, "message handled", "outcome", outcome, "transactionId", res.TransactionID, "status", res.Status)
}

func (c *Consumer) deadLetter(ctx context.Context, log *slog.Logger, m application.Message, reason string, cause error, observe func(string)) {
	if err := c.queue.SendToDeadLetter(ctx, m, reason); err != nil {
		log.ErrorContext(ctx, "cannot move message to the dead-letter queue", "reason", reason, "cause", cause, "err", err)
		return
	}
	if err := c.queue.Delete(ctx, m.ReceiptHandle); err != nil {
		log.WarnContext(ctx, "message copied to dlq but not deleted", "err", err)
		return
	}
	observe("dead_letter")
	log.WarnContext(ctx, "message moved to the dead-letter queue", "reason", reason, "cause", cause)
}

func (c *Consumer) retryLater(ctx context.Context, log *slog.Logger, m application.Message, cause error, observe func(string)) time.Duration {
	delay := c.opts.RetryBackoff.Next(m.ReceiveCount)
	if err := c.queue.ChangeVisibility(ctx, m.ReceiptHandle, delay); err != nil {
		log.WarnContext(ctx, "transient failure; visibility unchanged", "cause", cause, "err", err)
	} else {
		log.WarnContext(ctx, "transient failure; message will be redelivered", "cause", cause, "retryIn", delay.String())
	}
	observe("retry")
	return delay
}

func (c *Consumer) holdBack(ctx context.Context, m application.Message, delay time.Duration) {
	dctx, cancel := detached(ctx)
	defer cancel()
	if err := c.queue.ChangeVisibility(dctx, m.ReceiptHandle, delay); err != nil {
		c.logger.WarnContext(dctx, "message of a held group keeps its visibility", "messageId", m.ID, "groupId", m.GroupID, "err", err)
	}
	c.metrics.MessageHandled("held_back", 0)
	c.logger.InfoContext(dctx, "message held back behind an earlier failure of its group", "messageId", m.ID, "groupId", m.GroupID, "retryIn", delay.String())
}

func (c *Consumer) giveBack(ctx context.Context, msgs []application.Message) {
	if len(msgs) == 0 {
		return
	}
	dctx, cancel := detached(ctx)
	defer cancel()
	for _, m := range msgs {
		if err := c.queue.ChangeVisibility(dctx, m.ReceiptHandle, 0); err != nil {
			c.logger.WarnContext(dctx, "message not returned to the queue; it reappears when its visibility expires", "messageId", m.ID, "err", err)
		}
	}
	c.metrics.MessageHandled("returned", 0)
	c.logger.InfoContext(dctx, "returned unprocessed messages to the queue", "count", len(msgs))
}

func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachedTimeout)
}

func isPermanent(err error) bool {
	return errors.Is(err, application.ErrInvalidInput) ||
		errors.Is(err, application.ErrConflict) ||
		errors.Is(err, application.ErrIntegrity) ||
		errors.Is(err, application.ErrNotFound) ||
		errors.Is(err, application.ErrForbidden)
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, application.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, application.ErrConflict):
		return "conflict"
	case errors.Is(err, application.ErrNotFound):
		return "not_found"
	case errors.Is(err, application.ErrIntegrity):
		return "integrity"
	default:
		return "permanent_failure"
	}
}
