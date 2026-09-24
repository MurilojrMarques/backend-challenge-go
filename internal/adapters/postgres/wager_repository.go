package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

const wagerColumns = `
	id, kind, status, wallet_id, player_id, amount_units, currency,
	provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
	reference_external_transaction_id, resolved_reference_id, failure_code, balance_after_units,
	reference_attempts, next_attempt_at, created_at, updated_at, completed_at`

type wagerRow struct {
	ID                             uuid.UUID     `db:"id"`
	Kind                           string        `db:"kind"`
	Status                         string        `db:"status"`
	WalletID                       uuid.UUID     `db:"wallet_id"`
	PlayerID                       uuid.UUID     `db:"player_id"`
	AmountUnits                    int64         `db:"amount_units"`
	Currency                       string        `db:"currency"`
	ProviderID                     *string       `db:"provider_id"`
	ExternalTransactionID          *string       `db:"external_transaction_id"`
	IdempotencyKey                 *string       `db:"idempotency_key"`
	PayloadHash                    *string       `db:"payload_hash"`
	RoundID                        *string       `db:"round_id"`
	GameID                         *string       `db:"game_id"`
	ReferenceExternalTransactionID *string       `db:"reference_external_transaction_id"`
	ResolvedReferenceID            uuid.NullUUID `db:"resolved_reference_id"`
	FailureCode                    *string       `db:"failure_code"`
	BalanceAfterUnits              *int64        `db:"balance_after_units"`
	ReferenceAttempts              int32         `db:"reference_attempts"`
	NextAttemptAt                  *time.Time    `db:"next_attempt_at"`
	CreatedAt                      time.Time     `db:"created_at"`
	UpdatedAt                      time.Time     `db:"updated_at"`
	CompletedAt                    *time.Time    `db:"completed_at"`
}

func (r wagerRow) toTransaction() (*wager.Transaction, error) {
	amount, err := moneyFromRow(r.AmountUnits, r.Currency)
	if err != nil {
		return nil, err
	}
	s := wager.Snapshot{
		ID:                r.ID,
		Kind:              wager.Kind(r.Kind),
		Status:            wager.Status(r.Status),
		WalletID:          r.WalletID,
		PlayerID:          r.PlayerID,
		Amount:            amount,
		ReferenceAttempts: int(r.ReferenceAttempts),
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
	if r.ProviderID != nil {
		s.External = &wager.External{
			ProviderID:                     *r.ProviderID,
			ExternalTransactionID:          deref(r.ExternalTransactionID),
			IdempotencyKey:                 deref(r.IdempotencyKey),
			PayloadHash:                    deref(r.PayloadHash),
			RoundID:                        deref(r.RoundID),
			GameID:                         deref(r.GameID),
			ReferenceExternalTransactionID: deref(r.ReferenceExternalTransactionID),
		}
	}
	if r.ResolvedReferenceID.Valid {
		s.ResolvedReferenceID = r.ResolvedReferenceID.UUID
	}
	if r.FailureCode != nil {
		s.FailureCode = wager.FailureCode(*r.FailureCode)
	}
	if r.BalanceAfterUnits != nil {
		if s.BalanceAfter, err = moneyFromRow(*r.BalanceAfterUnits, r.Currency); err != nil {
			return nil, err
		}
	}
	if r.CompletedAt != nil {
		s.CompletedAt = *r.CompletedAt
	}

	tx, err := wager.Rehydrate(s)
	if err != nil {
		return nil, fmt.Errorf("%w: transaction %s: %v", application.ErrIntegrity, r.ID, err)
	}
	return tx, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func wagerValues(tx *wager.Transaction, nextAttemptAt time.Time) []any {
	s := tx.Snapshot()
	ext := wager.External{}
	if s.External != nil {
		ext = *s.External
	}
	var resolved uuid.NullUUID
	if s.ResolvedReferenceID != uuid.Nil {
		resolved = uuid.NullUUID{UUID: s.ResolvedReferenceID, Valid: true}
	}
	var balanceAfter *int64
	if s.BalanceAfter.Validate() == nil {
		units := s.BalanceAfter.Units()
		balanceAfter = &units
	}
	return []any{
		s.ID, string(s.Kind), string(s.Status), s.WalletID, s.PlayerID, s.Amount.Units(), s.Amount.Currency().Code(),
		nullable(ext.ProviderID), nullable(ext.ExternalTransactionID), nullable(ext.IdempotencyKey),
		nullable(ext.PayloadHash), nullable(ext.RoundID), nullable(ext.GameID),
		nullable(ext.ReferenceExternalTransactionID), resolved, nullable(string(s.FailureCode)), balanceAfter,
		int32(s.ReferenceAttempts), nullableTime(nextAttemptAt), s.CreatedAt, s.UpdatedAt, nullableTime(s.CompletedAt),
	}
}

type wagerRepo struct {
	tx pgx.Tx
}

func (r *wagerRepo) Insert(ctx context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error {
	_, err := r.tx.Exec(ctx, `
		INSERT INTO wager_transactions (`+wagerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`,
		wagerValues(tx, nextAttemptAt)...,
	)
	return translate(err)
}

func (r *wagerRepo) Update(ctx context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error {
	tag, err := r.tx.Exec(ctx, `
		UPDATE wager_transactions SET
			status = $3, resolved_reference_id = $15, failure_code = $16, balance_after_units = $17,
			reference_attempts = $18, next_attempt_at = $19, updated_at = $21, completed_at = $22
		WHERE id = $1
		  AND kind = $2 AND wallet_id = $4 AND player_id = $5 AND amount_units = $6 AND currency = $7
		  AND provider_id IS NOT DISTINCT FROM $8
		  AND external_transaction_id IS NOT DISTINCT FROM $9
		  AND idempotency_key IS NOT DISTINCT FROM $10
		  AND payload_hash IS NOT DISTINCT FROM $11
		  AND round_id IS NOT DISTINCT FROM $12
		  AND game_id IS NOT DISTINCT FROM $13
		  AND reference_external_transaction_id IS NOT DISTINCT FROM $14
		  AND created_at = $20
		  AND status NOT IN ('PROCESSED', 'REJECTED', 'FAILED')`,
		wagerValues(tx, nextAttemptAt)...,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: transaction %s is terminal, missing or has immutable fields changed", application.ErrConcurrentModification, tx.ID())
	}
	return nil
}

func (r *wagerRepo) Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error) {
	return r.one(ctx, `SELECT `+wagerColumns+` FROM wager_transactions WHERE id = $1`, id)
}

func (r *wagerRepo) GetByIdempotencyKey(ctx context.Context, providerID, key string) (*wager.Transaction, error) {
	return r.one(ctx, `
		SELECT `+wagerColumns+` FROM wager_transactions
		WHERE provider_id = $1 AND idempotency_key = $2`, providerID, key)
}

func (r *wagerRepo) GetByExternalID(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error) {
	return r.one(ctx, `
		SELECT `+wagerColumns+` FROM wager_transactions
		WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalTransactionID)
}

func (r *wagerRepo) HasSuccessfulReversal(ctx context.Context, referenceID uuid.UUID) (bool, error) {
	var exists bool
	err := r.tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM wager_transactions
			WHERE resolved_reference_id = $1
			  AND status = 'PROCESSED'
			  AND kind IN ('REFUND', 'ROLLBACK'))`, referenceID).Scan(&exists)
	if err != nil {
		return false, translate(err)
	}
	return exists, nil
}

func (r *wagerRepo) ListDuePendingReferences(ctx context.Context, now time.Time, limit int) ([]*wager.Transaction, error) {
	rows, err := r.tx.Query(ctx, `
		SELECT `+wagerColumns+` FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= $1
		ORDER BY next_attempt_at, created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, translate(err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[wagerRow])
	if err != nil {
		return nil, translate(err)
	}
	out := make([]*wager.Transaction, 0, len(items))
	for _, row := range items {
		tx, err := row.toTransaction()
		if err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, nil
}

func (r *wagerRepo) one(ctx context.Context, sql string, args ...any) (*wager.Transaction, error) {
	rows, err := r.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, translate(err)
	}
	row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[wagerRow])
	if err != nil {
		return nil, translate(err)
	}
	return row.toTransaction()
}
