package application

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type InboxRecord struct {
	ConsumerName  string
	MessageID     string
	PayloadHash   string
	TransactionID uuid.UUID
	ReceivedAt    time.Time
	CompletedAt   time.Time
}

func (r InboxRecord) Validate() error {
	for name, value := range map[string]string{
		"consumerName": r.ConsumerName,
		"messageId":    r.MessageID,
		"payloadHash":  r.PayloadHash,
	} {
		if value == "" || len(value) > 128 {
			return fmt.Errorf("%w: inbox %s must be non-empty and at most 128 bytes", ErrInvalidInput, name)
		}
	}
	if r.ReceivedAt.IsZero() {
		return fmt.Errorf("%w: inbox receivedAt is required", ErrInvalidInput)
	}
	if !r.CompletedAt.IsZero() && r.CompletedAt.Before(r.ReceivedAt) {
		return fmt.Errorf("%w: inbox completedAt precedes receivedAt", ErrInvalidInput)
	}
	return nil
}

func (r InboxRecord) Completed() bool {
	return !r.CompletedAt.IsZero()
}
