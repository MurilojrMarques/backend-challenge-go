package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

func (u *UnitOfWork) Do(ctx context.Context, opts application.TxOptions, fn func(ctx context.Context, r application.Repos) error) (err error) {
	tx, err := u.pool.BeginTx(ctx, pgxOptions(opts))
	if err != nil {
		return translate(err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) && err == nil {
			err = translate(rbErr)
		}
	}()

	if err = fn(ctx, newRepos(tx)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return translate(err)
	}
	return nil
}

func pgxOptions(opts application.TxOptions) pgx.TxOptions {
	out := pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}
	switch opts.Isolation {
	case application.RepeatableRead:
		out.IsoLevel = pgx.RepeatableRead
	case application.Serializable:
		out.IsoLevel = pgx.Serializable
	}
	if opts.ReadOnly {
		out.AccessMode = pgx.ReadOnly
	}
	return out
}

func newRepos(tx pgx.Tx) application.Repos {
	return application.Repos{
		Wallets: &walletRepo{tx: tx},
		Wagers:  &wagerRepo{tx: tx},
		Ledger:  &ledgerRepo{tx: tx},
		Inbox:   &inboxRepo{tx: tx},
		Outbox:  &outboxRepo{tx: tx},
	}
}
