package wagering_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

var brl = apptest.BRL

func TestBetIsProcessed(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "1000.00")

	res := h.Submit(t, h.Command(w, "BET", "b1", "25.00"))
	assert.Equal(t, wager.Processed, res.Status)
	assert.False(t, res.IdempotentReplay)
	assert.True(t, res.HasBalance)
	assert.True(t, brl("975.00").Equal(res.Balance))
	assert.True(t, brl("975.00").Equal(h.Balance(t, w.ID)))

	entries := h.Store.LedgerEntries(w.ID)
	require.Len(t, entries, 2)
	assert.Equal(t, wallet.Debit, entries[1].Direction())
	assert.Equal(t, res.TransactionID, entries[1].TransactionID())

	assert.Equal(t, int64(2), h.Store.Wallet(w.ID).Version)
	assert.Len(t, h.Store.OutboxByType(event.WagerTransactionProcessed), 2)
	assert.Len(t, h.Store.OutboxByType(event.WalletBalanceChanged), 2)
	assert.Equal(t, 1, h.Metrics.Concluded(wager.Bet, wager.Processed, ""))
}

func TestIdempotentReplayReturnsOriginalBalance(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "1000.00")
	cmd := h.Command(w, "BET", "b1", "25.00")

	first := h.Submit(t, cmd)
	h.Submit(t, h.Command(w, "BET", "b2", "100.00"))

	replay := h.Submit(t, cmd)
	assert.True(t, replay.IdempotentReplay)
	assert.Equal(t, first.TransactionID, replay.TransactionID)
	assert.True(t, brl("975.00").Equal(replay.Balance), "replay returns the balance observed originally, not the current one")
	assert.True(t, brl("875.00").Equal(h.Balance(t, w.ID)))
	assert.Len(t, h.Store.LedgerEntries(w.ID), 3, "replay creates no ledger entry")
	assert.Equal(t, 1, h.Metrics.Replays("http"))
}

func TestIdempotencyConflicts(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "1000.00")
	h.Submit(t, h.Command(w, "BET", "b1", "25.00"))

	_, err := h.Wagers.Submit(h.Ctx, h.ProviderA, h.Command(w, "BET", "b1", "26.00"))
	assert.ErrorIs(t, err, application.ErrConflict, "same key, different payload")
	assert.ErrorIs(t, err, wager.ErrPayloadConflict)

	_, err = h.Wagers.Submit(h.Ctx, h.ProviderA, h.Command(w, "BET", "b1", "25.00", apptest.WithKey("another-key")))
	assert.ErrorIs(t, err, application.ErrConflict, "same operation, different key")
	assert.ErrorIs(t, err, wager.ErrIdempotencyKeyMismatch)

	assert.True(t, brl("975.00").Equal(h.Balance(t, w.ID)))
	assert.Equal(t, 2, h.Store.WagerCount())

	foreign := h.Command(w, "BET", "b1", "5.00")
	foreign.ProviderID = "provider-b"
	res, err := h.Wagers.Submit(h.Ctx, h.ProviderB, foreign)
	require.NoError(t, err, "idempotency keys are scoped per provider")
	assert.Equal(t, wager.Processed, res.Status)
	assert.False(t, res.IdempotentReplay)
	assert.True(t, brl("970.00").Equal(h.Balance(t, w.ID)))
}

func TestSubmitAuthorizationAndInput(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "1000.00")

	_, err := h.Wagers.Submit(h.Ctx, h.ProviderB, h.Command(w, "BET", "b1", "25.00"))
	assert.ErrorIs(t, err, application.ErrForbidden, "provider-b cannot submit as provider-a")
	_, err = h.Wagers.Submit(h.Ctx, h.Internal, h.Command(w, "BET", "b1", "25.00"))
	assert.ErrorIs(t, err, application.ErrForbidden, "internal service does not submit wagers")

	invalid := map[string]apptest.CommandOption{
		"opening kind":       func(c *wagering.Command) { c.Kind = "OPENING" },
		"unknown kind":       func(c *wagering.Command) { c.Kind = "DEPOSIT" },
		"bet with zero":      func(c *wagering.Command) { c.Money.Amount = "0.00" },
		"loss with amount":   func(c *wagering.Command) { c.Kind = "LOSS" },
		"bad amount":         func(c *wagering.Command) { c.Money.Amount = "25" },
		"bad currency":       func(c *wagering.Command) { c.Money.Currency = "brl" },
		"missing key":        func(c *wagering.Command) { c.IdempotencyKey = " " },
		"bad wallet id":      func(c *wagering.Command) { c.WalletID = "w" },
		"bad player id":      func(c *wagering.Command) { c.PlayerID = "" },
		"refund without ref": func(c *wagering.Command) { c.Kind = "REFUND" },
		"bet with reference": func(c *wagering.Command) { c.ReferenceExternalTransactionID = "b0" },
		"empty round":        func(c *wagering.Command) { c.RoundID = "" },
		"self reference":     func(c *wagering.Command) { c.Kind = "ROLLBACK"; c.ReferenceExternalTransactionID = "b1" },
	}
	for name, mutate := range invalid {
		_, err := h.Wagers.Submit(h.Ctx, h.ProviderA, h.Command(w, "BET", "b1", "25.00", mutate))
		assert.ErrorIs(t, err, application.ErrInvalidInput, name)
	}

	_, err = h.Wagers.Submit(h.Ctx, h.ProviderA, h.Command(w, "BET", "b1", "25.00", func(c *wagering.Command) {
		c.WalletID = uuid.Must(uuid.NewV7()).String()
	}))
	assert.ErrorIs(t, err, application.ErrNotFound)

	assert.Equal(t, 1, h.Store.WagerCount(), "no rejected input is persisted")
	assert.True(t, brl("1000.00").Equal(h.Balance(t, w.ID)))
}

func TestBusinessRejections(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")
	other := h.OpenWallet(t, "100.00")

	res := h.Submit(t, h.Command(w, "BET", "b1", "100.01"))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.InsufficientFunds, res.FailureCode)
	assert.True(t, brl("100.00").Equal(res.Balance), "rejection carries the observed balance")

	res = h.Submit(t, h.Command(w, "BET", "b2", "10.00", func(c *wagering.Command) { c.PlayerID = other.PlayerID.String() }))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.WalletPlayerMismatch, res.FailureCode)

	res = h.Submit(t, h.Command(w, "BET", "b3", "10.00", apptest.WithCurrency("USD")))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.CurrencyMismatch, res.FailureCode)
	assert.False(t, res.HasBalance, "a BRL balance is never reported on a USD transaction")

	assert.True(t, brl("100.00").Equal(h.Balance(t, w.ID)))
	assert.Equal(t, int64(1), h.Store.Wallet(w.ID).Version)
	assert.Len(t, h.Store.LedgerEntries(w.ID), 1)
	assert.Len(t, h.Store.OutboxByType(event.WagerTransactionRejected), 3)
	assert.Equal(t, 1, h.Metrics.Concluded(wager.Bet, wager.Rejected, wager.InsufficientFunds))

	replay := h.Submit(t, h.Command(w, "BET", "b1", "100.01"))
	assert.True(t, replay.IdempotentReplay)
	assert.Equal(t, wager.Rejected, replay.Status)
	assert.Equal(t, wager.InsufficientFunds, replay.FailureCode)
}

func TestLossProducesNoMovement(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")

	res := h.Submit(t, h.Command(w, "LOSS", "l1", "0.00"))
	assert.Equal(t, wager.Processed, res.Status)
	assert.True(t, brl("100.00").Equal(res.Balance))
	assert.Equal(t, int64(1), h.Store.Wallet(w.ID).Version)
	assert.Len(t, h.Store.LedgerEntries(w.ID), 1)
	assert.Len(t, h.Store.OutboxByType(event.WagerTransactionProcessed), 2)
	assert.Len(t, h.Store.OutboxByType(event.WalletBalanceChanged), 1, "LOSS emits no balance change")
}

func TestWinCreditsAndOverflowIsRejected(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "0.00")

	res := h.Submit(t, h.Command(w, "WIN", "w1", "50.00"))
	assert.Equal(t, wager.Processed, res.Status)
	assert.True(t, brl("50.00").Equal(h.Balance(t, w.ID)))

	full := h.OpenWallet(t, "92233720368547758.07")
	res = h.Submit(t, h.Command(full, "WIN", "w2", "0.01"))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.BalanceLimitExceeded, res.FailureCode)
}

func TestQueriesRespectProviderIsolation(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")
	res := h.Submit(t, h.Command(w, "BET", "b1", "10.00"))

	view, err := h.Wagers.GetByID(h.Ctx, h.ProviderA, res.TransactionID)
	require.NoError(t, err)
	assert.Equal(t, wager.Processed, view.Status)
	assert.Equal(t, "provider-a", view.External.ProviderID)

	_, err = h.Wagers.GetByID(h.Ctx, h.ProviderB, res.TransactionID)
	assert.ErrorIs(t, err, application.ErrNotFound, "other providers see nothing, not even existence")
	_, err = h.Wagers.GetByID(h.Ctx, h.Internal, res.TransactionID)
	assert.NoError(t, err)

	_, err = h.Wagers.GetByExternalID(h.Ctx, h.ProviderA, "provider-a", "b1")
	assert.NoError(t, err)
	_, err = h.Wagers.GetByExternalID(h.Ctx, h.ProviderB, "provider-a", "b1")
	assert.ErrorIs(t, err, application.ErrNotFound)
	_, err = h.Wagers.GetByExternalID(h.Ctx, h.Internal, "provider-a", "b1")
	assert.NoError(t, err)
	_, err = h.Wagers.GetByExternalID(h.Ctx, h.ProviderA, "provider-a", "missing")
	assert.ErrorIs(t, err, application.ErrNotFound)

	entries := h.Store.LedgerEntries(w.ID)
	_, err = h.Wagers.GetByID(h.Ctx, h.ProviderA, entries[0].TransactionID())
	assert.ErrorIs(t, err, application.ErrNotFound, "internal OPENING is invisible to providers")
	opening, err := h.Wagers.GetByID(h.Ctx, h.Internal, entries[0].TransactionID())
	require.NoError(t, err)
	assert.Equal(t, wager.Opening, opening.Kind)
	assert.Nil(t, opening.External)
}

func TestTransientFailureLeavesNoPartialState(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")

	h.Store.FailNext(application.ErrUnavailable)
	_, err := h.Wagers.Submit(h.Ctx, h.ProviderA, h.Command(w, "BET", "b1", "10.00"))
	assert.ErrorIs(t, err, application.ErrUnavailable)
	assert.Equal(t, 1, h.Store.WagerCount())
	assert.True(t, brl("100.00").Equal(h.Balance(t, w.ID)))

	res := h.Submit(t, h.Command(w, "BET", "b1", "10.00"))
	assert.Equal(t, wager.Processed, res.Status)
	assert.False(t, res.IdempotentReplay, "nothing was persisted by the failed attempt")
}
