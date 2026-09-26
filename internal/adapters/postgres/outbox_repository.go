package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
)

const outboxColumns = `
	event_id, event_type, aggregate_id, payload, occurred_at, created_at,
	attempts, next_attempt_at, locked_by, locked_until, published_at, last_error`

type outboxRow struct {
	EventID       uuid.UUID  `db:"event_id"`
	EventType     string     `db:"event_type"`
	AggregateID   uuid.UUID  `db:"aggregate_id"`
	Payload       []byte     `db:"payload"`
	OccurredAt    time.Time  `db:"occurred_at"`
	CreatedAt     time.Time  `db:"created_at"`
	Attempts      int32      `db:"attempts"`
	NextAttemptAt time.Time  `db:"next_attempt_at"`
	LockedBy      *string    `db:"locked_by"`
	LockedUntil   *time.Time `db:"locked_until"`
	PublishedAt   *time.Time `db:"published_at"`
	LastError     *string    `db:"last_error"`
}

func (r outboxRow) toRecord() application.OutboxRecord {
	rec := application.OutboxRecord{
		EventID:       r.EventID,
		EventType:     event.Type(r.EventType),
		AggregateID:   r.AggregateID,
		Payload:       r.Payload,
		OccurredAt:    r.OccurredAt,
		CreatedAt:     r.CreatedAt,
		Attempts:      int(r.Attempts),
		NextAttemptAt: r.NextAttemptAt,
		LockedBy:      deref(r.LockedBy),
		LastError:     deref(r.LastError),
	}
	if r.LockedUntil != nil {
		rec.LockedUntil = *r.LockedUntil
	}
	if r.PublishedAt != nil {
		rec.PublishedAt = *r.PublishedAt
	}
	return rec
}

type outboxRepo struct {
	tx pgx.Tx
}

func (r *outboxRepo) Append(ctx context.Context, events ...event.Event) error {
	if len(events) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, e := range events {
		payload, err := e.Encode()
		if err != nil {
			return fmt.Errorf("%w: encode event %s: %v", application.ErrInvalidInput, e.ID, err)
		}
		batch.Queue(`
			INSERT INTO outbox_events (event_id, event_type, aggregate_id, payload, occurred_at, created_at, attempts, next_attempt_at)
			VALUES ($1, $2, $3, $4, $5, now(), 0, LEAST($5, now()))`,
			e.ID, string(e.Type), e.AggregateID, payload, e.OccurredAt,
		)
	}
	results := r.tx.SendBatch(ctx, batch)
	defer results.Close()
	for range events {
		if _, err := results.Exec(); err != nil {
			return translate(err)
		}
	}
	return nil
}

func (r *outboxRepo) Claim(ctx context.Context, publisher string, now time.Time, lease time.Duration, limit int) ([]application.OutboxRecord, error) {
	rows, err := r.tx.Query(ctx, `
		WITH due AS (
			SELECT o.event_id
			FROM outbox_events o
			WHERE o.published_at IS NULL
			  AND o.next_attempt_at <= $2
			  AND (o.locked_until IS NULL OR o.locked_until <= $2)
			  AND NOT EXISTS (
			      SELECT 1 FROM outbox_events older
			      WHERE older.aggregate_id = o.aggregate_id
			        AND older.published_at IS NULL
			        AND (older.occurred_at, older.event_id) < (o.occurred_at, o.event_id)
			  )
			ORDER BY o.occurred_at, o.event_id
			LIMIT $4
			FOR UPDATE OF o SKIP LOCKED
		)
		UPDATE outbox_events o
		   SET locked_by = $1, locked_until = $3
		  FROM due
		 WHERE o.event_id = due.event_id
		RETURNING `+qualified(outboxColumns, "o."),
		publisher, now, now.Add(lease), limit,
	)
	if err != nil {
		return nil, translate(err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[outboxRow])
	if err != nil {
		return nil, translate(err)
	}
	out := make([]application.OutboxRecord, 0, len(items))
	for _, row := range items {
		out = append(out, row.toRecord())
	}
	return out, nil
}

func (r *outboxRepo) MarkPublished(ctx context.Context, eventID uuid.UUID, publisher string, at time.Time) error {
	tag, err := r.tx.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = $3, locked_by = NULL, locked_until = NULL, last_error = NULL
		 WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL`,
		eventID, publisher, at,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: outbox event %s is not leased by %s", application.ErrConcurrentModification, eventID, publisher)
	}
	return nil
}

func (r *outboxRepo) Release(ctx context.Context, eventID uuid.UUID, publisher string, nextAttemptAt time.Time, lastError string) error {
	tag, err := r.tx.Exec(ctx, `
		UPDATE outbox_events
		   SET attempts = attempts + 1, next_attempt_at = $3, last_error = $4, locked_by = NULL, locked_until = NULL
		 WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL`,
		eventID, publisher, nextAttemptAt, nullable(lastError),
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: outbox event %s is not leased by %s", application.ErrConcurrentModification, eventID, publisher)
	}
	return nil
}

func (r *outboxRepo) OldestPending(ctx context.Context) (time.Time, int, error) {
	var (
		oldest *time.Time
		count  int
	)
	err := r.tx.QueryRow(ctx, `
		SELECT MIN(occurred_at), COUNT(*)::INTEGER
		FROM outbox_events
		WHERE published_at IS NULL`,
	).Scan(&oldest, &count)
	if err != nil {
		return time.Time{}, 0, translate(err)
	}
	if oldest == nil {
		return time.Time{}, 0, nil
	}
	return *oldest, count, nil
}
