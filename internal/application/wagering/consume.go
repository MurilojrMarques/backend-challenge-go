package wagering

import (
	"context"
	"errors"
	"fmt"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

type InboundMessage struct {
	ConsumerName string
	MessageID    string
	PayloadHash  string
	Command      Command
}

func (m InboundMessage) validate() error {
	if m.ConsumerName == "" || m.MessageID == "" || m.PayloadHash == "" {
		return fmt.Errorf("%w: inbound message requires consumerName, messageId and payloadHash", application.ErrInvalidInput)
	}
	return nil
}

func (s *Service) Consume(ctx context.Context, msg InboundMessage) (Result, error) {
	if err := msg.validate(); err != nil {
		return Result{}, err
	}
	p, err := msg.Command.parse()
	if err != nil {
		return Result{}, err
	}

	var result Result
	err = s.uow.Do(ctx, application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		now := s.clock.Now().UTC()

		rec, err := r.Inbox.Get(ctx, msg.ConsumerName, msg.MessageID)
		switch {
		case err == nil:
			if rec.PayloadHash != msg.PayloadHash {
				return fmt.Errorf("%w: message %s redelivered with a different payload", application.ErrConflict, msg.MessageID)
			}
			if rec.Completed() {
				tx, err := r.Wagers.Get(ctx, rec.TransactionID)
				if err != nil {
					return err
				}
				result = resultOf(tx, true)
				return nil
			}
		case errors.Is(err, application.ErrNotFound):
			insertErr := r.Inbox.Insert(ctx, application.InboxRecord{
				ConsumerName: msg.ConsumerName,
				MessageID:    msg.MessageID,
				PayloadHash:  msg.PayloadHash,
				ReceivedAt:   now,
			})
			if errors.Is(insertErr, application.ErrConflict) {
				return fmt.Errorf("%w: %s", application.ErrMessageInFlight, msg.MessageID)
			}
			if insertErr != nil {
				return insertErr
			}
		default:
			return err
		}

		out, err := s.run(ctx, r, p)
		if err != nil {
			return err
		}
		result = out
		return r.Inbox.Complete(ctx, msg.ConsumerName, msg.MessageID, out.TransactionID, now)
	})
	if err != nil {
		return Result{}, err
	}
	s.observe(result, "sqs")
	return result, nil
}
