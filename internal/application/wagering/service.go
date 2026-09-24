package wagering

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

type Options struct {
	ReferencePolicy  wager.ReferencePolicy
	ReferenceBackoff Backoff
}

type Service struct {
	uow     application.UnitOfWork
	clock   application.Clock
	metrics application.Metrics
	logger  *slog.Logger
	opts    Options
}

func NewService(uow application.UnitOfWork, clock application.Clock, metrics application.Metrics, logger *slog.Logger, opts Options) *Service {
	return &Service{uow: uow, clock: clock, metrics: metrics, logger: logger, opts: opts}
}

type Result struct {
	TransactionID    uuid.UUID
	Status           wager.Status
	Balance          money.Money
	HasBalance       bool
	FailureCode      wager.FailureCode
	IdempotentReplay bool
}

func resultOf(tx *wager.Transaction, replay bool) Result {
	balance, hasBalance := tx.BalanceAfter()
	code, _ := tx.FailureCode()
	return Result{
		TransactionID:    tx.ID(),
		Status:           tx.Status(),
		Balance:          balance,
		HasBalance:       hasBalance,
		FailureCode:      code,
		IdempotentReplay: replay,
	}
}

type View struct {
	ID                  uuid.UUID
	Kind                wager.Kind
	Status              wager.Status
	WalletID            uuid.UUID
	PlayerID            uuid.UUID
	Amount              money.Money
	External            *wager.External
	ResolvedReferenceID uuid.UUID
	FailureCode         wager.FailureCode
	Correctable         bool
	BalanceAfter        money.Money
	HasBalanceAfter     bool
	ReferenceAttempts   int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CompletedAt         time.Time
}

func viewOf(tx *wager.Transaction) View {
	s := tx.Snapshot()
	return View{
		ID:                  s.ID,
		Kind:                s.Kind,
		Status:              s.Status,
		WalletID:            s.WalletID,
		PlayerID:            s.PlayerID,
		Amount:              s.Amount,
		External:            s.External,
		ResolvedReferenceID: s.ResolvedReferenceID,
		FailureCode:         s.FailureCode,
		Correctable:         s.FailureCode.Correctable(),
		BalanceAfter:        s.BalanceAfter,
		HasBalanceAfter:     s.BalanceAfter.Validate() == nil,
		ReferenceAttempts:   s.ReferenceAttempts,
		CreatedAt:           s.CreatedAt,
		UpdatedAt:           s.UpdatedAt,
		CompletedAt:         s.CompletedAt,
	}
}

func (s *Service) Submit(ctx context.Context, principal application.Principal, cmd Command) (Result, error) {
	if err := principal.RequireProvider(cmd.ProviderID); err != nil {
		return Result{}, err
	}
	p, err := cmd.parse()
	if err != nil {
		return Result{}, err
	}

	var result Result
	err = s.uow.Do(ctx, application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		out, err := s.run(ctx, r, p, "http")
		if err != nil {
			return err
		}
		result = out
		return nil
	})
	return result, err
}

func (s *Service) GetByID(ctx context.Context, principal application.Principal, id uuid.UUID) (View, error) {
	if err := principal.Validate(); err != nil {
		return View{}, err
	}
	var view View
	err := s.uow.Do(ctx, application.TxOptions{ReadOnly: true}, func(ctx context.Context, r application.Repos) error {
		tx, err := r.Wagers.Get(ctx, id)
		if err != nil {
			return err
		}
		if !visibleTo(principal, tx) {
			return application.ErrNotFound
		}
		view = viewOf(tx)
		return nil
	})
	return view, err
}

func (s *Service) GetByExternalID(ctx context.Context, principal application.Principal, providerID, externalTransactionID string) (View, error) {
	if err := principal.Validate(); err != nil {
		return View{}, err
	}
	if !principal.IsInternal() && !principal.CanActAsProvider(providerID) {
		return View{}, application.ErrNotFound
	}
	var view View
	err := s.uow.Do(ctx, application.TxOptions{ReadOnly: true}, func(ctx context.Context, r application.Repos) error {
		tx, err := r.Wagers.GetByExternalID(ctx, providerID, externalTransactionID)
		if err != nil {
			return err
		}
		view = viewOf(tx)
		return nil
	})
	return view, err
}

func visibleTo(principal application.Principal, tx *wager.Transaction) bool {
	if principal.IsInternal() {
		return true
	}
	ext, ok := tx.External()
	return ok && principal.CanActAsProvider(ext.ProviderID)
}
