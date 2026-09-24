package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

const ledgerColumns = `
	id, wallet_id, transaction_id, direction, amount_units, currency,
	balance_before_units, balance_after_units, created_at`

type ledgerRow struct {
	ID                 uuid.UUID `db:"id"`
	WalletID           uuid.UUID `db:"wallet_id"`
	TransactionID      uuid.UUID `db:"transaction_id"`
	Direction          string    `db:"direction"`
	AmountUnits        int64     `db:"amount_units"`
	Currency           string    `db:"currency"`
	BalanceBeforeUnits int64     `db:"balance_before_units"`
	BalanceAfterUnits  int64     `db:"balance_after_units"`
	CreatedAt          time.Time `db:"created_at"`
}

func (r ledgerRow) toEntry() (wallet.LedgerEntry, error) {
	amount, err := moneyFromRow(r.AmountUnits, r.Currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	before, err := moneyFromRow(r.BalanceBeforeUnits, r.Currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	after, err := moneyFromRow(r.BalanceAfterUnits, r.Currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	entry, err := wallet.NewLedgerEntry(wallet.LedgerEntryParams{
		ID:            r.ID,
		WalletID:      r.WalletID,
		TransactionID: r.TransactionID,
		Direction:     wallet.Direction(r.Direction),
		Amount:        amount,
		BalanceBefore: before,
		BalanceAfter:  after,
		CreatedAt:     r.CreatedAt,
	})
	if err != nil {
		return wallet.LedgerEntry{}, fmt.Errorf("%w: ledger entry %s: %v", application.ErrIntegrity, r.ID, err)
	}
	return entry, nil
}

type ledgerRepo struct {
	tx pgx.Tx
}

func (r *ledgerRepo) Append(ctx context.Context, e wallet.LedgerEntry) error {
	_, err := r.tx.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (`+ledgerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()),
		e.Amount().Units(), e.Amount().Currency().Code(),
		e.BalanceBefore().Units(), e.BalanceAfter().Units(), e.CreatedAt(),
	)
	return translate(err)
}

func (r *ledgerRepo) List(ctx context.Context, walletID uuid.UUID, after application.LedgerCursor, limit int) ([]wallet.LedgerEntry, error) {
	rows, err := r.tx.Query(ctx, `
		SELECT `+ledgerColumns+` FROM wallet_ledger_entries
		WHERE wallet_id = $1
		  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3))
		ORDER BY created_at, id
		LIMIT $4`,
		walletID, nullableTime(after.CreatedAt), after.ID, limit,
	)
	if err != nil {
		return nil, translate(err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[ledgerRow])
	if err != nil {
		return nil, translate(err)
	}
	out := make([]wallet.LedgerEntry, 0, len(items))
	for _, row := range items {
		entry, err := row.toEntry()
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

func (r *ledgerRepo) Totals(ctx context.Context, walletID uuid.UUID) (application.LedgerTotals, error) {
	var t application.LedgerTotals
	err := r.tx.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount_units ELSE 0 END), 0)::BIGINT,
			COALESCE(SUM(CASE WHEN direction = 'DEBIT'  THEN amount_units ELSE 0 END), 0)::BIGINT,
			COUNT(*)::INTEGER
		FROM wallet_ledger_entries
		WHERE wallet_id = $1`, walletID,
	).Scan(&t.CreditUnits, &t.DebitUnits, &t.Entries)
	if err != nil {
		return application.LedgerTotals{}, translate(err)
	}
	return t, nil
}
