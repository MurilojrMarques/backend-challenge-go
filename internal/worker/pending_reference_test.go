package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

func TestPendingResolverResolvesDueReferences(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	p := NewPendingResolver(h.Wagers, PendingOptions{PollInterval: time.Millisecond, BatchSize: 10}, discard)
	w := h.OpenWallet(t, "100.00")

	pending := h.Submit(t, h.Command(w, "ROLLBACK", "rb-1", "30.00", apptest.WithReference("bet-1")))
	require.Equal(t, wager.PendingReference, pending.Status)

	busy, err := p.tick(context.Background())
	require.NoError(t, err)
	assert.False(t, busy, "nothing due yet")

	h.Submit(t, h.Command(w, "BET", "bet-1", "30.00"))
	h.Clock.Advance(apptest.DefaultOptions.ReferenceBackoff.Max)

	busy, err = p.tick(context.Background())
	require.NoError(t, err)
	assert.True(t, busy)
	assert.Equal(t, wager.Processed, h.Store.Wager(pending.TransactionID).Status)
	assert.True(t, apptest.BRL("100.00").Equal(h.Balance(t, w.ID)), "rollback credited the bet back")
}

func TestWorkersStartAndStop(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	q := newFakeQueue()

	consumer := newConsumer(t, h, q)
	relay := newRelay(h, &fakePublisher{}, "relay", nil)
	pending := NewPendingResolver(h.Wagers, PendingOptions{PollInterval: time.Millisecond, BatchSize: 10}, discard)

	ctx := context.Background()
	for _, w := range []interface {
		Start(context.Context) error
		Stop(context.Context) error
	}{consumer, relay, pending} {
		require.NoError(t, w.Start(ctx))
	}
	time.Sleep(20 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	require.NoError(t, consumer.Stop(stopCtx))
	require.NoError(t, relay.Stop(stopCtx))
	require.NoError(t, pending.Stop(stopCtx))

	assert.NotEmpty(t, PublisherID())
	assert.NotEqual(t, PublisherID(), PublisherID(), "publisher ids are unique per process instance")
}
