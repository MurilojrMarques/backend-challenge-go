package event_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

var (
	now  = time.Date(2026, 9, 24, 12, 0, 0, 500_000_000, time.FixedZone("BRT", -3*3600))
	meta = event.Metadata{CorrelationID: "corr-1", CausationID: "cause-1", OccurredAt: now}
)

func brl(amount string) money.Money {
	return money.MustParse(amount, money.BRL)
}

func newID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

type fixture struct {
	walletID uuid.UUID
	playerID uuid.UUID
}

func newFixture() fixture {
	return fixture{walletID: newID(), playerID: newID()}
}

func (f fixture) external(t *testing.T, kind wager.Kind, amount money.Money, externalID, reference string) *wager.Transaction {
	t.Helper()
	tx, err := wager.NewExternal(wager.ExternalParams{
		ID:       newID(),
		WalletID: f.walletID,
		PlayerID: f.playerID,
		Kind:     kind,
		Amount:   amount,
		External: wager.External{
			ProviderID:                     "provider-a",
			ExternalTransactionID:          externalID,
			IdempotencyKey:                 "provider-a:" + externalID,
			PayloadHash:                    "hash-" + externalID,
			RoundID:                        "round-1",
			GameID:                         "fortune-chimp",
			ReferenceExternalTransactionID: reference,
		},
		Now: now,
	})
	require.NoError(t, err)
	return tx
}

func (f fixture) debited(t *testing.T) (*wallet.Wallet, wallet.LedgerEntry) {
	t.Helper()
	w, _, err := wallet.Open(wallet.OpenParams{
		ID:             f.walletID,
		PlayerID:       f.playerID,
		InitialBalance: brl("1000.00"),
		OpeningTxID:    newID(),
		OpeningEntryID: newID(),
		Now:            now,
	})
	require.NoError(t, err)
	entry, err := w.Apply(wallet.Movement{
		EntryID:       newID(),
		TransactionID: newID(),
		Direction:     wallet.Debit,
		Amount:        brl("25.00"),
		Now:           now,
	})
	require.NoError(t, err)
	return w, entry
}

func assertEnvelope(t *testing.T, e event.Event, wantType event.Type, wantAggregate uuid.UUID) {
	t.Helper()
	assert.NotEqual(t, uuid.Nil, e.ID)
	assert.Equal(t, uuid.Version(7), e.ID.Version())
	assert.Equal(t, wantType, e.Type)
	assert.Equal(t, wantAggregate, e.AggregateID)
	assert.Equal(t, "corr-1", e.CorrelationID)
	assert.Equal(t, "cause-1", e.CausationID)
	assert.Equal(t, now.UTC(), e.OccurredAt)
	assert.Equal(t, time.UTC, e.OccurredAt.Location())
	assert.Equal(t, 1, e.Version)
}

func assertRef(t *testing.T, ref event.TransactionRef, tx *wager.Transaction) {
	t.Helper()
	ext, _ := tx.External()
	assert.Equal(t, tx.ID(), ref.TransactionID)
	assert.Equal(t, tx.Kind(), ref.Kind)
	assert.Equal(t, tx.WalletID(), ref.WalletID)
	assert.Equal(t, tx.PlayerID(), ref.PlayerID)
	assert.Equal(t, ext.ProviderID, ref.ProviderID)
	assert.Equal(t, ext.ExternalTransactionID, ref.ExternalTransactionID)
	assert.Equal(t, ext.RoundID, ref.RoundID)
	assert.Equal(t, ext.GameID, ref.GameID)
}

func TestTypes(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"WagerTransactionProcessed", "WagerTransactionRejected", "WagerTransactionPendingReference", "WalletBalanceChanged"} {
		typ, err := event.ParseType(s)
		require.NoError(t, err)
		assert.Equal(t, s, typ.String())
		assert.Equal(t, 1, typ.Version())
	}
	_, err := event.ParseType("WalletOpened")
	assert.ErrorIs(t, err, event.ErrUnknownType)
	assert.Equal(t, 0, event.Type("nope").Version())
}

func TestWagerTransactionProcessed(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx := f.external(t, wager.Rollback, brl("25.00"), "rb-1", "bet-1")
	refID := newID()
	require.NoError(t, tx.ResolveReference(refID, now))
	require.NoError(t, tx.MarkProcessed(brl("975.00"), now))

	e, err := event.NewWagerTransactionProcessed(tx, meta)
	require.NoError(t, err)
	assertEnvelope(t, e, event.WagerTransactionProcessed, tx.ID())

	data, ok := e.Data.(event.WagerTransactionProcessedData)
	require.True(t, ok)
	assertRef(t, data.TransactionRef, tx)
	require.NotNil(t, data.ReferenceTransactionID)
	assert.Equal(t, refID, *data.ReferenceTransactionID)
	assert.Equal(t, event.Money{Amount: "25.00", Currency: "BRL"}, data.Money)
	assert.Equal(t, event.Money{Amount: "975.00", Currency: "BRL"}, data.BalanceAfter)
	assert.Equal(t, now.UTC(), data.ProcessedAt)

	raw, err := e.Encode()
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"transactionId":"`+tx.ID().String()+`"`, "embedded ref is flattened")
	assert.Contains(t, string(raw), `"kind":"ROLLBACK"`)
}

func TestWagerTransactionProcessedForOpening(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx, err := wager.NewOpening(wager.OpeningParams{ID: newID(), WalletID: f.walletID, PlayerID: f.playerID, Amount: brl("1000.00"), Now: now})
	require.NoError(t, err)

	e, err := event.NewWagerTransactionProcessed(tx, meta)
	require.NoError(t, err)
	data := e.Data.(event.WagerTransactionProcessedData)
	assert.Equal(t, wager.Opening, data.Kind)
	assert.Empty(t, data.ProviderID)
	assert.Nil(t, data.ReferenceTransactionID)

	raw, err := e.Encode()
	require.NoError(t, err)
	for _, absent := range []string{"providerId", "externalTransactionId", "roundId", "gameId", "referenceTransactionId"} {
		assert.NotContains(t, string(raw), `"`+absent+`"`, "internal events omit %s", absent)
	}
}

func TestWagerTransactionProcessedRequiresProcessed(t *testing.T) {
	t.Parallel()

	f := newFixture()
	pending := f.external(t, wager.Bet, brl("25.00"), "b1", "")
	_, err := event.NewWagerTransactionProcessed(pending, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)

	rejected := f.external(t, wager.Bet, brl("25.00"), "b2", "")
	require.NoError(t, rejected.Reject(wager.InsufficientFunds, brl("10.00"), now))
	_, err = event.NewWagerTransactionProcessed(rejected, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)

	_, err = event.NewWagerTransactionProcessed(nil, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)
}

func TestWagerTransactionRejected(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx := f.external(t, wager.Bet, brl("80.00"), "b1", "")
	require.NoError(t, tx.Reject(wager.InsufficientFunds, brl("20.00"), now))

	e, err := event.NewWagerTransactionRejected(tx, meta)
	require.NoError(t, err)
	assertEnvelope(t, e, event.WagerTransactionRejected, tx.ID())

	data := e.Data.(event.WagerTransactionRejectedData)
	assertRef(t, data.TransactionRef, tx)
	assert.Equal(t, wager.InsufficientFunds, data.FailureCode)
	assert.False(t, data.Correctable)
	assert.Equal(t, event.Money{Amount: "80.00", Currency: "BRL"}, data.Money)
	require.NotNil(t, data.BalanceObserved)
	assert.Equal(t, event.Money{Amount: "20.00", Currency: "BRL"}, *data.BalanceObserved)
	assert.Equal(t, now.UTC(), data.RejectedAt)

	noBalance := f.external(t, wager.Bet, brl("80.00"), "b0", "")
	require.NoError(t, noBalance.Reject(wager.CurrencyMismatch, money.Money{}, now))
	e, err = event.NewWagerTransactionRejected(noBalance, meta)
	require.NoError(t, err)
	assert.Nil(t, e.Data.(event.WagerTransactionRejectedData).BalanceObserved)
	raw, err := e.Encode()
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "balanceObserved")

	correctable := f.external(t, wager.Bet, brl("80.00"), "b2", "")
	require.NoError(t, correctable.Reject(wager.CurrencyMismatch, brl("20.00"), now))
	e, err = event.NewWagerTransactionRejected(correctable, meta)
	require.NoError(t, err)
	assert.True(t, e.Data.(event.WagerTransactionRejectedData).Correctable)

	processed := f.external(t, wager.Bet, brl("1.00"), "b3", "")
	require.NoError(t, processed.MarkProcessed(brl("1.00"), now))
	_, err = event.NewWagerTransactionRejected(processed, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)
}

func TestWagerTransactionPendingReference(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx := f.external(t, wager.Refund, brl("25.00"), "rf-1", "bet-1")
	require.NoError(t, tx.AwaitReference(now))
	require.NoError(t, tx.AwaitReference(now.Add(time.Minute)))

	e, err := event.NewWagerTransactionPendingReference(tx, meta)
	require.NoError(t, err)
	assertEnvelope(t, e, event.WagerTransactionPendingReference, tx.ID())

	data := e.Data.(event.WagerTransactionPendingReferenceData)
	assertRef(t, data.TransactionRef, tx)
	assert.Equal(t, "bet-1", data.ReferenceExternalTransactionID)
	assert.Equal(t, 2, data.Attempt)
	assert.Equal(t, now.Add(time.Minute).UTC(), data.AttemptedAt)

	pending := f.external(t, wager.Refund, brl("25.00"), "rf-2", "bet-1")
	_, err = event.NewWagerTransactionPendingReference(pending, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)
}

func TestWalletBalanceChanged(t *testing.T) {
	t.Parallel()

	f := newFixture()
	w, entry := f.debited(t)

	e, err := event.NewWalletBalanceChanged(w, entry, meta)
	require.NoError(t, err)
	assertEnvelope(t, e, event.WalletBalanceChanged, f.walletID)

	data := e.Data.(event.WalletBalanceChangedData)
	assert.Equal(t, f.walletID, data.WalletID)
	assert.Equal(t, entry.TransactionID(), data.TransactionID)
	assert.Equal(t, entry.ID(), data.LedgerEntryID)
	assert.Equal(t, wallet.Debit, data.Direction)
	assert.Equal(t, event.Money{Amount: "25.00", Currency: "BRL"}, data.Money)
	assert.Equal(t, event.Money{Amount: "1000.00", Currency: "BRL"}, data.BalanceBefore)
	assert.Equal(t, event.Money{Amount: "975.00", Currency: "BRL"}, data.BalanceAfter)
	assert.Equal(t, int64(2), data.WalletVersion)
	assert.Equal(t, now.UTC(), data.ChangedAt)
}

func TestWalletBalanceChangedRejectsIncoherentInput(t *testing.T) {
	t.Parallel()

	f := newFixture()
	w, entry := f.debited(t)
	otherWallet, _ := newFixture().debited(t)

	_, err := event.NewWalletBalanceChanged(nil, entry, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)
	_, err = event.NewWalletBalanceChanged(w, wallet.LedgerEntry{}, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)
	_, err = event.NewWalletBalanceChanged(otherWallet, entry, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent, "entry from another wallet")

	stale, err := w.Apply(wallet.Movement{EntryID: newID(), TransactionID: newID(), Direction: wallet.Debit, Amount: brl("1.00"), Now: now})
	require.NoError(t, err)
	_, err = event.NewWalletBalanceChanged(w, entry, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent, "entry no longer matches the wallet balance")
	_, err = event.NewWalletBalanceChanged(w, stale, meta)
	assert.NoError(t, err, "latest entry matches")
}

func TestMetadataValidation(t *testing.T) {
	t.Parallel()

	f := newFixture()
	w, entry := f.debited(t)

	_, err := event.NewWalletBalanceChanged(w, entry, event.Metadata{OccurredAt: now})
	assert.ErrorIs(t, err, event.ErrInvalidEvent)
	_, err = event.NewWalletBalanceChanged(w, entry, event.Metadata{CorrelationID: "c"})
	assert.ErrorIs(t, err, event.ErrInvalidEvent)

	e, err := event.NewWalletBalanceChanged(w, entry, event.Metadata{CorrelationID: "c", OccurredAt: now})
	require.NoError(t, err)
	assert.Empty(t, e.CausationID)
	raw, err := e.Encode()
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "causationId")
}

func TestEncodeContract(t *testing.T) {
	t.Parallel()

	f := newFixture()
	w, entry := f.debited(t)
	e, err := event.NewWalletBalanceChanged(w, entry, meta)
	require.NoError(t, err)

	raw, err := e.Encode()
	require.NoError(t, err)

	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	for _, key := range []string{"eventId", "eventType", "aggregateId", "correlationId", "causationId", "occurredAt", "version", "data"} {
		assert.Contains(t, generic, key)
	}
	assert.Equal(t, "WalletBalanceChanged", generic["eventType"])
	assert.Equal(t, float64(1), generic["version"])

	occurredAt := generic["occurredAt"].(string)
	assert.True(t, strings.HasSuffix(occurredAt, "Z"), "timestamps are UTC: %s", occurredAt)
	_, err = time.Parse(time.RFC3339Nano, occurredAt)
	assert.NoError(t, err)

	data := generic["data"].(map[string]any)
	assert.Equal(t, map[string]any{"amount": "25.00", "currency": "BRL"}, data["money"])
	assert.Equal(t, "DEBIT", data["direction"])
	assert.Equal(t, float64(2), data["walletVersion"])
	assert.NotContains(t, string(raw), `"amount":25`, "money is never a JSON number")
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	f := newFixture()
	w, entry := f.debited(t)

	processed := f.external(t, wager.Rollback, brl("25.00"), "rb", "b1")
	require.NoError(t, processed.ResolveReference(newID(), now))
	require.NoError(t, processed.MarkProcessed(brl("975.00"), now))
	rejected := f.external(t, wager.Bet, brl("25.00"), "b2", "")
	require.NoError(t, rejected.Reject(wager.InsufficientFunds, brl("10.00"), now))
	pending := f.external(t, wager.Rollback, brl("25.00"), "rb2", "b1")
	require.NoError(t, pending.AwaitReference(now))

	events := []func() (event.Event, error){
		func() (event.Event, error) { return event.NewWagerTransactionProcessed(processed, meta) },
		func() (event.Event, error) { return event.NewWagerTransactionRejected(rejected, meta) },
		func() (event.Event, error) { return event.NewWagerTransactionPendingReference(pending, meta) },
		func() (event.Event, error) { return event.NewWalletBalanceChanged(w, entry, meta) },
	}

	for _, build := range events {
		original, err := build()
		require.NoError(t, err)

		raw, err := original.Encode()
		require.NoError(t, err)
		decoded, err := event.Decode(raw)
		require.NoError(t, err, original.Type)

		assert.Equal(t, original, decoded, original.Type)
	}
}

func TestDecodeRejectsInvalid(t *testing.T) {
	t.Parallel()

	f := newFixture()
	w, entry := f.debited(t)
	e, err := event.NewWalletBalanceChanged(w, entry, meta)
	require.NoError(t, err)
	raw, err := e.Encode()
	require.NoError(t, err)
	valid := string(raw)

	cases := map[string]struct {
		payload string
		want    error
	}{
		"not json":          {"{", event.ErrInvalidPayload},
		"unknown type":      {strings.Replace(valid, "WalletBalanceChanged", "WalletOpened", 1), event.ErrUnknownType},
		"wrong version":     {strings.Replace(valid, `"version":1`, `"version":2`, 1), event.ErrInvalidPayload},
		"unknown field":     {strings.Replace(valid, `"walletVersion"`, `"walletVersion":2,"extra"`, 1), event.ErrInvalidPayload},
		"missing eventId":   {strings.Replace(valid, e.ID.String(), uuid.Nil.String(), 1), event.ErrInvalidPayload},
		"empty correlation": {strings.Replace(valid, `"correlationId":"corr-1"`, `"correlationId":""`, 1), event.ErrInvalidPayload},
		"data type mismatch": {
			strings.Replace(valid, `"walletVersion":2`, `"walletVersion":"two"`, 1),
			event.ErrInvalidPayload,
		},
	}

	for name, tc := range cases {
		_, err := event.Decode([]byte(tc.payload))
		assert.ErrorIs(t, err, tc.want, name)
	}
}

func TestWalletBalanceChangedRejectsStaleEntry(t *testing.T) {
	t.Parallel()
	f := newFixture()
	w, first := f.debited(t)
	for i, direction := range []wallet.Direction{wallet.Credit, wallet.Debit} {
		_, err := w.Apply(wallet.Movement{
			EntryID:       newID(),
			TransactionID: newID(),
			Direction:     direction,
			Amount:        brl("25.00"),
			Now:           now.Add(time.Duration(i+1) * time.Second),
		})
		require.NoError(t, err)
	}
	require.True(t, first.BalanceAfter().Equal(w.Balance()), "the balance coincides with an older entry")

	_, err := event.NewWalletBalanceChanged(w, first, meta)
	assert.ErrorIs(t, err, event.ErrInvalidEvent)
}

func TestDecodeRejectsMissingDataAndUnknownEnvelopeFields(t *testing.T) {
	t.Parallel()
	f := newFixture()
	w, entry := f.debited(t)
	e, err := event.NewWalletBalanceChanged(w, entry, meta)
	require.NoError(t, err)
	raw, err := e.Encode()
	require.NoError(t, err)
	valid := string(raw)

	var encoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &encoded))
	data := string(encoded["data"])

	for name, payload := range map[string]string{
		"null data":              strings.Replace(valid, data, "null", 1),
		"unknown envelope field": strings.Replace(valid, `"version":1`, `"version":1,"extra":true`, 1),
		"trailing data":          valid + "{}",
	} {
		_, err := event.Decode([]byte(payload))
		assert.ErrorIs(t, err, event.ErrInvalidPayload, name)
	}
}
