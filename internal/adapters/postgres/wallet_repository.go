package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

const walletColumns = `id, player_id, currency, balance_units, version, created_at, updated_at`

type walletRow struct {
	ID           uuid.UUID `db:"id"`
	PlayerID     uuid.UUID `db:"player_id"`
	Currency     string    `db:"currency"`
	BalanceUnits int64     `db:"balance_units"`
	Version      int64     `db:"version"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

func (r walletRow) toWallet() (*wallet.Wallet, error) {
	balance, err := moneyFromRow(r.BalanceUnits, r.Currency)
	if err != nil {
		return nil, err
	}
	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID:        r.ID,
		PlayerID:  r.PlayerID,
		Balance:   balance,
		Version:   r.Version,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: wallet %s: %v", application.ErrIntegrity, r.ID, err)
	}
	return w, nil
}

func moneyFromRow(units int64, code string) (money.Money, error) {
	currency, err := money.ParseCurrency(code)
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %v", application.ErrIntegrity, err)
	}
	m, err := money.FromUnits(units, currency)
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %v", application.ErrIntegrity, err)
	}
	return m, nil
}

type walletRepo struct {
	tx pgx.Tx
}

func (r *walletRepo) Create(ctx context.Context, w *wallet.Wallet) error {
	s := w.Snapshot()
	_, err := r.tx.Exec(ctx, `
		INSERT INTO wallets (`+walletColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.PlayerID, s.Balance.Currency().Code(), s.Balance.Units(), s.Version, s.CreatedAt, s.UpdatedAt,
	)
	return translate(err)
}

func (r *walletRepo) Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return r.get(ctx, id, "")
}

func (r *walletRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return r.get(ctx, id, " FOR UPDATE")
}

func (r *walletRepo) get(ctx context.Context, id uuid.UUID, lock string) (*wallet.Wallet, error) {
	rows, err := r.tx.Query(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`+lock, id)
	if err != nil {
		return nil, translate(err)
	}
	row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[walletRow])
	if err != nil {
		return nil, translate(err)
	}
	return row.toWallet()
}

func (r *walletRepo) Save(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error {
	s := w.Snapshot()
	tag, err := r.tx.Exec(ctx, `
		UPDATE wallets
		   SET balance_units = $2, version = $3, updated_at = $4
		 WHERE id = $1 AND version = $5`,
		s.ID, s.Balance.Units(), s.Version, s.UpdatedAt, expectedVersion,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: wallet %s expected version %d", application.ErrConcurrentModification, s.ID, expectedVersion)
	}
	return nil
}
