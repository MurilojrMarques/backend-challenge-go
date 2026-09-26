package application

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

type Isolation int

const (
	ReadCommitted Isolation = iota
	RepeatableRead
	Serializable
)

type TxOptions struct {
	Isolation Isolation
	ReadOnly  bool
}

type Repos struct {
	Wallets WalletRepository
	Wagers  WagerRepository
	Ledger  LedgerRepository
	Inbox   InboxRepository
	Outbox  OutboxRepository
}

type UnitOfWork interface {
	Do(ctx context.Context, opts TxOptions, fn func(ctx context.Context, r Repos) error) error
}

type WalletRepository interface {
	Create(ctx context.Context, w *wallet.Wallet) error
	Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	Save(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error
}

type WagerRepository interface {
	Insert(ctx context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error
	Update(ctx context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error
	Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error)
	GetByIdempotencyKey(ctx context.Context, providerID, key string) (*wager.Transaction, error)
	GetByExternalID(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error)
	HasSuccessfulReversal(ctx context.Context, referenceID uuid.UUID) (bool, error)
	ListDuePendingReferences(ctx context.Context, now time.Time, limit int) ([]*wager.Transaction, error)
}

type LedgerCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func (c LedgerCursor) IsZero() bool {
	return c.CreatedAt.IsZero() && c.ID == uuid.Nil
}

type LedgerTotals struct {
	CreditUnits int64
	DebitUnits  int64
	Entries     int
}

type LedgerRepository interface {
	Append(ctx context.Context, entry wallet.LedgerEntry) error
	List(ctx context.Context, walletID uuid.UUID, after LedgerCursor, limit int) ([]wallet.LedgerEntry, error)
	Totals(ctx context.Context, walletID uuid.UUID) (LedgerTotals, error)
}

type InboxRepository interface {
	Insert(ctx context.Context, rec InboxRecord) error
	Get(ctx context.Context, consumerName, messageID string) (InboxRecord, error)
	Complete(ctx context.Context, consumerName, messageID string, transactionID uuid.UUID, at time.Time) error
}

type OutboxRepository interface {
	Append(ctx context.Context, events ...event.Event) error
	Claim(ctx context.Context, publisher string, now time.Time, lease time.Duration, limit int) ([]OutboxRecord, error)
	MarkPublished(ctx context.Context, eventID uuid.UUID, publisher string, at time.Time) error
	Release(ctx context.Context, eventID uuid.UUID, publisher string, nextAttemptAt time.Time, lastError string) error
	OldestPending(ctx context.Context) (time.Time, int, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

type Metrics interface {
	WagerConcluded(kind wager.Kind, status wager.Status, code wager.FailureCode)
	IdempotentReplay(source string)
	ConcurrencyConflict(operation string)
	ReconciliationChecked(consistent bool)
	MessageHandled(outcome string)
	OutboxPublished(eventType event.Type)
	OutboxRetried(eventType event.Type)
	OutboxLag(oldest time.Duration, pending int)
}

type NopMetrics struct{}

func (NopMetrics) WagerConcluded(wager.Kind, wager.Status, wager.FailureCode) {}
func (NopMetrics) IdempotentReplay(string)                                    {}
func (NopMetrics) ConcurrencyConflict(string)                                 {}
func (NopMetrics) ReconciliationChecked(bool)                                 {}
func (NopMetrics) MessageHandled(string)                                      {}
func (NopMetrics) OutboxPublished(event.Type)                                 {}
func (NopMetrics) OutboxRetried(event.Type)                                   {}
func (NopMetrics) OutboxLag(time.Duration, int)                               {}

type Message struct {
	ID            string
	GroupID       string
	ReceiptHandle string
	Body          []byte
	ReceiveCount  int
}

type Queue interface {
	Receive(ctx context.Context) ([]Message, error)
	Delete(ctx context.Context, receiptHandle string) error
	ChangeVisibility(ctx context.Context, receiptHandle string, timeout time.Duration) error
	SendToDeadLetter(ctx context.Context, msg Message, reason string) error
}

type EventPublisher interface {
	Publish(ctx context.Context, rec OutboxRecord) error
}

type HealthChecker interface {
	Name() string
	Check(ctx context.Context) error
}
