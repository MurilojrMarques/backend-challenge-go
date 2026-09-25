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

const consumerIdleInterval = 100 * time.Millisecond

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
}

func NewConsumer(queue application.Queue, wagers *wagering.Service, opts ConsumerOptions, metrics application.Metrics, logger *slog.Logger, fault *Fault) *Consumer {
	c := &Consumer{queue: queue, wagers: wagers, opts: opts, metrics: metrics, logger: logger.With("worker", "consumer"), fault: fault}
	c.loop = NewLoop("consumer", logger, consumerIdleInterval, c.tick)
	return c
}

func (c *Consumer) Start(ctx context.Context) error { return c.loop.Start(ctx) }
func (c *Consumer) Stop(ctx context.Context) error  { return c.loop.Stop(ctx) }

func (c *Consumer) tick(ctx context.Context) (bool, error) {
	msgs, err := c.queue.Receive(ctx)
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		if ctx.Err() != nil {
			return true, nil
		}
		c.handle(ctx, m)
	}
	return len(msgs) > 0, nil
}

func (c *Consumer) handle(ctx context.Context, m application.Message) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opts.VisibilityTimeout)
	defer cancel()
	log := c.logger.With("messageId", m.ID, "receiveCount", m.ReceiveCount)

	inbound, err := wagering.ParseEnvelope(c.opts.ConsumerName, m.Body)
	if err != nil {
		c.deadLetter(ctx, log, m, "invalid_envelope", err)
		return
	}
	log = log.With("providerId", inbound.Command.ProviderID, "externalTransactionId", inbound.Command.ExternalTransactionID)

	res, err := c.wagers.Consume(ctx, inbound)
	switch {
	case err == nil:
		c.fault.Trigger(FaultConsumerAfterCommit)
		c.ack(ctx, log, m, res)
	case errors.Is(err, application.ErrMessageInFlight):
		c.metrics.MessageHandled("in_flight")
		log.InfoContext(ctx, "message already being processed by another consumer")
	case isPermanent(err):
		c.deadLetter(ctx, log, m, reasonFor(err), err)
	default:
		c.retryLater(ctx, log, m, err)
	}
}

func (c *Consumer) ack(ctx context.Context, log *slog.Logger, m application.Message, res wagering.Result) {
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
		c.metrics.MessageHandled("ack_failed")
		return
	}
	c.metrics.MessageHandled(outcome)
	log.InfoContext(ctx, "message handled", "outcome", outcome, "transactionId", res.TransactionID, "status", res.Status)
}

func (c *Consumer) deadLetter(ctx context.Context, log *slog.Logger, m application.Message, reason string, cause error) {
	if err := c.queue.SendToDeadLetter(ctx, m, reason); err != nil {
		log.ErrorContext(ctx, "cannot move message to the dead-letter queue", "reason", reason, "cause", cause, "err", err)
		return
	}
	if err := c.queue.Delete(ctx, m.ReceiptHandle); err != nil {
		log.WarnContext(ctx, "message copied to dlq but not deleted", "err", err)
		return
	}
	c.metrics.MessageHandled("dead_letter")
	log.WarnContext(ctx, "message moved to the dead-letter queue", "reason", reason, "cause", cause)
}

func (c *Consumer) retryLater(ctx context.Context, log *slog.Logger, m application.Message, cause error) {
	delay := c.opts.RetryBackoff.Next(m.ReceiveCount)
	if err := c.queue.ChangeVisibility(ctx, m.ReceiptHandle, delay); err != nil {
		log.WarnContext(ctx, "transient failure; visibility unchanged", "cause", cause, "err", err)
	} else {
		log.WarnContext(ctx, "transient failure; message will be redelivered", "cause", cause, "retryIn", delay.String())
	}
	c.metrics.MessageHandled("retry")
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
