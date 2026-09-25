package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

var (
	errMissingIdempotencyKey = errors.New("httpapi: missing idempotency key")
	errInvalidCursor         = errors.New("httpapi: invalid cursor")
)

type MoneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func moneyDTO(m money.Money) MoneyDTO {
	return MoneyDTO{Amount: m.Amount(), Currency: m.Currency().Code()}
}

func moneyPtr(m money.Money, ok bool) *MoneyDTO {
	if !ok {
		return nil
	}
	dto := moneyDTO(m)
	return &dto
}

type OpenWalletRequest struct {
	PlayerID       string   `json:"playerId"`
	InitialBalance MoneyDTO `json:"initialBalance"`
}

type WalletResponse struct {
	ID        uuid.UUID `json:"id"`
	PlayerID  uuid.UUID `json:"playerId"`
	Balance   MoneyDTO  `json:"balance"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func walletResponse(v wallets.View) WalletResponse {
	return WalletResponse{
		ID:        v.ID,
		PlayerID:  v.PlayerID,
		Balance:   moneyDTO(v.Balance),
		Version:   v.Version,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
	}
}

type LedgerEntryResponse struct {
	ID            uuid.UUID `json:"id"`
	TransactionID uuid.UUID `json:"transactionId"`
	Direction     string    `json:"direction"`
	Money         MoneyDTO  `json:"money"`
	BalanceBefore MoneyDTO  `json:"balanceBefore"`
	BalanceAfter  MoneyDTO  `json:"balanceAfter"`
	CreatedAt     time.Time `json:"createdAt"`
}

type LedgerResponse struct {
	WalletID   uuid.UUID             `json:"walletId"`
	Entries    []LedgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func ledgerResponse(walletID uuid.UUID, page wallets.LedgerPage) LedgerResponse {
	out := LedgerResponse{WalletID: walletID, Entries: make([]LedgerEntryResponse, 0, len(page.Entries))}
	for _, e := range page.Entries {
		out.Entries = append(out.Entries, ledgerEntryResponse(e))
	}
	if page.HasMore {
		out.NextCursor = encodeCursor(page.Next)
	}
	return out
}

func ledgerEntryResponse(e wallet.LedgerEntry) LedgerEntryResponse {
	return LedgerEntryResponse{
		ID:            e.ID(),
		TransactionID: e.TransactionID(),
		Direction:     e.Direction().String(),
		Money:         moneyDTO(e.Amount()),
		BalanceBefore: moneyDTO(e.BalanceBefore()),
		BalanceAfter:  moneyDTO(e.BalanceAfter()),
		CreatedAt:     e.CreatedAt(),
	}
}

func encodeCursor(c application.LedgerCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (application.LedgerCursor, error) {
	if s == "" {
		return application.LedgerCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return application.LedgerCursor{}, errInvalidCursor
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return application.LedgerCursor{}, errInvalidCursor
	}
	createdAt, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return application.LedgerCursor{}, errInvalidCursor
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil {
		return application.LedgerCursor{}, errInvalidCursor
	}
	return application.LedgerCursor{CreatedAt: createdAt, ID: parsed}, nil
}

type ReconciliationResponse struct {
	WalletID          uuid.UUID `json:"walletId"`
	StoredBalance     MoneyDTO  `json:"storedBalance"`
	CalculatedBalance MoneyDTO  `json:"calculatedBalance"`
	Difference        MoneyDTO  `json:"difference"`
	Consistent        bool      `json:"consistent"`
	CheckedEntries    int       `json:"checkedEntries"`
}

func reconciliationResponse(r wallets.Reconciliation) ReconciliationResponse {
	return ReconciliationResponse{
		WalletID:          r.WalletID,
		StoredBalance:     moneyDTO(r.Stored),
		CalculatedBalance: moneyDTO(r.Calculated),
		Difference:        moneyDTO(r.Difference),
		Consistent:        r.Consistent,
		CheckedEntries:    r.CheckedEntries,
	}
}

type SubmitWagerRequest struct {
	ProviderID                     string   `json:"providerId"`
	ExternalTransactionID          string   `json:"externalTransactionId"`
	PlayerID                       string   `json:"playerId"`
	WalletID                       string   `json:"walletId"`
	RoundID                        string   `json:"roundId"`
	GameID                         string   `json:"gameId"`
	Kind                           string   `json:"kind"`
	Money                          MoneyDTO `json:"money"`
	ReferenceExternalTransactionID string   `json:"referenceExternalTransactionId,omitempty"`
}

func (r SubmitWagerRequest) command(idempotencyKey, correlationID string) wagering.Command {
	return wagering.Command{
		IdempotencyKey:                 idempotencyKey,
		ProviderID:                     r.ProviderID,
		ExternalTransactionID:          r.ExternalTransactionID,
		PlayerID:                       r.PlayerID,
		WalletID:                       r.WalletID,
		RoundID:                        r.RoundID,
		GameID:                         r.GameID,
		Kind:                           r.Kind,
		Money:                          application.MoneyInput{Amount: r.Money.Amount, Currency: r.Money.Currency},
		ReferenceExternalTransactionID: r.ReferenceExternalTransactionID,
		CorrelationID:                  correlationID,
	}
}

type WagerResultResponse struct {
	TransactionID    uuid.UUID `json:"transactionId"`
	Status           string    `json:"status"`
	Balance          *MoneyDTO `json:"balance,omitempty"`
	FailureCode      string    `json:"failureCode,omitempty"`
	Correctable      *bool     `json:"correctable,omitempty"`
	IdempotentReplay bool      `json:"idempotentReplay"`
}

func wagerResultResponse(res wagering.Result) WagerResultResponse {
	out := WagerResultResponse{
		TransactionID:    res.TransactionID,
		Status:           res.Status.String(),
		Balance:          moneyPtr(res.Balance, res.HasBalance),
		IdempotentReplay: res.IdempotentReplay,
	}
	if res.FailureCode != "" {
		out.FailureCode = res.FailureCode.String()
		correctable := res.FailureCode.Correctable()
		out.Correctable = &correctable
	}
	return out
}

type WagerViewResponse struct {
	TransactionID                  uuid.UUID  `json:"transactionId"`
	Kind                           string     `json:"kind"`
	Status                         string     `json:"status"`
	WalletID                       uuid.UUID  `json:"walletId"`
	PlayerID                       uuid.UUID  `json:"playerId"`
	Money                          MoneyDTO   `json:"money"`
	ProviderID                     string     `json:"providerId,omitempty"`
	ExternalTransactionID          string     `json:"externalTransactionId,omitempty"`
	RoundID                        string     `json:"roundId,omitempty"`
	GameID                         string     `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
	ResolvedReferenceID            *uuid.UUID `json:"resolvedReferenceId,omitempty"`
	FailureCode                    string     `json:"failureCode,omitempty"`
	Correctable                    *bool      `json:"correctable,omitempty"`
	Balance                        *MoneyDTO  `json:"balance,omitempty"`
	ReferenceAttempts              int        `json:"referenceAttempts"`
	CreatedAt                      time.Time  `json:"createdAt"`
	UpdatedAt                      time.Time  `json:"updatedAt"`
	CompletedAt                    *time.Time `json:"completedAt,omitempty"`
}

func wagerViewResponse(v wagering.View) WagerViewResponse {
	out := WagerViewResponse{
		TransactionID:     v.ID,
		Kind:              v.Kind.String(),
		Status:            v.Status.String(),
		WalletID:          v.WalletID,
		PlayerID:          v.PlayerID,
		Money:             moneyDTO(v.Amount),
		Balance:           moneyPtr(v.BalanceAfter, v.HasBalanceAfter),
		ReferenceAttempts: v.ReferenceAttempts,
		CreatedAt:         v.CreatedAt,
		UpdatedAt:         v.UpdatedAt,
	}
	if v.External != nil {
		out.ProviderID = v.External.ProviderID
		out.ExternalTransactionID = v.External.ExternalTransactionID
		out.RoundID = v.External.RoundID
		out.GameID = v.External.GameID
		out.ReferenceExternalTransactionID = v.External.ReferenceExternalTransactionID
	}
	if v.ResolvedReferenceID != uuid.Nil {
		id := v.ResolvedReferenceID
		out.ResolvedReferenceID = &id
	}
	if v.FailureCode != "" {
		out.FailureCode = v.FailureCode.String()
		out.Correctable = &v.Correctable
	}
	if !v.CompletedAt.IsZero() {
		completed := v.CompletedAt
		out.CompletedAt = &completed
	}
	return out
}

func pathUUID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a uuid", application.ErrInvalidInput, field)
	}
	return id, nil
}
