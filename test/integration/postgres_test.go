//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	m, err := pg.Migrator()
	require.NoError(t, err)
	defer m.Close()

	require.NoError(t, m.Down())
	admin := rawPool(t, pg.AdminURL())
	var tables int
	require.NoError(t, admin.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'").Scan(&tables))
	assert.Equal(t, 0, tables, "down leaves no application table behind")

	require.NoError(t, m.Up())
	version, dirty, err := m.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(5), version)
	assert.False(t, dirty)
}

func pgCode(t *testing.T, err error) string {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	return pgErr.Code
}

func (s services) do(t *testing.T, fn func(r application.Repos) error) error {
	t.Helper()
	return s.uow.Do(context.Background(), application.TxOptions{}, func(_ context.Context, r application.Repos) error {
		return fn(r)
	})
}

func TestLeastPrivilege(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)
	w := s.open(t, "100.00")

	app := rawPool(t, pg.AppURL())
	_, err := app.Exec(ctx, "DELETE FROM wallets WHERE id = $1", w.ID)
	assert.Equal(t, "42501", pgCode(t, err), "wallet_app cannot delete")
	_, err = app.Exec(ctx, "UPDATE wallet_ledger_entries SET created_at = now() WHERE wallet_id = $1", w.ID)
	assert.Equal(t, "42501", pgCode(t, err), "wallet_app cannot update the ledger")
	_, err = app.Exec(ctx, "UPDATE schema_migrations SET dirty = true")
	assert.Equal(t, "42501", pgCode(t, err), "wallet_app cannot touch the migration state")

	migrator := rawPool(t, pg.MigratorURL())
	_, err = migrator.Exec(ctx, "UPDATE wallet_ledger_entries SET created_at = now() WHERE wallet_id = $1", w.ID)
	assert.Equal(t, "23001", pgCode(t, err), "even the owner is stopped by the immutability trigger")
	_, err = migrator.Exec(ctx, "DELETE FROM wallet_ledger_entries WHERE wallet_id = $1", w.ID)
	assert.Equal(t, "23001", pgCode(t, err))
	_, err = migrator.Exec(ctx, "TRUNCATE wallet_ledger_entries")
	assert.Equal(t, "23001", pgCode(t, err))
}

func TestWalletRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)
	now := application.SystemClock{}.Now()

	w, _, err := wallet.Open(wallet.OpenParams{ID: newID(), PlayerID: newID(), InitialBalance: brl("0.00"), Now: now})
	require.NoError(t, err)
	require.NoError(t, s.do(t, func(r application.Repos) error { return r.Wallets.Create(ctx, w) }))

	var loaded *wallet.Wallet
	require.NoError(t, s.do(t, func(r application.Repos) error {
		var err error
		loaded, err = r.Wallets.Get(ctx, w.ID())
		return err
	}))
	assert.Equal(t, w.ID(), loaded.ID())
	assert.Equal(t, w.PlayerID(), loaded.PlayerID())
	assert.Equal(t, int64(1), loaded.Version())
	assert.True(t, loaded.CreatedAt().Equal(now), "timestamps survive the round trip at microsecond precision")

	twin, _, err := wallet.Open(wallet.OpenParams{ID: newID(), PlayerID: w.PlayerID(), InitialBalance: brl("0.00"), Now: now})
	require.NoError(t, err)
	err = s.do(t, func(r application.Repos) error { return r.Wallets.Create(ctx, twin) })
	assert.ErrorIs(t, err, application.ErrWalletExists)
	assert.ErrorIs(t, err, application.ErrConflict)

	_, err = loaded.Apply(wallet.Movement{EntryID: newID(), TransactionID: newID(), Direction: wallet.Credit, Amount: brl("5.00"), Now: now.Add(time.Second)})
	require.NoError(t, err)
	err = s.do(t, func(r application.Repos) error { return r.Wallets.Save(ctx, loaded, 99) })
	assert.ErrorIs(t, err, application.ErrConcurrentModification, "a stale version never overwrites the row")
	require.NoError(t, s.do(t, func(r application.Repos) error { return r.Wallets.Save(ctx, loaded, 1) }))

	require.NoError(t, s.do(t, func(r application.Repos) error {
		var err error
		loaded, err = r.Wallets.GetForUpdate(ctx, w.ID())
		return err
	}))
	assert.Equal(t, int64(2), loaded.Version())
	assert.True(t, brl("5.00").Equal(loaded.Balance()))

	err = s.do(t, func(r application.Repos) error {
		_, err := r.Wallets.Get(ctx, newID())
		return err
	})
	assert.ErrorIs(t, err, application.ErrNotFound)
}

func TestWagerRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)
	w := s.open(t, "50.00")
	now := application.SystemClock{}.Now()

	external := wager.External{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "rb-" + newID().String(),
		IdempotencyKey:                 "provider-a:rb-" + newID().String(),
		PayloadHash:                    "hash-1",
		RoundID:                        "round-1",
		GameID:                         "fortune-chimp",
		ReferenceExternalTransactionID: "bet-" + newID().String(),
	}
	rollback, err := wager.NewExternal(wager.ExternalParams{ID: newID(), WalletID: w.ID, PlayerID: w.PlayerID, Kind: wager.Rollback, Amount: brl("10.00"), External: external, Now: now})
	require.NoError(t, err)
	require.NoError(t, rollback.AwaitReference(now))
	require.NoError(t, s.do(t, func(r application.Repos) error { return r.Wagers.Insert(ctx, rollback, now.Add(time.Second)) }))

	var loaded *wager.Transaction
	require.NoError(t, s.do(t, func(r application.Repos) error {
		var err error
		loaded, err = r.Wagers.Get(ctx, rollback.ID())
		return err
	}))
	assert.Equal(t, wager.PendingReference, loaded.Status())
	assert.Equal(t, 1, loaded.ReferenceAttempts())
	ext, ok := loaded.External()
	require.True(t, ok)
	assert.Equal(t, external, ext)
	assert.True(t, loaded.CreatedAt().Equal(rollback.CreatedAt()))

	require.NoError(t, s.do(t, func(r application.Repos) error {
		byKey, err := r.Wagers.GetByIdempotencyKey(ctx, "provider-a", external.IdempotencyKey)
		if err != nil {
			return err
		}
		assert.Equal(t, rollback.ID(), byKey.ID())
		byExternal, err := r.Wagers.GetByExternalID(ctx, "provider-a", external.ExternalTransactionID)
		if err != nil {
			return err
		}
		assert.Equal(t, rollback.ID(), byExternal.ID())
		_, err = r.Wagers.GetByExternalID(ctx, "provider-b", external.ExternalTransactionID)
		assert.ErrorIs(t, err, application.ErrNotFound, "lookups are scoped by provider")
		return nil
	}))

	reused := external
	reused.ExternalTransactionID = "other-" + newID().String()
	reused.ReferenceExternalTransactionID = ""
	duplicate, err := wager.NewExternal(wager.ExternalParams{ID: newID(), WalletID: w.ID, PlayerID: w.PlayerID, Kind: wager.Bet, Amount: brl("1.00"), External: reused, Now: now})
	require.NoError(t, err)
	err = s.do(t, func(r application.Repos) error { return r.Wagers.Insert(ctx, duplicate, time.Time{}) })
	var conflict *application.ConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "wager_transactions_idempotency_key", conflict.Constraint)

	require.NoError(t, s.do(t, func(r application.Repos) error {
		due, err := r.Wagers.ListDuePendingReferences(ctx, now.Add(time.Hour), 500)
		if err != nil {
			return err
		}
		assert.Contains(t, ids(due), rollback.ID())
		notYet, err := r.Wagers.ListDuePendingReferences(ctx, now, 500)
		if err != nil {
			return err
		}
		assert.NotContains(t, ids(notYet), rollback.ID(), "scheduled one second later")
		return nil
	}))

	betExternal := wager.External{ProviderID: "provider-a", ExternalTransactionID: external.ReferenceExternalTransactionID, IdempotencyKey: "provider-a:" + external.ReferenceExternalTransactionID, PayloadHash: "hash-2", RoundID: "round-1", GameID: "fortune-chimp"}
	bet, err := wager.NewExternal(wager.ExternalParams{ID: newID(), WalletID: w.ID, PlayerID: w.PlayerID, Kind: wager.Bet, Amount: brl("10.00"), External: betExternal, Now: now})
	require.NoError(t, err)
	require.NoError(t, bet.MarkProcessed(brl("40.00"), now))
	require.NoError(t, s.do(t, func(r application.Repos) error { return r.Wagers.Insert(ctx, bet, time.Time{}) }))

	later := now.Add(2 * time.Second)
	require.NoError(t, loaded.ResolveReference(bet.ID(), later))
	require.NoError(t, loaded.MarkProcessed(brl("50.00"), later))
	require.NoError(t, s.do(t, func(r application.Repos) error { return r.Wagers.Update(ctx, loaded, time.Time{}) }))

	require.NoError(t, s.do(t, func(r application.Repos) error {
		again, err := r.Wagers.Get(ctx, rollback.ID())
		if err != nil {
			return err
		}
		assert.Equal(t, wager.Processed, again.Status())
		resolved, ok := again.ResolvedReferenceID()
		assert.True(t, ok)
		assert.Equal(t, bet.ID(), resolved)
		reversed, err := r.Wagers.HasSuccessfulReversal(ctx, bet.ID())
		if err != nil {
			return err
		}
		assert.True(t, reversed)
		none, err := r.Wagers.HasSuccessfulReversal(ctx, newID())
		if err != nil {
			return err
		}
		assert.False(t, none)
		return nil
	}))
}

func ids(txs []*wager.Transaction) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(txs))
	for _, tx := range txs {
		out = append(out, tx.ID())
	}
	return out
}

func TestLedgerRepositoryAndReconciliation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)
	w := s.open(t, "100.00")
	for _, ext := range []string{"a", "b", "c"} {
		res, err := s.wagers.Submit(ctx, provider, command(w, "BET", ext+"-"+newID().String(), "10.00"))
		require.NoError(t, err)
		require.Equal(t, wager.Processed, res.Status)
	}

	var first, second []wallet.LedgerEntry
	var totals application.LedgerTotals
	require.NoError(t, s.do(t, func(r application.Repos) error {
		var err error
		if first, err = r.Ledger.List(ctx, w.ID, application.LedgerCursor{}, 3); err != nil {
			return err
		}
		if second, err = r.Ledger.List(ctx, w.ID, application.LedgerCursor{CreatedAt: first[2].CreatedAt(), ID: first[2].ID()}, 3); err != nil {
			return err
		}
		totals, err = r.Ledger.Totals(ctx, w.ID)
		return err
	}))
	require.Len(t, first, 3)
	require.Len(t, second, 1, "the cursor continues exactly where the page ended")
	all := append(first, second...)
	seen := map[uuid.UUID]bool{}
	for i, e := range all {
		assert.False(t, seen[e.ID()], "no entry is paged twice")
		seen[e.ID()] = true
		if i > 0 {
			assert.True(t, all[i-1].BalanceAfter().Equal(e.BalanceBefore()), "entries chain balances in order")
		}
	}
	assert.Equal(t, wallet.Credit, all[0].Direction())
	assert.Equal(t, application.LedgerTotals{CreditUnits: 10000, DebitUnits: 3000, Entries: 4}, totals)

	rec, err := s.wallets.Reconcile(ctx, internal, w.ID)
	require.NoError(t, err)
	assert.True(t, rec.Consistent)
	assert.True(t, brl("70.00").Equal(rec.Calculated))
	assert.Equal(t, 4, rec.CheckedEntries)
}

func TestInboxRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)
	w := s.open(t, "10.00")
	res, err := s.wagers.Submit(ctx, provider, command(w, "BET", "bet-"+newID().String(), "1.00"))
	require.NoError(t, err)
	now := application.SystemClock{}.Now()

	rec := application.InboxRecord{ConsumerName: "wager-transactions", MessageID: "msg-" + newID().String(), PayloadHash: "hash-1", ReceivedAt: now}
	require.NoError(t, s.do(t, func(r application.Repos) error { return r.Inbox.Insert(ctx, rec) }))
	err = s.do(t, func(r application.Repos) error { return r.Inbox.Insert(ctx, rec) })
	assert.ErrorIs(t, err, application.ErrConflict, "the primary key is the durable identity of the message")

	require.NoError(t, s.do(t, func(r application.Repos) error {
		got, err := r.Inbox.Get(ctx, rec.ConsumerName, rec.MessageID)
		if err != nil {
			return err
		}
		assert.Equal(t, rec.PayloadHash, got.PayloadHash)
		assert.False(t, got.Completed())
		return r.Inbox.Complete(ctx, rec.ConsumerName, rec.MessageID, res.TransactionID, now.Add(time.Second))
	}))
	require.NoError(t, s.do(t, func(r application.Repos) error {
		got, err := r.Inbox.Get(ctx, rec.ConsumerName, rec.MessageID)
		if err != nil {
			return err
		}
		assert.True(t, got.Completed())
		assert.Equal(t, res.TransactionID, got.TransactionID)
		_, err = r.Inbox.Get(ctx, rec.ConsumerName, "unknown")
		assert.ErrorIs(t, err, application.ErrNotFound)
		return nil
	}))
}

func TestOutboxRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)
	w := s.open(t, "100.00")
	bet, err := s.wagers.Submit(ctx, provider, command(w, "BET", "bet-"+newID().String(), "10.00"))
	require.NoError(t, err)

	var opening []wallet.LedgerEntry
	require.NoError(t, s.do(t, func(r application.Repos) error {
		var err error
		opening, err = r.Ledger.List(ctx, w.ID, application.LedgerCursor{}, 1)
		return err
	}))
	require.Len(t, opening, 1)
	mine := map[uuid.UUID]bool{w.ID: true, opening[0].TransactionID(): true, bet.TransactionID: true}
	now := application.SystemClock{}.Now()

	claim := func(publisher string) []application.OutboxRecord {
		var out []application.OutboxRecord
		require.NoError(t, s.do(t, func(r application.Repos) error {
			var err error
			out, err = r.Outbox.Claim(ctx, publisher, application.SystemClock{}.Now(), 30*time.Second, 500)
			return err
		}))
		return out
	}

	var claimed [2][]application.OutboxRecord
	var wg sync.WaitGroup
	for i, publisher := range []string{"relay-a", "relay-b"} {
		wg.Add(1)
		go func(i int, publisher string) {
			defer wg.Done()
			claimed[i] = claim(publisher)
		}(i, publisher)
	}
	wg.Wait()

	seen := map[uuid.UUID]string{}
	var ours []application.OutboxRecord
	for i, recs := range claimed {
		for _, rec := range recs {
			owner, dup := seen[rec.EventID]
			assert.False(t, dup, "event %s claimed by both %s and %s", rec.EventID, owner, rec.LockedBy)
			seen[rec.EventID] = []string{"relay-a", "relay-b"}[i]
			if mine[rec.AggregateID] {
				ours = append(ours, rec)
			}
		}
	}
	require.Len(t, ours, 3, "opening processed, first balance change and bet processed; the second balance change waits")
	var firstChange application.OutboxRecord
	for _, rec := range ours {
		if rec.EventType == event.WalletBalanceChanged {
			require.Equal(t, uuid.Nil, firstChange.EventID, "only one balance change per wallet is claimable at a time")
			firstChange = rec
		}
	}
	require.NotEqual(t, uuid.Nil, firstChange.EventID)

	err = s.do(t, func(r application.Repos) error {
		return r.Outbox.MarkPublished(ctx, firstChange.EventID, "intruder", now)
	})
	assert.ErrorIs(t, err, application.ErrConcurrentModification, "only the lease holder can mark an event")
	require.NoError(t, s.do(t, func(r application.Repos) error {
		return r.Outbox.MarkPublished(ctx, firstChange.EventID, seen[firstChange.EventID], now)
	}))

	var secondChange application.OutboxRecord
	for _, rec := range claim("relay-c") {
		if mine[rec.AggregateID] {
			require.Equal(t, event.WalletBalanceChanged, rec.EventType)
			secondChange = rec
		}
	}
	require.NotEqual(t, uuid.Nil, secondChange.EventID, "the held-back event becomes claimable once its predecessor is published")
	assert.True(t, secondChange.OccurredAt.After(firstChange.OccurredAt))

	require.NoError(t, s.do(t, func(r application.Repos) error {
		return r.Outbox.Release(ctx, secondChange.EventID, "relay-c", now.Add(time.Hour), "publisher down")
	}))
	for _, rec := range claim("relay-d") {
		assert.False(t, mine[rec.AggregateID], "a released event is not due before its next attempt")
	}
}

func TestConcurrentSubmissionsOnPostgres(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)

	w := s.open(t, "1000.00")
	cmd := command(w, "BET", "bet-"+newID().String(), "25.00")
	results := make([]wagering.Result, 50)
	errs := make([]error, 50)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.wagers.Submit(ctx, provider, cmd)
		}(i)
	}
	wg.Wait()
	replays := 0
	for i, res := range results {
		require.NoError(t, errs[i])
		assert.Equal(t, wager.Processed, res.Status)
		assert.Equal(t, results[0].TransactionID, res.TransactionID)
		assert.True(t, brl("975.00").Equal(res.Balance))
		if res.IdempotentReplay {
			replays++
		}
	}
	assert.Equal(t, 49, replays)
	assert.True(t, brl("975.00").Equal(s.balance(t, w.ID)))
	require.NoError(t, s.do(t, func(r application.Repos) error {
		entries, err := r.Ledger.List(ctx, w.ID, application.LedgerCursor{}, 10)
		assert.Len(t, entries, 2)
		return err
	}))

	w2 := s.open(t, "100.00")
	outcomes := make([]wagering.Result, 2)
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i], errs[i] = s.wagers.Submit(ctx, provider, command(w2, "BET", "bet-"+newID().String(), "80.00"))
		}(i)
	}
	wg.Wait()
	statuses := map[wager.Status]int{}
	for i := range outcomes {
		require.NoError(t, errs[i])
		statuses[outcomes[i].Status]++
	}
	assert.Equal(t, map[wager.Status]int{wager.Processed: 1, wager.Rejected: 1}, statuses)
	assert.True(t, brl("20.00").Equal(s.balance(t, w2.ID)))

	w3 := s.open(t, "100.00")
	msg := wagering.InboundMessage{ConsumerName: "wager-transactions", MessageID: "msg-" + newID().String(), PayloadHash: "hash", Command: command(w3, "BET", "bet-"+newID().String(), "30.00")}
	consumed := make([]wagering.Result, 2)
	for i := range consumed {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			consumed[i], errs[i] = s.wagers.Consume(ctx, msg)
		}(i)
	}
	wg.Wait()
	processed, inFlightOrReplay := 0, 0
	for i := range consumed {
		switch {
		case errs[i] == nil && !consumed[i].IdempotentReplay:
			processed++
		case errs[i] == nil, errors.Is(errs[i], application.ErrMessageInFlight):
			inFlightOrReplay++
		default:
			t.Fatalf("unexpected error: %v", errs[i])
		}
	}
	assert.Equal(t, 1, processed, "the inbox lets exactly one consumer apply the message")
	assert.Equal(t, 1, inFlightOrReplay)
	assert.True(t, brl("70.00").Equal(s.balance(t, w3.ID)))
}
