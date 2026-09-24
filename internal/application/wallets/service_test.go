package wallets_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

func TestOpenWithPositiveBalance(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)

	view := h.OpenWallet(t, "1000.00")
	assert.True(t, apptest.BRL("1000.00").Equal(view.Balance))
	assert.Equal(t, wallet.InitialVersion, view.Version)

	entries := h.Store.LedgerEntries(view.ID)
	require.Len(t, entries, 1)
	assert.Equal(t, wallet.Credit, entries[0].Direction())
	assert.True(t, apptest.BRL("0.00").Equal(entries[0].BalanceBefore()))
	assert.True(t, apptest.BRL("1000.00").Equal(entries[0].BalanceAfter()))

	opening := h.Store.Wager(entries[0].TransactionID())
	assert.Equal(t, wager.Opening, opening.Kind)
	assert.Equal(t, wager.Processed, opening.Status)
	assert.Nil(t, opening.External)

	assert.Len(t, h.Store.OutboxByType(event.WagerTransactionProcessed), 1)
	assert.Len(t, h.Store.OutboxByType(event.WalletBalanceChanged), 1)
	assert.Equal(t, 2, h.Store.OutboxCount())
}

func TestOpenWithZeroBalance(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)

	view := h.OpenWallet(t, "0.00")
	assert.True(t, view.Balance.IsZero())
	assert.Empty(t, h.Store.LedgerEntries(view.ID))
	assert.Equal(t, 0, h.Store.WagerCount())
	assert.Equal(t, 0, h.Store.OutboxCount())
}

func TestOpenRejections(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)

	playerID := uuid.Must(uuid.NewV7()).String()
	valid := wallets.OpenCommand{PlayerID: playerID, InitialBalance: application.MoneyInput{Amount: "10.00", Currency: "BRL"}}

	_, err := h.Wallets.Open(h.Ctx, h.ProviderA, valid)
	assert.ErrorIs(t, err, application.ErrForbidden)

	_, err = h.Wallets.Open(h.Ctx, h.Internal, valid)
	require.NoError(t, err)
	_, err = h.Wallets.Open(h.Ctx, h.Internal, valid)
	assert.ErrorIs(t, err, application.ErrConflict, "same player and currency")

	usd := valid
	usd.InitialBalance.Currency = "USD"
	_, err = h.Wallets.Open(h.Ctx, h.Internal, usd)
	assert.NoError(t, err, "same player, other currency is a different wallet")

	invalid := map[string]wallets.OpenCommand{
		"bad player id":   {PlayerID: "nope", InitialBalance: valid.InitialBalance},
		"nil player id":   {PlayerID: uuid.Nil.String(), InitialBalance: valid.InitialBalance},
		"bad amount":      {PlayerID: playerID, InitialBalance: application.MoneyInput{Amount: "10", Currency: "BRL"}},
		"negative amount": {PlayerID: playerID, InitialBalance: application.MoneyInput{Amount: "-1.00", Currency: "BRL"}},
		"bad currency":    {PlayerID: playerID, InitialBalance: application.MoneyInput{Amount: "1.00", Currency: "JPY"}},
	}
	for name, cmd := range invalid {
		_, err := h.Wallets.Open(h.Ctx, h.Internal, cmd)
		assert.ErrorIs(t, err, application.ErrInvalidInput, name)
	}
	assert.Equal(t, 2, h.Store.WalletCount())
	assert.Equal(t, 4, h.Store.OutboxCount(), "only the two successful openings produced events")
}

func TestGetAndAuthorization(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "5.00")

	view, err := h.Wallets.Get(h.Ctx, h.Internal, w.ID)
	require.NoError(t, err)
	assert.Equal(t, w, view)

	_, err = h.Wallets.Get(h.Ctx, h.ProviderA, w.ID)
	assert.ErrorIs(t, err, application.ErrForbidden)
	_, err = h.Wallets.Get(h.Ctx, h.Internal, uuid.Must(uuid.NewV7()))
	assert.ErrorIs(t, err, application.ErrNotFound)
}

func TestLedgerPagination(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")
	for i, amount := range []string{"1.00", "2.00", "3.00", "4.00"} {
		h.Submit(t, h.Command(w, "BET", "b"+string(rune('1'+i)), amount))
	}

	first, err := h.Wallets.Ledger(h.Ctx, h.Internal, w.ID, application.LedgerCursor{}, 2)
	require.NoError(t, err)
	require.Len(t, first.Entries, 2)
	assert.True(t, first.HasMore)
	assert.True(t, apptest.BRL("100.00").Equal(first.Entries[0].Amount()), "opening comes first")
	assert.True(t, apptest.BRL("1.00").Equal(first.Entries[1].Amount()))

	second, err := h.Wallets.Ledger(h.Ctx, h.Internal, w.ID, first.Next, 2)
	require.NoError(t, err)
	require.Len(t, second.Entries, 2)
	assert.True(t, second.HasMore)

	third, err := h.Wallets.Ledger(h.Ctx, h.Internal, w.ID, second.Next, 2)
	require.NoError(t, err)
	require.Len(t, third.Entries, 1)
	assert.False(t, third.HasMore)
	assert.True(t, apptest.BRL("4.00").Equal(third.Entries[0].Amount()))

	_, err = h.Wallets.Ledger(h.Ctx, h.Internal, w.ID, application.LedgerCursor{}, wallets.MaxLedgerLimit+1)
	assert.ErrorIs(t, err, application.ErrInvalidInput)
	_, err = h.Wallets.Ledger(h.Ctx, h.ProviderA, w.ID, application.LedgerCursor{}, 10)
	assert.ErrorIs(t, err, application.ErrForbidden)
}

func TestReconcile(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "1000.00")
	h.Submit(t, h.Command(w, "BET", "b1", "25.00"))

	rec, err := h.Wallets.Reconcile(h.Ctx, h.Internal, w.ID)
	require.NoError(t, err)
	assert.True(t, rec.Consistent)
	assert.True(t, apptest.BRL("975.00").Equal(rec.Stored))
	assert.True(t, apptest.BRL("975.00").Equal(rec.Calculated))
	assert.True(t, rec.Difference.IsZero())
	assert.Equal(t, 2, rec.CheckedEntries)
	assert.Equal(t, 1, h.Metrics.Reconciled(true))

	h.Store.TamperBalance(w.ID, apptest.BRL("980.00"))

	rec, err = h.Wallets.Reconcile(h.Ctx, h.Internal, w.ID)
	require.NoError(t, err)
	assert.False(t, rec.Consistent)
	assert.True(t, apptest.BRL("5.00").Equal(rec.Difference), "difference is stored minus calculated")
	assert.Equal(t, 1, h.Metrics.Reconciled(false))
	assert.True(t, apptest.BRL("980.00").Equal(h.Balance(t, w.ID)), "reconciliation never changes the balance")

	_, err = h.Wallets.Reconcile(h.Ctx, h.ProviderA, w.ID)
	assert.ErrorIs(t, err, application.ErrForbidden)
}
