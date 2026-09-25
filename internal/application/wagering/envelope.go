package wagering

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

const InboundMessageType = "WagerTransactionRequested"

type envelope struct {
	MessageID  string       `json:"messageId"`
	Type       string       `json:"type"`
	OccurredAt time.Time    `json:"occurredAt"`
	Data       envelopeData `json:"data"`
}

type envelopeData struct {
	ProviderID                     string        `json:"providerId"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	IdempotencyKey                 string        `json:"idempotencyKey"`
	PlayerID                       string        `json:"playerId"`
	WalletID                       string        `json:"walletId"`
	RoundID                        string        `json:"roundId"`
	GameID                         string        `json:"gameId"`
	Kind                           string        `json:"kind"`
	Money                          envelopeMoney `json:"money"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId,omitempty"`
}

type envelopeMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func ParseEnvelope(consumerName string, body []byte) (InboundMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var env envelope
	if err := dec.Decode(&env); err != nil {
		return InboundMessage{}, fmt.Errorf("%w: malformed envelope: %v", application.ErrInvalidInput, err)
	}
	if strings.TrimSpace(env.MessageID) == "" {
		return InboundMessage{}, fmt.Errorf("%w: envelope without messageId", application.ErrInvalidInput)
	}
	if env.Type != InboundMessageType {
		return InboundMessage{}, fmt.Errorf("%w: unsupported message type %q", application.ErrInvalidInput, env.Type)
	}

	sum := sha256.Sum256(body)
	return InboundMessage{
		ConsumerName: consumerName,
		MessageID:    env.MessageID,
		PayloadHash:  hex.EncodeToString(sum[:]),
		Command: Command{
			IdempotencyKey:                 env.Data.IdempotencyKey,
			ProviderID:                     env.Data.ProviderID,
			ExternalTransactionID:          env.Data.ExternalTransactionID,
			PlayerID:                       env.Data.PlayerID,
			WalletID:                       env.Data.WalletID,
			RoundID:                        env.Data.RoundID,
			GameID:                         env.Data.GameID,
			Kind:                           env.Data.Kind,
			Money:                          application.MoneyInput{Amount: env.Data.Money.Amount, Currency: env.Data.Money.Currency},
			ReferenceExternalTransactionID: env.Data.ReferenceExternalTransactionID,
			CorrelationID:                  env.MessageID,
		},
	}, nil
}
