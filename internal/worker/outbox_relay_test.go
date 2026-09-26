package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
)

type fakePublisher struct {
	mu        sync.Mutex
	published []application.OutboxRecord
	failUntil int
	calls     int
}

func (p *fakePublisher) Publish(_ context.Context, rec application.OutboxRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.failUntil {
		return errors.New("sqs down")
	}
	p.published = append(p.published, rec)
	return nil
}

func (p *fakePublisher) ids() []uuid.UUID {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]uuid.UUID, 0, len(p.published))
	for _, r := range p.published {
		out = append(out, r.EventID)
	}
	return out
}

func newRelay(h *apptest.Harness, pub application.EventPublisher, id string, fault *Fault) *OutboxRelay {
	return NewOutboxRelay(h.Store, pub, h.Clock, OutboxOptions{
		PublisherID:  id,
		PollInterval: time.Millisecond,
		BatchSize:    10,
		Lease:        30 * time.Second,
		RetryBackoff: wagering.Backoff{Base: time.Second, Max: time.Minute},
	}, h.Metrics, discard, fault)
}

func TestOutboxRelayPublishesInOrderPerAggregate(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	pub := &fakePublisher{}
	relay := newRelay(h, pub, "relay-1", nil)

	w := h.OpenWallet(t, "100.00")
	h.Submit(t, h.Command(w, "BET", "b1", "10.00"))
	h.Submit(t, h.Command(w, "BET", "b2", "10.00"))
	require.Equal(t, 6, h.Store.OutboxCount())

	for i := 0; i < 10; i++ {
		busy, err := relay.tick(context.Background())
		require.NoError(t, err)
		if !busy {
			break
		}
	}
	require.Len(t, pub.published, 6, "every event is published exactly once")
	assert.Equal(t, 6, h.Metrics.Count("outbox/published/"+string(event.WagerTransactionProcessed))+h.Metrics.Count("outbox/published/"+string(event.WalletBalanceChanged)))

	var lastForWallet time.Time
	for _, rec := range pub.published {
		if rec.AggregateID == w.ID {
			assert.False(t, rec.OccurredAt.Before(lastForWallet), "wallet events are published in occurrence order")
			lastForWallet = rec.OccurredAt
		}
	}

	busy, err := relay.tick(context.Background())
	require.NoError(t, err)
	assert.False(t, busy, "nothing left to publish")

	pending := -1
	err = h.Store.Do(context.Background(), application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		_, n, err := r.Outbox.OldestPending(ctx)
		pending = n
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, 0, pending)
}

func balanceChanges(records []application.OutboxRecord, walletID uuid.UUID) []application.OutboxRecord {
	var out []application.OutboxRecord
	for _, r := range records {
		if r.EventType == event.WalletBalanceChanged && r.AggregateID == walletID {
			out = append(out, r)
		}
	}
	return out
}

func TestOutboxRelayHoldsBackLaterEventsOfSameAggregate(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	pub := &fakePublisher{}
	relay := newRelay(h, pub, "relay-1", nil)

	w := h.OpenWallet(t, "100.00")
	h.Submit(t, h.Command(w, "BET", "b1", "10.00"))
	require.Equal(t, 4, h.Store.OutboxCount())

	busy, err := relay.tick(context.Background())
	require.NoError(t, err)
	assert.True(t, busy)
	assert.Len(t, pub.published, 3, "the second balance change of the wallet waits for the first to be published")
	require.Len(t, balanceChanges(pub.published, w.ID), 1)

	_, err = relay.tick(context.Background())
	require.NoError(t, err)
	changes := balanceChanges(pub.published, w.ID)
	require.Len(t, changes, 2)
	assert.True(t, changes[0].OccurredAt.Before(changes[1].OccurredAt) || changes[0].EventID.String() < changes[1].EventID.String())
}

func TestOutboxRelayRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	pub := &fakePublisher{failUntil: 2}
	relay := newRelay(h, pub, "relay-1", nil)
	h.OpenWallet(t, "100.00")

	_, err := relay.tick(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pub.published)
	assert.Equal(t, 1, h.Metrics.Count("outbox/retried/"+string(event.WagerTransactionProcessed)))
	assert.Equal(t, 1, h.Metrics.Count("outbox/retried/"+string(event.WalletBalanceChanged)))

	_, err = relay.tick(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pub.published, "not due again until the backoff elapses")

	h.Clock.Advance(2 * time.Second)
	_, err = relay.tick(context.Background())
	require.NoError(t, err)
	assert.Len(t, pub.published, 2, "published after the backoff, attempts recorded")
}

func TestOutboxRelayRecoversAbandonedLease(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	h.OpenWallet(t, "100.00")

	crashing := &fakePublisher{}
	fault := NewFault(FaultOutboxAfterPublish, discard)
	fault.exit = func(int) { panic("process exited") }
	dying := newRelay(h, crashing, "relay-dying", fault)
	func() {
		defer func() { require.Equal(t, "process exited", recover()) }()
		_, _ = dying.tick(context.Background())
	}()
	require.Len(t, crashing.published, 1, "published exactly one event before dying")
	publishedBeforeCrash := crashing.published[0].EventID

	survivor := &fakePublisher{}
	other := newRelay(h, survivor, "relay-2", nil)
	_, err := other.tick(context.Background())
	require.NoError(t, err)
	assert.Empty(t, survivor.published, "leased events are invisible to others until the lease expires")

	h.Clock.Advance(31 * time.Second)
	_, err = other.tick(context.Background())
	require.NoError(t, err)
	require.Len(t, survivor.published, 2, "the survivor takes over both events")
	assert.Contains(t, survivor.ids(), publishedBeforeCrash, "the republished event keeps its eventId")

	pending := -1
	require.NoError(t, h.Store.Do(context.Background(), application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		_, n, err := r.Outbox.OldestPending(ctx)
		pending = n
		return err
	}))
	assert.Equal(t, 0, pending)
}

func TestOutboxRelayTwoPublishersNeverDuplicate(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	pub := &fakePublisher{}
	a := newRelay(h, pub, "relay-a", nil)
	b := newRelay(h, pub, "relay-b", nil)

	for i := 0; i < 5; i++ {
		h.OpenWallet(t, "10.00")
	}

	var wg sync.WaitGroup
	for _, relay := range []*OutboxRelay{a, b} {
		wg.Add(1)
		go func(r *OutboxRelay) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_, _ = r.tick(context.Background())
			}
		}(relay)
	}
	wg.Wait()

	ids := pub.ids()
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		assert.False(t, seen[id], "event %s published twice", id)
		seen[id] = true
	}
	assert.Len(t, ids, 10, "5 wallets x 2 events, each exactly once")
}

type cancellingPublisher struct {
	inner  *fakePublisher
	cancel context.CancelFunc
}

func (p *cancellingPublisher) Publish(ctx context.Context, rec application.OutboxRecord) error {
	err := p.inner.Publish(ctx, rec)
	p.cancel()
	return err
}

func TestOutboxRelayReleasesClaimedEventsOnShutdown(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	h.OpenWallet(t, "100.00")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := &fakePublisher{}
	stopping := newRelay(h, &cancellingPublisher{inner: inner, cancel: cancel}, "relay-stopping", nil)
	busy, err := stopping.tick(ctx)
	require.NoError(t, err)
	assert.True(t, busy)
	require.Len(t, inner.published, 1, "shutdown began right after the first publish")

	survivor := &fakePublisher{}
	other := newRelay(h, survivor, "relay-2", nil)
	_, err = other.tick(context.Background())
	require.NoError(t, err)
	assert.Len(t, survivor.published, 1, "the abandoned event is claimable at once, without waiting for the lease")
}

func TestOutboxRelaySkipsEventsWhoseLeaseExpired(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	h.OpenWallet(t, "100.00")
	pub := &fakePublisher{}
	relay := newRelay(h, pub, "relay-slow", nil)

	var claimed []application.OutboxRecord
	require.NoError(t, h.Store.Do(context.Background(), application.TxOptions{}, func(ctx context.Context, r application.Repos) error {
		var err error
		claimed, err = r.Outbox.Claim(ctx, "relay-slow", h.Clock.Now(), time.Second, 10)
		return err
	}))
	require.Len(t, claimed, 2)

	h.Clock.Advance(2 * time.Second)
	for _, rec := range claimed {
		relay.publishOne(context.Background(), rec)
	}
	assert.Empty(t, pub.published, "a publisher never sends an event whose lease it no longer holds")
}
