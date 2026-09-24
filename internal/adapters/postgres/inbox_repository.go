package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

type inboxRepo struct {
	tx pgx.Tx
}

func (r *inboxRepo) Insert(ctx context.Context, rec application.InboxRecord) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	var txID uuid.NullUUID
	if rec.TransactionID != uuid.Nil {
		txID = uuid.NullUUID{UUID: rec.TransactionID, Valid: true}
	}
	_, err := r.tx.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, transaction_id, received_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		rec.ConsumerName, rec.MessageID, rec.PayloadHash, txID, rec.ReceivedAt, nullableTime(rec.CompletedAt),
	)
	return translate(err)
}

func (r *inboxRepo) Get(ctx context.Context, consumerName, messageID string) (application.InboxRecord, error) {
	var (
		rec         application.InboxRecord
		txID        uuid.NullUUID
		completedAt *time.Time
	)
	err := r.tx.QueryRow(ctx, `
		SELECT consumer_name, message_id, payload_hash, transaction_id, received_at, completed_at
		FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`, consumerName, messageID,
	).Scan(&rec.ConsumerName, &rec.MessageID, &rec.PayloadHash, &txID, &rec.ReceivedAt, &completedAt)
	if err != nil {
		return application.InboxRecord{}, translate(err)
	}
	if txID.Valid {
		rec.TransactionID = txID.UUID
	}
	if completedAt != nil {
		rec.CompletedAt = *completedAt
	}
	return rec, nil
}

func (r *inboxRepo) Complete(ctx context.Context, consumerName, messageID string, transactionID uuid.UUID, at time.Time) error {
	var txID uuid.NullUUID
	if transactionID != uuid.Nil {
		txID = uuid.NullUUID{UUID: transactionID, Valid: true}
	}
	tag, err := r.tx.Exec(ctx, `
		UPDATE inbox_messages
		   SET transaction_id = COALESCE($3, transaction_id), completed_at = $4
		 WHERE consumer_name = $1 AND message_id = $2 AND completed_at IS NULL`,
		consumerName, messageID, txID, at,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: inbox message %s/%s is missing or already completed", application.ErrConcurrentModification, consumerName, messageID)
	}
	return nil
}
