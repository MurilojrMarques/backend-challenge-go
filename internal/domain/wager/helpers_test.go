package wager_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

var (
	now = time.Date(2026, 9, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	usd = money.MustCurrency("USD")
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

func (f fixture) external(kind wager.Kind, amount money.Money, externalID, reference string) wager.ExternalParams {
	return wager.ExternalParams{
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
	}
}

func (f fixture) mustExternal(t *testing.T, kind wager.Kind, amount money.Money, externalID, reference string) *wager.Transaction {
	t.Helper()
	tx, err := wager.NewExternal(f.external(kind, amount, externalID, reference))
	require.NoError(t, err)
	return tx
}

func (f fixture) processed(t *testing.T, kind wager.Kind, amount money.Money, externalID, reference string) *wager.Transaction {
	t.Helper()
	tx := f.mustExternal(t, kind, amount, externalID, reference)
	if reference != "" {
		require.NoError(t, tx.ResolveReference(newID(), now))
	}
	require.NoError(t, tx.MarkProcessed(brl("900.00"), now))
	return tx
}

func (f fixture) processedBet(t *testing.T, amount money.Money, externalID string) *wager.Transaction {
	t.Helper()
	return f.processed(t, wager.Bet, amount, externalID, "")
}

func (f fixture) rejected(t *testing.T, kind wager.Kind, amount money.Money, externalID, reference string, code wager.FailureCode) *wager.Transaction {
	t.Helper()
	tx := f.mustExternal(t, kind, amount, externalID, reference)
	require.NoError(t, tx.Reject(code, brl("900.00"), now))
	return tx
}

func (f fixture) wallet(t *testing.T, balance money.Money) *wallet.Wallet {
	t.Helper()
	w, _, err := wallet.Open(wallet.OpenParams{
		ID:             f.walletID,
		PlayerID:       f.playerID,
		InitialBalance: balance,
		OpeningTxID:    newID(),
		OpeningEntryID: newID(),
		Now:            now,
	})
	require.NoError(t, err)
	return w
}
