package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
)

const InitialVersion int64 = 1

var (
	ErrInvalidWallet      = errors.New("wallet: invalid wallet")
	ErrInvalidLedgerEntry = errors.New("wallet: invalid ledger entry")
	ErrInvalidMovement    = errors.New("wallet: invalid movement")
	ErrCurrencyMismatch   = errors.New("wallet: currency mismatch")
	ErrInsufficientFunds  = errors.New("wallet: insufficient funds")
)

type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

type OpenParams struct {
	ID             uuid.UUID
	PlayerID       uuid.UUID
	InitialBalance money.Money
	OpeningTxID    uuid.UUID
	OpeningEntryID uuid.UUID
	Now            time.Time
}

func Open(p OpenParams) (*Wallet, *LedgerEntry, error) {
	if err := p.validate(); err != nil {
		return nil, nil, err
	}

	now := p.Now.UTC()
	w := &Wallet{
		id:        p.ID,
		playerID:  p.PlayerID,
		balance:   p.InitialBalance,
		version:   InitialVersion,
		createdAt: now,
		updatedAt: now,
	}
	if p.InitialBalance.IsZero() {
		return w, nil, nil
	}

	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID:            p.OpeningEntryID,
		WalletID:      p.ID,
		TransactionID: p.OpeningTxID,
		Direction:     Credit,
		Amount:        p.InitialBalance,
		BalanceBefore: money.Zero(p.InitialBalance.Currency()),
		BalanceAfter:  p.InitialBalance,
		CreatedAt:     now,
	})
	if err != nil {
		return nil, nil, err
	}
	return w, &entry, nil
}

func (p OpenParams) validate() error {
	if p.ID == uuid.Nil || p.PlayerID == uuid.Nil || p.Now.IsZero() {
		return fmt.Errorf("%w: missing identity or timestamp", ErrInvalidWallet)
	}
	if err := validBalance(p.InitialBalance); err != nil {
		return err
	}
	if p.InitialBalance.IsPositive() && (p.OpeningTxID == uuid.Nil || p.OpeningEntryID == uuid.Nil) {
		return fmt.Errorf("%w: opening transaction and entry are required for a positive initial balance", ErrInvalidWallet)
	}
	return nil
}

func validBalance(b money.Money) error {
	if b.Validate() != nil || b.IsNegative() {
		return fmt.Errorf("%w: balance must be a non-negative amount", ErrInvalidWallet)
	}
	return nil
}

type Snapshot struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func Rehydrate(s Snapshot) (*Wallet, error) {
	if err := s.validIdentity(); err != nil {
		return nil, err
	}
	if err := s.validTimes(); err != nil {
		return nil, err
	}
	if err := validBalance(s.Balance); err != nil {
		return nil, err
	}
	return &Wallet{
		id:        s.ID,
		playerID:  s.PlayerID,
		balance:   s.Balance,
		version:   s.Version,
		createdAt: s.CreatedAt.UTC(),
		updatedAt: s.UpdatedAt.UTC(),
	}, nil
}

func (s Snapshot) validIdentity() error {
	if s.ID == uuid.Nil || s.PlayerID == uuid.Nil || s.Version < InitialVersion {
		return fmt.Errorf("%w: invalid snapshot identity or version", ErrInvalidWallet)
	}
	return nil
}

func (s Snapshot) validTimes() error {
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return fmt.Errorf("%w: invalid snapshot timestamps", ErrInvalidWallet)
	}
	return nil
}

func (w *Wallet) Snapshot() Snapshot {
	return Snapshot{
		ID:        w.id,
		PlayerID:  w.playerID,
		Balance:   w.balance,
		Version:   w.version,
		CreatedAt: w.createdAt,
		UpdatedAt: w.updatedAt,
	}
}

type Movement struct {
	EntryID       uuid.UUID
	TransactionID uuid.UUID
	Direction     Direction
	Amount        money.Money
	Now           time.Time
}

func (w *Wallet) Apply(m Movement) (LedgerEntry, error) {
	if err := w.checkMovement(m); err != nil {
		return LedgerEntry{}, err
	}

	after, err := w.balance.Add(m.Direction.signed(m.Amount))
	if err != nil {
		return LedgerEntry{}, err
	}
	if after.IsNegative() {
		return LedgerEntry{}, fmt.Errorf("%w: balance %s, debit %s", ErrInsufficientFunds, w.balance, m.Amount)
	}

	at := m.Now.UTC()
	if at.Before(w.updatedAt) {
		at = w.updatedAt
	}
	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID:            m.EntryID,
		WalletID:      w.id,
		TransactionID: m.TransactionID,
		Direction:     m.Direction,
		Amount:        m.Amount,
		BalanceBefore: w.balance,
		BalanceAfter:  after,
		CreatedAt:     at,
	})
	if err != nil {
		return LedgerEntry{}, err
	}

	w.balance = after
	w.version++
	w.updatedAt = entry.CreatedAt()
	return entry, nil
}

func (w *Wallet) checkMovement(m Movement) error {
	if m.EntryID == uuid.Nil || m.TransactionID == uuid.Nil || m.Now.IsZero() {
		return fmt.Errorf("%w: missing identity or timestamp", ErrInvalidMovement)
	}
	if !m.Direction.Valid() {
		return fmt.Errorf("%w: unknown direction %q", ErrInvalidMovement, m.Direction)
	}
	if m.Amount.Validate() != nil || !m.Amount.IsPositive() {
		return fmt.Errorf("%w: amount must be positive", ErrInvalidMovement)
	}
	if m.Amount.Currency() != w.Currency() {
		return fmt.Errorf("%w: wallet %s, movement %s", ErrCurrencyMismatch, w.Currency(), m.Amount.Currency())
	}
	return nil
}

func (w *Wallet) CanDebit(amount money.Money) bool {
	cmp, err := w.balance.Cmp(amount)
	return err == nil && cmp >= 0
}

func (w *Wallet) ID() uuid.UUID            { return w.id }
func (w *Wallet) PlayerID() uuid.UUID      { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.balance.Currency() }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }
