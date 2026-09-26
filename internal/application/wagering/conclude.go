package wagering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

type conclusion struct {
	tx            *wager.Transaction
	wallet        *wallet.Wallet
	ref           wager.Reference
	verdict       wager.Verdict
	now           time.Time
	correlationID string
	insert        bool
}

func (s *Service) conclude(ctx context.Context, r application.Repos, c conclusion) (Result, error) {
	switch c.verdict.Decision {
	case wager.Proceed:
		return s.process(ctx, r, c)
	case wager.Await:
		return s.await(ctx, r, c)
	case wager.Reject:
		return s.reject(ctx, r, c, c.verdict.Code)
	default:
		return Result{}, fmt.Errorf("%w: no decision for transaction %s", application.ErrIntegrity, c.tx.ID())
	}
}

func (s *Service) await(ctx context.Context, r application.Repos, c conclusion) (Result, error) {
	first := c.tx.Status() == wager.Pending
	if err := c.tx.AwaitReference(c.now); err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
	}
	if s.opts.ReferencePolicy.Exhausted(c.tx, c.now) {
		return s.reject(ctx, r, c, wager.ReferenceNotFound)
	}
	next := c.now.Add(s.opts.ReferenceBackoff.Next(c.tx.ReferenceAttempts()))
	if err := s.persist(ctx, r, c, next); err != nil {
		return Result{}, err
	}
	if first {
		pending, err := event.NewWagerTransactionPendingReference(c.tx, s.metadata(c))
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		if err := r.Outbox.Append(ctx, pending); err != nil {
			return Result{}, err
		}
	}
	return resultOf(c.tx, false), nil
}

func (s *Service) reject(ctx context.Context, r application.Repos, c conclusion, code wager.FailureCode) (Result, error) {
	observed := c.wallet.Balance()
	if observed.Currency() != c.tx.Amount().Currency() {
		observed = money.Money{}
	}
	if err := c.tx.Reject(code, observed, c.now); err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
	}
	if err := s.persist(ctx, r, c, time.Time{}); err != nil {
		return Result{}, err
	}
	rejected, err := event.NewWagerTransactionRejected(c.tx, s.metadata(c))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
	}
	if err := r.Outbox.Append(ctx, rejected); err != nil {
		return Result{}, err
	}
	return resultOf(c.tx, false), nil
}

func (s *Service) process(ctx context.Context, r application.Repos, c conclusion) (Result, error) {
	if c.ref.Transaction != nil {
		if err := c.tx.ResolveReference(c.ref.Transaction.ID(), c.now); err != nil {
			return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
	}

	var entry *wallet.LedgerEntry
	expectedVersion := c.wallet.Version()
	if c.verdict.Moves {
		e, err := c.wallet.Apply(wallet.Movement{
			EntryID:       application.NewID(),
			TransactionID: c.tx.ID(),
			Direction:     c.verdict.Direction,
			Amount:        c.tx.Amount(),
			Now:           c.now,
		})
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		entry = &e
	}

	if err := c.tx.MarkProcessed(c.wallet.Balance(), c.now); err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
	}
	if err := s.persist(ctx, r, c, time.Time{}); err != nil {
		return Result{}, err
	}

	if entry != nil {
		if err := r.Wallets.Save(ctx, c.wallet, expectedVersion); err != nil {
			if errors.Is(err, application.ErrConcurrentModification) {
				s.metrics.ConcurrencyConflict("wallet.save")
			}
			return Result{}, err
		}
		if err := r.Ledger.Append(ctx, *entry); err != nil {
			return Result{}, err
		}
	}

	meta := s.metadata(c)
	processed, err := event.NewWagerTransactionProcessed(c.tx, meta)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
	}
	events := []event.Event{processed}
	if entry != nil {
		changed, err := event.NewWalletBalanceChanged(c.wallet, *entry, meta)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		events = append(events, changed)
	}
	if err := r.Outbox.Append(ctx, events...); err != nil {
		return Result{}, err
	}

	return resultOf(c.tx, false), nil
}

func (s *Service) persist(ctx context.Context, r application.Repos, c conclusion, nextAttemptAt time.Time) error {
	if c.insert {
		return r.Wagers.Insert(ctx, c.tx, nextAttemptAt)
	}
	return r.Wagers.Update(ctx, c.tx, nextAttemptAt)
}

func (s *Service) metadata(c conclusion) event.Metadata {
	return event.Metadata{
		CorrelationID: c.correlationID,
		CausationID:   c.tx.ID().String(),
		OccurredAt:    c.now,
	}
}
