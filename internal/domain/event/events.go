package event

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

type TransactionRef struct {
	TransactionID         uuid.UUID  `json:"transactionId"`
	Kind                  wager.Kind `json:"kind"`
	WalletID              uuid.UUID  `json:"walletId"`
	PlayerID              uuid.UUID  `json:"playerId"`
	ProviderID            string     `json:"providerId,omitempty"`
	ExternalTransactionID string     `json:"externalTransactionId,omitempty"`
	RoundID               string     `json:"roundId,omitempty"`
	GameID                string     `json:"gameId,omitempty"`
}

func transactionRef(tx *wager.Transaction) TransactionRef {
	ext, _ := tx.External()
	return TransactionRef{
		TransactionID:         tx.ID(),
		Kind:                  tx.Kind(),
		WalletID:              tx.WalletID(),
		PlayerID:              tx.PlayerID(),
		ProviderID:            ext.ProviderID,
		ExternalTransactionID: ext.ExternalTransactionID,
		RoundID:               ext.RoundID,
		GameID:                ext.GameID,
	}
}

type WagerTransactionProcessedData struct {
	TransactionRef
	ReferenceTransactionID *uuid.UUID `json:"referenceTransactionId,omitempty"`
	Money                  Money      `json:"money"`
	BalanceAfter           Money      `json:"balanceAfter"`
	ProcessedAt            time.Time  `json:"processedAt"`
}

type WagerTransactionRejectedData struct {
	TransactionRef
	Money           Money             `json:"money"`
	FailureCode     wager.FailureCode `json:"failureCode"`
	Correctable     bool              `json:"correctable"`
	BalanceObserved *Money            `json:"balanceObserved,omitempty"`
	RejectedAt      time.Time         `json:"rejectedAt"`
}

type WagerTransactionPendingReferenceData struct {
	TransactionRef
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId"`
	Money                          Money     `json:"money"`
	Attempt                        int       `json:"attempt"`
	AttemptedAt                    time.Time `json:"attemptedAt"`
}

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	LedgerEntryID uuid.UUID        `json:"ledgerEntryId"`
	Direction     wallet.Direction `json:"direction"`
	Money         Money            `json:"money"`
	BalanceBefore Money            `json:"balanceBefore"`
	BalanceAfter  Money            `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
	ChangedAt     time.Time        `json:"changedAt"`
}

func NewWagerTransactionProcessed(tx *wager.Transaction, m Metadata) (Event, error) {
	if err := requireStatus(tx, wager.Processed); err != nil {
		return Event{}, err
	}
	balance, _ := tx.BalanceAfter()
	completed, _ := tx.CompletedAt()

	data := WagerTransactionProcessedData{
		TransactionRef: transactionRef(tx),
		Money:          moneyOf(tx.Amount()),
		BalanceAfter:   moneyOf(balance),
		ProcessedAt:    completed,
	}
	if ref, ok := tx.ResolvedReferenceID(); ok {
		data.ReferenceTransactionID = &ref
	}
	return newEvent(WagerTransactionProcessed, tx.ID(), m, data)
}

func NewWagerTransactionRejected(tx *wager.Transaction, m Metadata) (Event, error) {
	if err := requireStatus(tx, wager.Rejected); err != nil {
		return Event{}, err
	}
	if _, ok := tx.External(); !ok {
		return Event{}, fmt.Errorf("%w: rejection events require an external transaction", ErrInvalidEvent)
	}
	code, _ := tx.FailureCode()
	completed, _ := tx.CompletedAt()

	data := WagerTransactionRejectedData{
		TransactionRef: transactionRef(tx),
		Money:          moneyOf(tx.Amount()),
		FailureCode:    code,
		Correctable:    code.Correctable(),
		RejectedAt:     completed,
	}
	if balance, ok := tx.BalanceAfter(); ok {
		observed := moneyOf(balance)
		data.BalanceObserved = &observed
	}
	return newEvent(WagerTransactionRejected, tx.ID(), m, data)
}

func NewWagerTransactionPendingReference(tx *wager.Transaction, m Metadata) (Event, error) {
	if err := requireStatus(tx, wager.PendingReference); err != nil {
		return Event{}, err
	}
	_, reference, _ := tx.ReferenceKey()

	return newEvent(WagerTransactionPendingReference, tx.ID(), m, WagerTransactionPendingReferenceData{
		TransactionRef:                 transactionRef(tx),
		ReferenceExternalTransactionID: reference,
		Money:                          moneyOf(tx.Amount()),
		Attempt:                        tx.ReferenceAttempts(),
		AttemptedAt:                    tx.UpdatedAt(),
	})
}

func NewWalletBalanceChanged(w *wallet.Wallet, entry wallet.LedgerEntry, m Metadata) (Event, error) {
	if w == nil {
		return Event{}, fmt.Errorf("%w: nil wallet", ErrInvalidEvent)
	}
	if entry.ID() == uuid.Nil || entry.TransactionID() == uuid.Nil {
		return Event{}, fmt.Errorf("%w: incomplete ledger entry", ErrInvalidEvent)
	}
	if entry.WalletID() != w.ID() {
		return Event{}, fmt.Errorf("%w: ledger entry belongs to wallet %s, not %s", ErrInvalidEvent, entry.WalletID(), w.ID())
	}
	if !entry.BalanceAfter().Equal(w.Balance()) {
		return Event{}, fmt.Errorf("%w: ledger entry balance %s does not match wallet balance %s", ErrInvalidEvent, entry.BalanceAfter(), w.Balance())
	}
	if !entry.CreatedAt().Equal(w.UpdatedAt()) {
		return Event{}, fmt.Errorf("%w: ledger entry from %s is not the latest change of the wallet (%s)", ErrInvalidEvent, entry.CreatedAt(), w.UpdatedAt())
	}

	return newEvent(WalletBalanceChanged, w.ID(), m, WalletBalanceChangedData{
		WalletID:      w.ID(),
		TransactionID: entry.TransactionID(),
		LedgerEntryID: entry.ID(),
		Direction:     entry.Direction(),
		Money:         moneyOf(entry.Amount()),
		BalanceBefore: moneyOf(entry.BalanceBefore()),
		BalanceAfter:  moneyOf(entry.BalanceAfter()),
		WalletVersion: w.Version(),
		ChangedAt:     entry.CreatedAt(),
	})
}

func requireStatus(tx *wager.Transaction, want wager.Status) error {
	if tx == nil {
		return fmt.Errorf("%w: nil transaction", ErrInvalidEvent)
	}
	if tx.Status() != want {
		return fmt.Errorf("%w: transaction is %s, expected %s", ErrInvalidEvent, tx.Status(), want)
	}
	return nil
}
