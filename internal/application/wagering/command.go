package wagering

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

type Command struct {
	IdempotencyKey                 string
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	Money                          application.MoneyInput
	ReferenceExternalTransactionID string
	CorrelationID                  string
}

type parsedCommand struct {
	idempotencyKey string
	playerID       uuid.UUID
	walletID       uuid.UUID
	kind           wager.Kind
	amount         money.Money
	external       wager.External
	correlationID  string
}

func (c Command) parse() (parsedCommand, error) {
	if strings.TrimSpace(c.IdempotencyKey) == "" {
		return parsedCommand{}, fmt.Errorf("%w: idempotency key is required", application.ErrInvalidInput)
	}
	if len(c.IdempotencyKey) > wager.MaxFieldLength {
		return parsedCommand{}, fmt.Errorf("%w: idempotency key exceeds %d bytes", application.ErrInvalidInput, wager.MaxFieldLength)
	}

	kind, err := wager.ParseKind(c.Kind)
	if err != nil {
		return parsedCommand{}, fmt.Errorf("%w: %v", application.ErrInvalidInput, err)
	}
	if !kind.External() {
		return parsedCommand{}, fmt.Errorf("%w: %w", application.ErrInvalidInput, wager.ErrKindNotAllowed)
	}
	playerID, err := application.ParseUUID("playerId", c.PlayerID)
	if err != nil {
		return parsedCommand{}, err
	}
	walletID, err := application.ParseUUID("walletId", c.WalletID)
	if err != nil {
		return parsedCommand{}, err
	}
	amount, err := c.Money.Parse()
	if err != nil {
		return parsedCommand{}, err
	}

	hash, err := Fingerprint{
		ProviderID:                     c.ProviderID,
		ExternalTransactionID:          c.ExternalTransactionID,
		PlayerID:                       playerID.String(),
		WalletID:                       walletID.String(),
		RoundID:                        c.RoundID,
		GameID:                         c.GameID,
		Kind:                           kind.String(),
		Amount:                         amount.Amount(),
		Currency:                       amount.Currency().Code(),
		ReferenceExternalTransactionID: c.ReferenceExternalTransactionID,
	}.Hash()
	if err != nil {
		return parsedCommand{}, err
	}

	return parsedCommand{
		idempotencyKey: c.IdempotencyKey,
		playerID:       playerID,
		walletID:       walletID,
		kind:           kind,
		amount:         amount,
		external: wager.External{
			ProviderID:                     c.ProviderID,
			ExternalTransactionID:          c.ExternalTransactionID,
			IdempotencyKey:                 c.IdempotencyKey,
			PayloadHash:                    hash,
			RoundID:                        c.RoundID,
			GameID:                         c.GameID,
			ReferenceExternalTransactionID: c.ReferenceExternalTransactionID,
		},
		correlationID: c.CorrelationID,
	}, nil
}
