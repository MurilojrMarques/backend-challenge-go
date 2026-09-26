package wagering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

func (s *Service) run(ctx context.Context, r application.Repos, p parsedCommand) (Result, error) {
	if err := p.external.Validate(p.kind); err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrInvalidInput, err)
	}

	existing, err := r.Wagers.GetByIdempotencyKey(ctx, p.external.ProviderID, p.idempotencyKey)
	if err == nil {
		return replayOf(existing, p)
	}
	if !errors.Is(err, application.ErrNotFound) {
		return Result{}, err
	}

	w, err := r.Wallets.GetForUpdate(ctx, p.walletID)
	if err != nil {
		return Result{}, err
	}
	existing, err = s.findExisting(ctx, r, p)
	if err != nil {
		return Result{}, err
	}
	if existing != nil {
		return replayOf(existing, p)
	}

	now := after(s.clock.Now().UTC(), w.UpdatedAt())
	tx, err := wager.NewExternal(wager.ExternalParams{
		ID:       application.NewID(),
		WalletID: p.walletID,
		PlayerID: p.playerID,
		Kind:     p.kind,
		Amount:   p.amount,
		External: p.external,
		Now:      now,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrInvalidInput, err)
	}

	ref, err := s.loadReference(ctx, r, tx)
	if err != nil {
		return Result{}, err
	}

	return s.conclude(ctx, r, conclusion{
		tx:            tx,
		wallet:        w,
		ref:           ref,
		verdict:       wager.Evaluate(tx, w, ref),
		now:           now,
		correlationID: application.CorrelationID(p.correlationID, tx.ID()),
		insert:        true,
	})
}

func replayOf(existing *wager.Transaction, p parsedCommand) (Result, error) {
	if err := existing.CheckReplay(p.idempotencyKey, p.external.PayloadHash); err != nil {
		return Result{}, fmt.Errorf("%w: %w", application.ErrConflict, err)
	}
	return resultOf(existing, true), nil
}

func after(now time.Time, marks ...time.Time) time.Time {
	for _, m := range marks {
		if !now.After(m) {
			now = m.Add(time.Microsecond)
		}
	}
	return now
}

func (s *Service) findExisting(ctx context.Context, r application.Repos, p parsedCommand) (*wager.Transaction, error) {
	tx, err := r.Wagers.GetByIdempotencyKey(ctx, p.external.ProviderID, p.idempotencyKey)
	if err == nil {
		return tx, nil
	}
	if !errors.Is(err, application.ErrNotFound) {
		return nil, err
	}
	tx, err = r.Wagers.GetByExternalID(ctx, p.external.ProviderID, p.external.ExternalTransactionID)
	if err == nil {
		return tx, nil
	}
	if errors.Is(err, application.ErrNotFound) {
		return nil, nil
	}
	return nil, err
}

func (s *Service) loadReference(ctx context.Context, r application.Repos, tx *wager.Transaction) (wager.Reference, error) {
	providerID, externalRef, ok := tx.ReferenceKey()
	if !ok {
		return wager.Reference{}, nil
	}
	ref, err := r.Wagers.GetByExternalID(ctx, providerID, externalRef)
	if errors.Is(err, application.ErrNotFound) {
		return wager.Reference{}, nil
	}
	if err != nil {
		return wager.Reference{}, err
	}
	reversed, err := r.Wagers.HasSuccessfulReversal(ctx, ref.ID())
	if err != nil {
		return wager.Reference{}, err
	}
	return wager.Reference{Transaction: ref, AlreadyReversed: reversed}, nil
}
