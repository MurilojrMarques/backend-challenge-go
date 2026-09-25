package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
)

type fakeQueue struct {
	mu         sync.Mutex
	pending    []application.Message
	deleted    map[string]bool
	visibility map[string]time.Duration
	dlq        []application.Message
	receiveErr error
}

func newFakeQueue() *fakeQueue {
	return &fakeQueue{deleted: map[string]bool{}, visibility: map[string]time.Duration{}}
}

func (q *fakeQueue) enqueue(msgs ...application.Message) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, msgs...)
}

func (q *fakeQueue) Receive(context.Context) ([]application.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.receiveErr != nil {
		return nil, q.receiveErr
	}
	out := q.pending
	q.pending = nil
	return out, nil
}

func (q *fakeQueue) Delete(_ context.Context, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deleted[receipt] = true
	return nil
}

func (q *fakeQueue) ChangeVisibility(_ context.Context, receipt string, d time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.visibility[receipt] = d
	return nil
}

func (q *fakeQueue) SendToDeadLetter(_ context.Context, m application.Message, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dlq = append(q.dlq, m)
	return nil
}

func envelope(t *testing.T, w wallets.View, kind, externalID, amount string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"messageId":  "msg-" + externalID,
		"type":       wagering.InboundMessageType,
		"occurredAt": "2026-09-24T12:00:00Z",
		"data": map[string]any{
			"providerId":            "provider-a",
			"externalTransactionId": externalID,
			"idempotencyKey":        "provider-a:" + externalID,
			"playerId":              w.PlayerID.String(),
			"walletId":              w.ID.String(),
			"roundId":               "round-1",
			"gameId":                "fortune-chimp",
			"kind":                  kind,
			"money":                 map[string]string{"amount": amount, "currency": "BRL"},
		},
	})
	require.NoError(t, err)
	return body
}

func message(id string, body []byte, receiveCount int) application.Message {
	return application.Message{ID: id, ReceiptHandle: "rh-" + id, Body: body, ReceiveCount: receiveCount}
}

func newConsumer(t *testing.T, h *apptest.Harness, q *fakeQueue) *Consumer {
	t.Helper()
	return NewConsumer(q, h.Wagers, ConsumerOptions{
		ConsumerName:      "wager-transactions",
		VisibilityTimeout: 30 * time.Second,
		RetryBackoff:      wagering.Backoff{Base: time.Second, Max: time.Minute},
	}, h.Metrics, discard, NewFault("", discard))
}

func TestConsumerProcessesAndAcks(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	q := newFakeQueue()
	c := newConsumer(t, h, q)
	w := h.OpenWallet(t, "100.00")

	body := envelope(t, w, "BET", "b1", "25.00")
	q.enqueue(message("m1", body, 1))
	busy, err := c.tick(context.Background())
	require.NoError(t, err)
	assert.True(t, busy)
	assert.True(t, q.deleted["rh-m1"], "processed message is deleted after commit")
	assert.True(t, apptest.BRL("75.00").Equal(h.Balance(t, w.ID)))
	assert.Equal(t, 1, h.Metrics.Count("message/processed"))

	q.enqueue(message("m1", body, 2))
	_, err = c.tick(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, h.Metrics.Count("message/replay"), "redelivery is a replay and is acked")
	assert.True(t, apptest.BRL("75.00").Equal(h.Balance(t, w.ID)))

	q.enqueue(message("m2", envelope(t, w, "BET", "b2", "500.00"), 1))
	_, err = c.tick(context.Background())
	require.NoError(t, err)
	assert.True(t, q.deleted["rh-m2"], "business rejection is terminal and acked")
	assert.Equal(t, 1, h.Metrics.Count("message/rejected"))

	busy, err = c.tick(context.Background())
	require.NoError(t, err)
	assert.False(t, busy, "empty receive is not busy")
	assert.Empty(t, q.dlq)
}

func TestConsumerDeadLettersPermanentFailures(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	q := newFakeQueue()
	c := newConsumer(t, h, q)
	w := h.OpenWallet(t, "100.00")

	valid := envelope(t, w, "BET", "b1", "25.00")
	q.enqueue(
		message("bad-json", []byte("{"), 1),
		message("wrong-type", []byte(`{"messageId":"x","type":"Other","occurredAt":"2026-09-24T12:00:00Z","data":{}}`), 1),
		message("bad-amount", envelope(t, w, "BET", "b9", "25"), 1),
		message("m-ok", valid, 1),
	)
	_, err := c.tick(context.Background())
	require.NoError(t, err)
	assert.Len(t, q.dlq, 3)
	for _, id := range []string{"bad-json", "wrong-type", "bad-amount"} {
		assert.True(t, q.deleted["rh-"+id], "%s deleted after copy to dlq", id)
	}
	assert.True(t, q.deleted["rh-m-ok"])
	assert.Equal(t, 3, h.Metrics.Count("message/dead_letter"))

	tampered := append(append([]byte{}, valid[:len(valid)-1]...), ' ', '}')
	q.enqueue(message("m-ok", tampered, 2))
	_, err = c.tick(context.Background())
	require.NoError(t, err)
	assert.Len(t, q.dlq, 4, "same messageId with a different body is poison")
}

func TestConsumerRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	q := newFakeQueue()
	c := newConsumer(t, h, q)
	w := h.OpenWallet(t, "100.00")

	h.Store.FailNext(application.ErrUnavailable)
	q.enqueue(message("m1", envelope(t, w, "BET", "b1", "25.00"), 3))
	_, err := c.tick(context.Background())
	require.NoError(t, err)
	assert.False(t, q.deleted["rh-m1"], "transient failure keeps the message")
	assert.Empty(t, q.dlq)
	assert.Equal(t, 4*time.Second, q.visibility["rh-m1"], "visibility follows the backoff for the third receive")
	assert.Equal(t, 1, h.Metrics.Count("message/retry"))
	assert.True(t, apptest.BRL("100.00").Equal(h.Balance(t, w.ID)))

	q.enqueue(message("m1", envelope(t, w, "BET", "b1", "25.00"), 4))
	_, err = c.tick(context.Background())
	require.NoError(t, err)
	assert.True(t, q.deleted["rh-m1"])
	assert.True(t, apptest.BRL("75.00").Equal(h.Balance(t, w.ID)))

	q.receiveErr = errors.New("network down")
	_, err = c.tick(context.Background())
	assert.Error(t, err, "receive failures surface to the loop for backoff")
}

func TestConsumerStopsProcessingWhenCancelled(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	q := newFakeQueue()
	c := newConsumer(t, h, q)
	w := h.OpenWallet(t, "100.00")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q.enqueue(message("m1", envelope(t, w, "BET", "b1", "25.00"), 1), message("m2", envelope(t, w, "BET", "b2", "25.00"), 1))
	busy, err := c.tick(ctx)
	require.NoError(t, err)
	assert.True(t, busy)
	assert.False(t, q.deleted["rh-m1"])
	assert.False(t, q.deleted["rh-m2"])
	assert.True(t, apptest.BRL("100.00").Equal(h.Balance(t, w.ID)), "nothing is processed after shutdown starts")
}

func TestConsumerFaultInjection(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	q := newFakeQueue()
	w := h.OpenWallet(t, "100.00")

	exited := 0
	fault := NewFault(FaultConsumerAfterCommit, discard)
	fault.exit = func(int) { exited++ }
	c := NewConsumer(q, h.Wagers, ConsumerOptions{ConsumerName: "c", VisibilityTimeout: time.Second}, h.Metrics, discard, fault)

	q.enqueue(message("m1", envelope(t, w, "BET", "b1", "25.00"), 1))
	_, err := c.tick(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, exited, "fault fires after the commit")
	assert.True(t, apptest.BRL("75.00").Equal(h.Balance(t, w.ID)), "the commit happened before the crash")
}
