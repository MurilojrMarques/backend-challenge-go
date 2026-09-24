package apptest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

var (
	now = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	brl = apptest.BRL
)

func newWallet(t *testing.T, playerID uuid.UUID, amount string) *wallet.Wallet {
	t.Helper()
	w, _, err := wallet.Open(wallet.OpenParams{
		ID: uuid.Must(uuid.NewV7()), PlayerID: playerID, InitialBalance: brl(amount),
		OpeningTxID: uuid.Must(uuid.NewV7()), OpeningEntryID: uuid.Must(uuid.NewV7()), Now: now,
	})
	require.NoError(t, err)
	return w
}

func newBet(t *testing.T, w *wallet.Wallet, externalID, key string) *wager.Transaction {
	t.Helper()
	tx, err := wager.NewExternal(wager.ExternalParams{
		ID: uuid.Must(uuid.NewV7()), WalletID: w.ID(), PlayerID: w.PlayerID(), Kind: wager.Bet, Amount: brl("1.00"),
		External: wager.External{ProviderID: "p", ExternalTransactionID: externalID, IdempotencyKey: key, PayloadHash: "h", RoundID: "r", GameID: "g"},
		Now:      now,
	})
	require.NoError(t, err)
	return tx
}

func inTx(store *apptest.MemStore, fn func(r application.Repos) error) error {
	return store.Do(context.Background(), application.TxOptions{}, func(_ context.Context, r application.Repos) error { return fn(r) })
}

func TestMemStoreEnforcesWalletConstraints(t *testing.T) {
	t.Parallel()
	store := apptest.NewMemStore()
	player := uuid.Must(uuid.NewV7())
	w := newWallet(t, player, "10.00")
	ctx := context.Background()

	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Wallets.Create(ctx, w) }))

	err := inTx(store, func(r application.Repos) error { return r.Wallets.Create(ctx, newWallet(t, player, "5.00")) })
	assert.ErrorIs(t, err, application.ErrConflict, "same player and currency")

	err = inTx(store, func(r application.Repos) error { return r.Wallets.Save(ctx, w, 99) })
	assert.ErrorIs(t, err, application.ErrConcurrentModification)

	err = inTx(store, func(r application.Repos) error {
		_, err := r.Wallets.Get(ctx, uuid.Must(uuid.NewV7()))
		return err
	})
	assert.ErrorIs(t, err, application.ErrNotFound)
}

func TestMemStoreEnforcesWagerConstraints(t *testing.T) {
	t.Parallel()
	store := apptest.NewMemStore()
	w := newWallet(t, uuid.Must(uuid.NewV7()), "10.00")
	first := newBet(t, w, "e1", "k1")
	ctx := context.Background()

	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, first, time.Time{}) }))

	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, newBet(t, w, "e2", "k1"), time.Time{}) }),
		application.ErrConflict, "duplicate idempotency key")
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, newBet(t, w, "e1", "k2"), time.Time{}) }),
		application.ErrConflict, "duplicate provider/external id")

	foreign, err := wager.NewExternal(wager.ExternalParams{
		ID: uuid.Must(uuid.NewV7()), WalletID: w.ID(), PlayerID: w.PlayerID(), Kind: wager.Bet, Amount: brl("1.00"),
		External: wager.External{ProviderID: "q", ExternalTransactionID: "e1", IdempotencyKey: "k1", PayloadHash: "h", RoundID: "r", GameID: "g"},
		Now:      now,
	})
	require.NoError(t, err)
	assert.NoError(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, foreign, time.Time{}) }),
		"same key and external id under another provider is a different operation")
	assert.NoError(t, inTx(store, func(r application.Repos) error {
		_, err := r.Wagers.GetByIdempotencyKey(ctx, "q", "k1")
		return err
	}))
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error {
		_, err := r.Wagers.GetByIdempotencyKey(ctx, "z", "k1")
		return err
	}), application.ErrNotFound, "lookup is scoped by provider")
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, newBet(t, w, "e3", "k3"), now) }),
		application.ErrIntegrity, "schedule without PENDING_REFERENCE")

	opening := func() *wager.Transaction {
		tx, err := wager.NewOpening(wager.OpeningParams{ID: uuid.Must(uuid.NewV7()), WalletID: w.ID(), PlayerID: w.PlayerID(), Amount: brl("1.00"), Now: now})
		require.NoError(t, err)
		return tx
	}
	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, opening(), time.Time{}) }))
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, opening(), time.Time{}) }),
		application.ErrConflict, "second OPENING for the same wallet")

	require.NoError(t, first.MarkProcessed(brl("9.00"), now))
	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Wagers.Update(ctx, first, time.Time{}) }))
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Wagers.Update(ctx, first, time.Time{}) }),
		application.ErrConcurrentModification, "terminal rows are not updatable")
}

func TestMemStoreEnforcesSingleReversal(t *testing.T) {
	t.Parallel()
	store := apptest.NewMemStore()
	w := newWallet(t, uuid.Must(uuid.NewV7()), "10.00")
	ctx := context.Background()
	bet := newBet(t, w, "bet", "k-bet")
	require.NoError(t, bet.MarkProcessed(brl("9.00"), now))
	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, bet, time.Time{}) }))

	reversal := func(externalID string) *wager.Transaction {
		tx, err := wager.NewExternal(wager.ExternalParams{
			ID: uuid.Must(uuid.NewV7()), WalletID: w.ID(), PlayerID: w.PlayerID(), Kind: wager.Refund, Amount: brl("1.00"),
			External: wager.External{ProviderID: "p", ExternalTransactionID: externalID, IdempotencyKey: "k-" + externalID, PayloadHash: "h", RoundID: "r", GameID: "g", ReferenceExternalTransactionID: "bet"},
			Now:      now,
		})
		require.NoError(t, err)
		require.NoError(t, tx.ResolveReference(bet.ID(), now))
		require.NoError(t, tx.MarkProcessed(brl("10.00"), now))
		return tx
	}
	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, reversal("r1"), time.Time{}) }))
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Wagers.Insert(ctx, reversal("r2"), time.Time{}) }),
		application.ErrConflict)
}

func TestMemStoreEnforcesLedgerAndInboxUniqueness(t *testing.T) {
	t.Parallel()
	store := apptest.NewMemStore()
	ctx := context.Background()
	_, entry, err := wallet.Open(wallet.OpenParams{
		ID: uuid.Must(uuid.NewV7()), PlayerID: uuid.Must(uuid.NewV7()), InitialBalance: brl("1.00"),
		OpeningTxID: uuid.Must(uuid.NewV7()), OpeningEntryID: uuid.Must(uuid.NewV7()), Now: now,
	})
	require.NoError(t, err)

	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Ledger.Append(ctx, *entry) }))
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Ledger.Append(ctx, *entry) }), application.ErrConflict)

	rec := application.InboxRecord{ConsumerName: "c", MessageID: "m", PayloadHash: "h", ReceivedAt: now}
	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Inbox.Insert(ctx, rec) }))
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Inbox.Insert(ctx, rec) }), application.ErrConflict)
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Inbox.Insert(ctx, application.InboxRecord{}) }), application.ErrInvalidInput)
	require.NoError(t, inTx(store, func(r application.Repos) error { return r.Inbox.Complete(ctx, "c", "m", uuid.Nil, now) }))
	assert.ErrorIs(t, inTx(store, func(r application.Repos) error { return r.Inbox.Complete(ctx, "c", "m", uuid.Nil, now) }), application.ErrConcurrentModification)
}

func TestMemStoreRollsBackOnError(t *testing.T) {
	t.Parallel()
	store := apptest.NewMemStore()
	w := newWallet(t, uuid.Must(uuid.NewV7()), "10.00")
	boom := errors.New("boom")

	err := inTx(store, func(r application.Repos) error {
		require.NoError(t, r.Wallets.Create(context.Background(), w))
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 0, store.WalletCount(), "nothing is visible after a rolled back transaction")
}
