package wagering

import (
	"context"
	"errors"
	"fmt"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

func (s *Service) run(ctx context.Context, r application.Repos, p parsedCommand, source string) (Result, error) {
	now := s.clock.Now().UTC()

	w, err := r.Wallets.GetForUpdate(ctx, p.walletID)
	if err != nil {
		return Result{}, err
	}

	existing, err := s.findExisting(ctx, r, p)
	if err != nil {
		return Result{}, err
	}
	if existing != nil {
		if err := existing.CheckReplay(p.idempotencyKey, p.external.PayloadHash); err != nil {
			return Result{}, fmt.Errorf("%w: %w", application.ErrConflict, err)
		}
		s.metrics.IdempotentReplay(source)
		return resultOf(existing, true), nil
	}

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
