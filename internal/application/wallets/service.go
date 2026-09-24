package wallets

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

const (
	DefaultLedgerLimit = 50
	MaxLedgerLimit     = 200
)

type OpenCommand struct {
	PlayerID       string
	InitialBalance application.MoneyInput
	CorrelationID  string
}

type View struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func viewOf(w *wallet.Wallet) View {
	return View{
		ID:        w.ID(),
		PlayerID:  w.PlayerID(),
		Balance:   w.Balance(),
		Version:   w.Version(),
		CreatedAt: w.CreatedAt(),
		UpdatedAt: w.UpdatedAt(),
	}
}

type LedgerPage struct {
	Entries []wallet.LedgerEntry
	Next    application.LedgerCursor
	HasMore bool
}

type Reconciliation struct {
	WalletID       uuid.UUID
	Stored         money.Money
	Calculated     money.Money
	Difference     money.Money
	Consistent     bool
	CheckedEntries int
}

type Service struct {
	uow     application.UnitOfWork
	clock   application.Clock
	metrics application.Metrics
	logger  *slog.Logger
}

func NewService(uow application.UnitOfWork, clock application.Clock, metrics application.Metrics, logger *slog.Logger) *Service {
	return &Service{uow: uow, clock: clock, metrics: metrics, logger: logger}
}

func (s *Service) Open(ctx context.Context, principal application.Principal, cmd OpenCommand) (View, error) {
	if err := principal.RequireInternal(); err != nil {
		return View{}, err
	}
	playerID, err := application.ParseUUID("playerId", cmd.PlayerID)
	if err != nil {
		return View{}, err
	}
	initial, err := cmd.InitialBalance.Parse()
	if err != nil {
		return View{}, err
	}

	now := s.clock.Now().UTC()
	walletID := application.NewID()
	var openingTxID, openingEntryID uuid.UUID
	if initial.IsPositive() {
		openingTxID, openingEntryID = application.NewID(), application.NewID()
	}

	w, entry, err := wallet.Open(wallet.OpenParams{
		ID:             walletID,
		PlayerID:       playerID,
		InitialBalance: initial,
		OpeningTxID:    openingTxID,
		OpeningEntryID: openingEntryID,
		Now:            now,
	})
	if err != nil {
		return View{}, fmt.Errorf("%w: %w", application.ErrInvalidInput, err)
	}

	err = s.uow.Do(ctx, application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		if err := r.Wallets.Create(ctx, w); err != nil {
			return err
		}
		if entry == nil {
			return nil
		}
		opening, err := wager.NewOpening(wager.OpeningParams{
			ID: openingTxID, WalletID: walletID, PlayerID: playerID, Amount: initial, Now: now,
		})
		if err != nil {
			return fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		if err := r.Wagers.Insert(ctx, opening, time.Time{}); err != nil {
			return err
		}
		if err := r.Ledger.Append(ctx, *entry); err != nil {
			return err
		}

		meta := event.Metadata{
			CorrelationID: application.CorrelationID(cmd.CorrelationID, walletID),
			CausationID:   openingTxID.String(),
			OccurredAt:    now,
		}
		processed, err := event.NewWagerTransactionProcessed(opening, meta)
		if err != nil {
			return fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		changed, err := event.NewWalletBalanceChanged(w, *entry, meta)
		if err != nil {
			return fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		return r.Outbox.Append(ctx, processed, changed)
	})
	if err != nil {
		return View{}, err
	}
	return viewOf(w), nil
}

func (s *Service) Get(ctx context.Context, principal application.Principal, walletID uuid.UUID) (View, error) {
	if err := principal.RequireInternal(); err != nil {
		return View{}, err
	}
	var view View
	err := s.uow.Do(ctx, application.TxOptions{ReadOnly: true}, func(ctx context.Context, r application.Repos) error {
		w, err := r.Wallets.Get(ctx, walletID)
		if err != nil {
			return err
		}
		view = viewOf(w)
		return nil
	})
	return view, err
}

func (s *Service) Ledger(ctx context.Context, principal application.Principal, walletID uuid.UUID, after application.LedgerCursor, limit int) (LedgerPage, error) {
	if err := principal.RequireInternal(); err != nil {
		return LedgerPage{}, err
	}
	if limit <= 0 {
		limit = DefaultLedgerLimit
	}
	if limit > MaxLedgerLimit {
		return LedgerPage{}, fmt.Errorf("%w: limit must be at most %d", application.ErrInvalidInput, MaxLedgerLimit)
	}

	var page LedgerPage
	err := s.uow.Do(ctx, application.TxOptions{ReadOnly: true}, func(ctx context.Context, r application.Repos) error {
		if _, err := r.Wallets.Get(ctx, walletID); err != nil {
			return err
		}
		entries, err := r.Ledger.List(ctx, walletID, after, limit+1)
		if err != nil {
			return err
		}
		if len(entries) > limit {
			entries = entries[:limit]
			page.HasMore = true
		}
		page.Entries = entries
		if len(entries) > 0 {
			last := entries[len(entries)-1]
			page.Next = application.LedgerCursor{CreatedAt: last.CreatedAt(), ID: last.ID()}
		}
		return nil
	})
	return page, err
}

func (s *Service) Reconcile(ctx context.Context, principal application.Principal, walletID uuid.UUID) (Reconciliation, error) {
	if err := principal.RequireInternal(); err != nil {
		return Reconciliation{}, err
	}

	var rec Reconciliation
	err := s.uow.Do(ctx, application.TxOptions{Isolation: application.RepeatableRead, ReadOnly: true}, func(ctx context.Context, r application.Repos) error {
		w, err := r.Wallets.Get(ctx, walletID)
		if err != nil {
			return err
		}
		totals, err := r.Ledger.Totals(ctx, walletID)
		if err != nil {
			return err
		}
		credits, err := money.FromUnits(totals.CreditUnits, w.Currency())
		if err != nil {
			return fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		debits, err := money.FromUnits(totals.DebitUnits, w.Currency())
		if err != nil {
			return fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		calculated, err := credits.Sub(debits)
		if err != nil {
			return fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		difference, err := w.Balance().Sub(calculated)
		if err != nil {
			return fmt.Errorf("%w: %w", application.ErrIntegrity, err)
		}
		rec = Reconciliation{
			WalletID:       walletID,
			Stored:         w.Balance(),
			Calculated:     calculated,
			Difference:     difference,
			Consistent:     difference.IsZero(),
			CheckedEntries: totals.Entries,
		}
		return nil
	})
	if err != nil {
		return Reconciliation{}, err
	}

	s.metrics.ReconciliationChecked(rec.Consistent)
	if !rec.Consistent {
		s.logger.ErrorContext(ctx, "wallet reconciliation divergence",
			"walletId", walletID,
			"storedBalance", rec.Stored.Amount(),
			"calculatedBalance", rec.Calculated.Amount(),
			"difference", rec.Difference.Amount(),
			"checkedEntries", rec.CheckedEntries,
		)
	}
	return rec, nil
}
