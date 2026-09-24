package application

import (
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
)

type OutboxRecord struct {
	EventID       uuid.UUID
	EventType     event.Type
	AggregateID   uuid.UUID
	Payload       []byte
	OccurredAt    time.Time
	CreatedAt     time.Time
	Attempts      int
	NextAttemptAt time.Time
	LockedBy      string
	LockedUntil   time.Time
	PublishedAt   time.Time
	LastError     string
}

func (r OutboxRecord) Published() bool {
	return !r.PublishedAt.IsZero()
}

func (r OutboxRecord) Locked() bool {
	return r.LockedBy != ""
}
