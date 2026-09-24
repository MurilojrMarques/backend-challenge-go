package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
)

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

func ParseDirection(s string) (Direction, error) {
	d := Direction(s)
	if !d.Valid() {
		return "", fmt.Errorf("%w: unknown direction %q", ErrInvalidLedgerEntry, s)
	}
	return d, nil
}

func (d Direction) Valid() bool {
	return d == Debit || d == Credit
}

func (d Direction) String() string {
	return string(d)
}

func (d Direction) signed(amount money.Money) money.Money {
	if d == Debit {
		return amount.Negate()
	}
	return amount
}

type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

type LedgerEntryParams struct {
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	CreatedAt     time.Time
}

func NewLedgerEntry(p LedgerEntryParams) (LedgerEntry, error) {
	if err := p.validIdentity(); err != nil {
		return LedgerEntry{}, err
	}
	if err := p.validAmounts(); err != nil {
		return LedgerEntry{}, err
	}
	return LedgerEntry{
		id:            p.ID,
		walletID:      p.WalletID,
		transactionID: p.TransactionID,
		direction:     p.Direction,
		amount:        p.Amount,
		balanceBefore: p.BalanceBefore,
		balanceAfter:  p.BalanceAfter,
		createdAt:     p.CreatedAt.UTC(),
	}, nil
}

func (p LedgerEntryParams) validIdentity() error {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.TransactionID == uuid.Nil {
		return fmt.Errorf("%w: missing identifiers", ErrInvalidLedgerEntry)
	}
	if !p.Direction.Valid() {
		return fmt.Errorf("%w: unknown direction %q", ErrInvalidLedgerEntry, p.Direction)
	}
	if p.CreatedAt.IsZero() {
		return fmt.Errorf("%w: missing timestamp", ErrInvalidLedgerEntry)
	}
	return nil
}

func (p LedgerEntryParams) validAmounts() error {
	if p.Amount.Validate() != nil || !p.Amount.IsPositive() {
		return fmt.Errorf("%w: amount must be positive", ErrInvalidLedgerEntry)
	}
	for _, b := range []money.Money{p.BalanceBefore, p.BalanceAfter} {
		if b.Validate() != nil || b.IsNegative() {
			return fmt.Errorf("%w: balances must be non-negative", ErrInvalidLedgerEntry)
		}
	}

	expected, err := p.BalanceBefore.Add(p.Direction.signed(p.Amount))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, err)
	}
	if !expected.Equal(p.BalanceAfter) {
		return fmt.Errorf("%w: %s %s %s should give %s, got %s",
			ErrInvalidLedgerEntry, p.BalanceBefore, p.Direction, p.Amount, expected, p.BalanceAfter)
	}
	return nil
}

func (e LedgerEntry) ID() uuid.UUID {
	return e.id
}

func (e LedgerEntry) WalletID() uuid.UUID {
	return e.walletID
}

func (e LedgerEntry) TransactionID() uuid.UUID {
	return e.transactionID
}

func (e LedgerEntry) Direction() Direction {
	return e.direction
}

func (e LedgerEntry) Amount() money.Money {
	return e.amount
}

func (e LedgerEntry) BalanceBefore() money.Money {
	return e.balanceBefore
}

func (e LedgerEntry) BalanceAfter() money.Money {
	return e.balanceAfter
}

func (e LedgerEntry) CreatedAt() time.Time {
	return e.createdAt
}
