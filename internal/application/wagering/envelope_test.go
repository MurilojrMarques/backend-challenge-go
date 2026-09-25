package wagering_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
)

const validEnvelope = `{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}`

func TestParseEnvelope(t *testing.T) {
	t.Parallel()

	msg, err := wagering.ParseEnvelope("wager-transactions", []byte(validEnvelope))
	require.NoError(t, err)
	assert.Equal(t, "wager-transactions", msg.ConsumerName)
	assert.Equal(t, "msg-123", msg.MessageID)
	assert.Len(t, msg.PayloadHash, 64)
	assert.Equal(t, "provider-a:transaction-123", msg.Command.IdempotencyKey)
	assert.Equal(t, "provider-a", msg.Command.ProviderID)
	assert.Equal(t, "BET", msg.Command.Kind)
	assert.Equal(t, application.MoneyInput{Amount: "25.00", Currency: "BRL"}, msg.Command.Money)
	assert.Equal(t, "msg-123", msg.Command.CorrelationID)

	again, err := wagering.ParseEnvelope("wager-transactions", []byte(validEnvelope))
	require.NoError(t, err)
	assert.Equal(t, msg.PayloadHash, again.PayloadHash, "hash is deterministic over the raw body")

	tampered, err := wagering.ParseEnvelope("wager-transactions", []byte(validEnvelope+" "))
	require.NoError(t, err)
	assert.NotEqual(t, msg.PayloadHash, tampered.PayloadHash, "any byte change changes the hash")
}

func TestParseEnvelopeRejectsInvalid(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not json":      `{`,
		"unknown field": `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{},"extra":1}`,
		"missing id":    `{"type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{}}`,
		"wrong type":    `{"messageId":"m","type":"SomethingElse","occurredAt":"2026-09-08T12:00:00Z","data":{}}`,
		"bad timestamp": `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"yesterday","data":{}}`,
		"amount number": `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"money":{"amount":25}}}`,
	}
	for name, body := range cases {
		_, err := wagering.ParseEnvelope("c", []byte(body))
		assert.ErrorIs(t, err, application.ErrInvalidInput, name)
	}
}
