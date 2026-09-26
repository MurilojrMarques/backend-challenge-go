package wagering_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

func TestTwoBetsOfEightyOnOneHundred(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")

	results := make([]wagering.Result, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range []string{"bet-1", "bet-2"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			results[i], errs[i] = h.Wagers.Submit(h.Ctx, h.ProviderA, h.Command(w, "BET", id, "80.00"))
		}(i, id)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	statuses := map[wager.Status]int{}
	for _, r := range results {
		statuses[r.Status]++
	}
	assert.Equal(t, 1, statuses[wager.Processed])
	assert.Equal(t, 1, statuses[wager.Rejected])
	assert.True(t, brl("20.00").Equal(h.Balance(t, w.ID)))

	debits := 0
	for _, e := range h.Store.LedgerEntries(w.ID) {
		if e.Direction() == wallet.Debit {
			debits++
		}
	}
	assert.Equal(t, 1, debits)

	for _, id := range []string{"bet-1", "bet-2"} {
		replay := h.Submit(t, h.Command(w, "BET", id, "80.00"))
		assert.True(t, replay.IdempotentReplay)
	}
	assert.True(t, brl("20.00").Equal(h.Balance(t, w.ID)), "resends do not change the outcome")
}

func TestSameBetFiftyTimesInParallel(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "1000.00")
	cmd := h.Command(w, "BET", "bet-1", "25.00")

	results := make([]wagering.Result, 50)
	errs := make([]error, 50)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = h.Wagers.Submit(h.Ctx, h.ProviderA, cmd)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	replays := 0
	for _, r := range results {
		assert.Equal(t, wager.Processed, r.Status)
		assert.Equal(t, results[0].TransactionID, r.TransactionID)
		assert.True(t, brl("975.00").Equal(r.Balance))
		if r.IdempotentReplay {
			replays++
		}
	}
	assert.Equal(t, 49, replays)
	assert.True(t, brl("975.00").Equal(h.Balance(t, w.ID)))
	assert.Len(t, h.Store.LedgerEntries(w.ID), 2)
	assert.Equal(t, 2, h.Store.WagerCount())
}

func TestIndependentWalletsProgressInParallel(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	views := make([]wallets.View, 10)
	for i := range views {
		views[i] = h.OpenWallet(t, "100.00")
	}

	errs := make([]error, len(views))
	var wg sync.WaitGroup
	for i, w := range views {
		wg.Add(1)
		go func(i int, w wallets.View) {
			defer wg.Done()
			for _, id := range []string{"a", "b", "c"} {
				if _, err := h.Wagers.Submit(h.Ctx, h.ProviderA, h.Command(w, "BET", w.ID.String()+id, "10.00")); err != nil {
					errs[i] = err
					return
				}
			}
		}(i, w)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	for _, w := range views {
		assert.True(t, brl("70.00").Equal(h.Balance(t, w.ID)))
		assert.Equal(t, int64(4), h.Store.Wallet(w.ID).Version)
	}
}

func TestRefundAndRollbackRules(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")
	h.Submit(t, h.Command(w, "BET", "bet-1", "30.00"))
	h.Submit(t, h.Command(w, "WIN", "win-1", "50.00"))

	res := h.Submit(t, h.Command(w, "REFUND", "refund-1", "30.00", apptest.WithReference("bet-1")))
	assert.Equal(t, wager.Processed, res.Status)
	assert.True(t, brl("150.00").Equal(h.Balance(t, w.ID)))

	res = h.Submit(t, h.Command(w, "ROLLBACK", "rb-1", "30.00", apptest.WithReference("bet-1")))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.ReferenceAlreadyReversed, res.FailureCode)

	res = h.Submit(t, h.Command(w, "REFUND", "refund-2", "30.00", apptest.WithReference("bet-1")))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.ReferenceAlreadyReversed, res.FailureCode)

	res = h.Submit(t, h.Command(w, "ROLLBACK", "rb-2", "50.00", apptest.WithReference("win-1")))
	assert.Equal(t, wager.Processed, res.Status)
	assert.True(t, brl("100.00").Equal(h.Balance(t, w.ID)))

	res = h.Submit(t, h.Command(w, "ROLLBACK", "rb-3", "30.00", apptest.WithReference("refund-1")))
	assert.Equal(t, wager.Processed, res.Status)
	assert.True(t, brl("70.00").Equal(h.Balance(t, w.ID)))

	res = h.Submit(t, h.Command(w, "REFUND", "refund-3", "31.00", apptest.WithReference("bet-1")))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.ReferenceAmountMismatch, res.FailureCode)

	res = h.Submit(t, h.Command(w, "REFUND", "refund-4", "50.00", apptest.WithReference("win-1")))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.ReferenceKindNotAllowed, res.FailureCode)

	res = h.Submit(t, h.Command(w, "ROLLBACK", "rb-4", "30.00", apptest.WithReference("bet-1"), apptest.WithRound("round-2")))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.ReferenceMismatch, res.FailureCode)

	rec, err := h.Wallets.Reconcile(h.Ctx, h.Internal, w.ID)
	require.NoError(t, err)
	assert.True(t, rec.Consistent)
}

func TestReversalInsufficientFundsHasItsOwnCode(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "0.00")
	h.Submit(t, h.Command(w, "WIN", "win-1", "50.00"))
	h.Submit(t, h.Command(w, "BET", "bet-1", "40.00"))

	res := h.Submit(t, h.Command(w, "ROLLBACK", "rb-1", "50.00", apptest.WithReference("win-1")))
	assert.Equal(t, wager.Rejected, res.Status)
	assert.Equal(t, wager.ReversalInsufficientFunds, res.FailureCode)
	assert.True(t, brl("10.00").Equal(h.Balance(t, w.ID)))
}

func TestReversalArrivesBeforeReference(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")

	res := h.Submit(t, h.Command(w, "REFUND", "refund-1", "30.00", apptest.WithReference("bet-1")))
	assert.Equal(t, wager.PendingReference, res.Status)
	assert.False(t, res.HasBalance)
	assert.Len(t, h.Store.OutboxByType(event.WagerTransactionPendingReference), 1)
	assert.True(t, brl("100.00").Equal(h.Balance(t, w.ID)))

	replay := h.Submit(t, h.Command(w, "REFUND", "refund-1", "30.00", apptest.WithReference("bet-1")))
	assert.True(t, replay.IdempotentReplay)
	assert.Equal(t, wager.PendingReference, replay.Status)

	processed, err := h.Wagers.ResolveDue(h.Ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, processed, "not due yet")

	h.Clock.Advance(apptest.DefaultOptions.ReferenceBackoff.Base)
	processed, err = h.Wagers.ResolveDue(h.Ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Equal(t, wager.PendingReference, h.Store.Wager(res.TransactionID).Status)
	assert.Equal(t, 2, h.Store.Wager(res.TransactionID).ReferenceAttempts)
	assert.Len(t, h.Store.OutboxByType(event.WagerTransactionPendingReference), 1, "retries do not re-emit the pending event")

	h.Submit(t, h.Command(w, "BET", "bet-1", "30.00"))
	assert.True(t, brl("70.00").Equal(h.Balance(t, w.ID)))

	h.Clock.Advance(apptest.DefaultOptions.ReferenceBackoff.Max)
	processed, err = h.Wagers.ResolveDue(h.Ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)

	final := h.Store.Wager(res.TransactionID)
	assert.Equal(t, wager.Processed, final.Status)
	assert.True(t, brl("100.00").Equal(final.BalanceAfter))
	assert.True(t, brl("100.00").Equal(h.Balance(t, w.ID)))

	view, err := h.Wagers.GetByID(h.Ctx, h.ProviderA, res.TransactionID)
	require.NoError(t, err)
	assert.Equal(t, wager.Processed, view.Status)
	assert.NotEqual(t, uuid.Nil, view.ResolvedReferenceID)

	rec, err := h.Wallets.Reconcile(h.Ctx, h.Internal, w.ID)
	require.NoError(t, err)
	assert.True(t, rec.Consistent)
}

func TestPendingReferenceExpires(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t, func(o *wagering.Options) {
		o.ReferencePolicy = wager.ReferencePolicy{MaxAttempts: 3}
	})
	w := h.OpenWallet(t, "100.00")

	res := h.Submit(t, h.Command(w, "ROLLBACK", "rb-1", "30.00", apptest.WithReference("bet-1")))
	assert.Equal(t, wager.PendingReference, res.Status)

	for attempt := 2; attempt <= 3; attempt++ {
		h.Clock.Advance(apptest.DefaultOptions.ReferenceBackoff.Max)
		processed, err := h.Wagers.ResolveDue(h.Ctx, 10)
		require.NoError(t, err)
		require.Equal(t, 1, processed)
		snap := h.Store.Wager(res.TransactionID)
		if attempt < 3 {
			assert.Equal(t, wager.PendingReference, snap.Status)
			assert.Equal(t, attempt, snap.ReferenceAttempts)
		} else {
			assert.Equal(t, wager.Rejected, snap.Status)
			assert.Equal(t, wager.ReferenceNotFound, snap.FailureCode)
		}
	}
	assert.Len(t, h.Store.OutboxByType(event.WagerTransactionRejected), 1)
	assert.Equal(t, 1, h.Metrics.Concluded(wager.Rollback, wager.Rejected, wager.ReferenceNotFound))

	h.Clock.Advance(apptest.DefaultOptions.ReferenceBackoff.Max)
	processed, err := h.Wagers.ResolveDue(h.Ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, processed, "terminal transactions are never picked up again")

	late := h.Submit(t, h.Command(w, "BET", "bet-1", "30.00"))
	assert.Equal(t, wager.Processed, late.Status)
	assert.True(t, brl("70.00").Equal(h.Balance(t, w.ID)), "late reference does not revive the expired rollback")
}

func TestPendingReferenceRejectedWhenReferenceFailed(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "10.00")

	pending := h.Submit(t, h.Command(w, "REFUND", "refund-1", "50.00", apptest.WithReference("bet-1")))
	assert.Equal(t, wager.PendingReference, pending.Status)

	rejectedBet := h.Submit(t, h.Command(w, "BET", "bet-1", "50.00"))
	assert.Equal(t, wager.Rejected, rejectedBet.Status)

	h.Clock.Advance(apptest.DefaultOptions.ReferenceBackoff.Max)
	processed, err := h.Wagers.ResolveDue(h.Ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)

	snap := h.Store.Wager(pending.TransactionID)
	assert.Equal(t, wager.Rejected, snap.Status)
	assert.Equal(t, wager.ReferenceNotProcessed, snap.FailureCode)
}

func TestConsumeSharesIdempotencyWithHTTP(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")
	cmd := h.Command(w, "BET", "bet-1", "25.00")

	msg := wagering.InboundMessage{ConsumerName: "wager-transactions", MessageID: "msg-1", PayloadHash: "hash-1", Command: cmd}
	first, err := h.Wagers.Consume(h.Ctx, msg)
	require.NoError(t, err)
	assert.Equal(t, wager.Processed, first.Status)
	assert.False(t, first.IdempotentReplay)

	redelivery, err := h.Wagers.Consume(h.Ctx, msg)
	require.NoError(t, err)
	assert.True(t, redelivery.IdempotentReplay)
	assert.Equal(t, first.TransactionID, redelivery.TransactionID)
	assert.Equal(t, 1, h.Metrics.Replays("sqs"))

	_, err = h.Wagers.Consume(h.Ctx, wagering.InboundMessage{ConsumerName: "wager-transactions", MessageID: "msg-1", PayloadHash: "tampered", Command: cmd})
	assert.ErrorIs(t, err, application.ErrConflict, "same messageId with a different payload is poison")

	viaHTTP := h.Submit(t, cmd)
	assert.True(t, viaHTTP.IdempotentReplay, "HTTP after SQS is a replay")

	other := h.Command(w, "BET", "bet-2", "5.00")
	h.Submit(t, other)
	viaSQS, err := h.Wagers.Consume(h.Ctx, wagering.InboundMessage{ConsumerName: "wager-transactions", MessageID: "msg-2", PayloadHash: "hash-2", Command: other})
	require.NoError(t, err)
	assert.True(t, viaSQS.IdempotentReplay, "SQS after HTTP is a replay")

	assert.True(t, brl("70.00").Equal(h.Balance(t, w.ID)))
	assert.Len(t, h.Store.LedgerEntries(w.ID), 3)

	inbox, ok := h.Store.Inbox("wager-transactions", "msg-2")
	require.True(t, ok)
	assert.True(t, inbox.Completed())
	assert.Equal(t, viaSQS.TransactionID, inbox.TransactionID)

	_, err = h.Wagers.Consume(h.Ctx, wagering.InboundMessage{MessageID: "x", PayloadHash: "y", Command: cmd})
	assert.ErrorIs(t, err, application.ErrInvalidInput)
	bad := cmd
	bad.Money.Amount = "nope"
	_, err = h.Wagers.Consume(h.Ctx, wagering.InboundMessage{ConsumerName: "c", MessageID: "m", PayloadHash: "h", Command: bad})
	assert.ErrorIs(t, err, application.ErrInvalidInput)
}

func TestConsumeBusinessRejectionIsTerminal(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "10.00")

	res, err := h.Wagers.Consume(h.Ctx, wagering.InboundMessage{
		ConsumerName: "c", MessageID: "m", PayloadHash: "h",
		Command: h.Command(w, "BET", "bet-1", "50.00"),
	})
	require.NoError(t, err, "a business rejection is a successful, terminal handling")
	assert.Equal(t, wager.Rejected, res.Status)
	inbox, ok := h.Store.Inbox("c", "m")
	require.True(t, ok)
	assert.True(t, inbox.Completed())
}

func TestPendingReferenceFailsPermanentlyOnIntegrityErrors(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")

	pending := h.Submit(t, h.Command(w, "ROLLBACK", "rb-1", "30.00", apptest.WithReference("bet-1")))
	require.Equal(t, wager.PendingReference, pending.Status)

	h.Clock.Advance(apptest.DefaultOptions.ReferenceBackoff.Max)
	h.Store.FailCommit(application.ErrIntegrity)
	processed, err := h.Wagers.ResolveDue(h.Ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)

	snap := h.Store.Wager(pending.TransactionID)
	assert.Equal(t, wager.Failed, snap.Status)
	assert.Equal(t, wager.PermanentFailure, snap.FailureCode)
	assert.Equal(t, 1, h.Metrics.Concluded(wager.Rollback, wager.Failed, wager.PermanentFailure))
	assert.True(t, brl("100.00").Equal(h.Balance(t, w.ID)))
}

func TestTimestampsFollowLockOrderNotTheClock(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	w := h.OpenWallet(t, "100.00")

	first := h.Submit(t, h.Command(w, "BET", "bet-1", "10.00"))
	h.Clock.Advance(-time.Hour)
	second := h.Submit(t, h.Command(w, "BET", "bet-2", "10.00"))

	entries := h.Store.LedgerEntries(w.ID)
	require.Len(t, entries, 3)
	assert.True(t, entries[2].CreatedAt().After(entries[1].CreatedAt()), "ledger order follows the wallet lock even when the clock goes backwards")

	var latest time.Time
	for _, rec := range h.Store.OutboxByType(event.WalletBalanceChanged) {
		if rec.OccurredAt.After(latest) {
			latest = rec.OccurredAt
		}
	}
	assert.Equal(t, entries[2].CreatedAt(), latest, "the newest balance event carries the newest ledger timestamp")
	assert.True(t, h.Store.Wager(second.TransactionID).CreatedAt.After(h.Store.Wager(first.TransactionID).CreatedAt))

	_, err := h.Wallets.Get(h.Ctx, h.Internal, w.ID)
	require.NoError(t, err, "the wallet still rehydrates")
}
