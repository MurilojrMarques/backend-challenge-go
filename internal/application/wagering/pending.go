package wagering

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

func (b Backoff) Next(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if b.Base <= 0 {
		return b.Max
	}
	d := b.Base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d <= 0 || d >= b.Max {
			return b.Max
		}
	}
	if b.Max > 0 && d > b.Max {
		return b.Max
	}
	return d
}

func (s *Service) ResolveDue(ctx context.Context, limit int) (int, error) {
	processed := 0
	for processed < limit {
		found, err := s.resolveNext(ctx)
		if err != nil {
			return processed, err
		}
		if !found {
			break
		}
		processed++
	}
	return processed, nil
}

func (s *Service) resolveNext(ctx context.Context) (bool, error) {
	var (
		found bool
		txID  uuid.UUID
	)
	err := s.uow.Do(ctx, application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		now := s.clock.Now().UTC()
		due, err := r.Wagers.ListDuePendingReferences(ctx, now, 1)
		if err != nil || len(due) == 0 {
			return err
		}
		found = true
		tx := due[0]
		txID = tx.ID()

		w, err := r.Wallets.GetForUpdate(ctx, tx.WalletID())
		if err != nil {
			return err
		}
		ref, err := s.loadReference(ctx, r, tx)
		if err != nil {
			return err
		}
		_, err = s.conclude(ctx, r, conclusion{
			tx:            tx,
			wallet:        w,
			ref:           ref,
			verdict:       wager.Evaluate(tx, w, ref),
			now:           now,
			correlationID: tx.ID().String(),
			insert:        false,
		})
		return err
	})
	if err != nil && found && isPermanent(err) {
		s.logger.ErrorContext(ctx, "pending reference resolution failed permanently", "transactionId", txID, "err", err)
		if failErr := s.failPermanently(ctx, txID); failErr != nil {
			return found, failErr
		}
		return found, nil
	}
	return found, err
}

func isPermanent(err error) bool {
	return errors.Is(err, application.ErrIntegrity) ||
		errors.Is(err, application.ErrInvalidInput) ||
		errors.Is(err, application.ErrNotFound)
}

func (s *Service) failPermanently(ctx context.Context, id uuid.UUID) error {
	return s.uow.Do(ctx, application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		tx, err := r.Wagers.Get(ctx, id)
		if err != nil {
			return err
		}
		if tx.Terminal() {
			return nil
		}
		if err := tx.Fail(s.clock.Now().UTC()); err != nil {
			return err
		}
		if err := r.Wagers.Update(ctx, tx, time.Time{}); err != nil {
			return err
		}
		s.metrics.WagerConcluded(tx.Kind(), wager.Failed, wager.PermanentFailure)
		return nil
	})
}
