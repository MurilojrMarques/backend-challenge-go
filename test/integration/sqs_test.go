//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/awssqs"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/worker"
	"github.com/MurilojrMarques/backend-challenge-go/test/testutil"
)

func sqsConfig() config.SQS {
	return config.SQS{
		WagerQueueURL:     ls.Queue(testutil.WagerQueue),
		WagerDLQURL:       ls.Queue(testutil.WagerDLQ),
		EventsQueueURL:    ls.Queue(testutil.EventsQueue),
		ConsumerName:      "wager-transactions",
		WaitTime:          time.Second,
		VisibilityTimeout: 5 * time.Second,
		MaxMessages:       10,
		RetryBackoffBase:  time.Second,
		RetryBackoffMax:   10 * time.Second,
	}
}

func newSQSClient(t *testing.T) *sqs.Client {
	t.Helper()
	client, err := awssqs.NewClient(context.Background(), config.AWS{Region: testutil.Region, EndpointURL: ls.URL})
	require.NoError(t, err)
	return client
}

func TestQueueAdapter(t *testing.T) {
	ctx := context.Background()
	client := newSQSClient(t)
	cfg := sqsConfig()
	q := awssqs.NewQueue(client, cfg)
	raw := testutil.NewSQS(ls.URL)

	require.NoError(t, awssqs.NewHealth(client, cfg.WagerQueueURL).Check(ctx))

	probe := newID().String()
	require.NoError(t, testutil.Send(ctx, raw, cfg.WagerQueueURL, "probe-group", probe, map[string]any{"probe": probe}))

	msgs, err := q.Receive(ctx)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	m := msgs[0]
	assert.Equal(t, "probe-group", m.GroupID)
	assert.Equal(t, 1, m.ReceiveCount)
	assert.Contains(t, string(m.Body), probe)

	require.NoError(t, q.ChangeVisibility(ctx, m.ReceiptHandle, 0))
	msgs, err = q.Receive(ctx)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "visibility zero redelivers at once")
	assert.Equal(t, m.ID, msgs[0].ID)
	assert.Equal(t, 2, msgs[0].ReceiveCount)
	m = msgs[0]

	require.NoError(t, q.SendToDeadLetter(ctx, m, "invalid_envelope"))
	dead, err := testutil.Receive(ctx, raw, cfg.WagerDLQURL, 5)
	require.NoError(t, err)
	require.Len(t, dead, 1)
	assert.Equal(t, string(m.Body), aws.ToString(dead[0].Body))
	assert.Equal(t, "invalid_envelope", aws.ToString(dead[0].MessageAttributes["reason"].StringValue))
	assert.Equal(t, m.ID, aws.ToString(dead[0].MessageAttributes["originalMessageId"].StringValue))
	require.NoError(t, testutil.Delete(ctx, raw, cfg.WagerDLQURL, aws.ToString(dead[0].ReceiptHandle)))

	require.NoError(t, q.Delete(ctx, m.ReceiptHandle))
	msgs, err = q.Receive(ctx)
	require.NoError(t, err)
	assert.Empty(t, msgs)

	publisher := awssqs.NewPublisher(client, cfg)
	rec := application.OutboxRecord{EventID: newID(), EventType: event.WalletBalanceChanged, AggregateID: newID(), Payload: []byte(`{"probe":"` + probe + `"}`)}
	require.NoError(t, publisher.Publish(ctx, rec))
	require.NoError(t, publisher.Publish(ctx, rec))
	events, err := testutil.Receive(ctx, raw, cfg.EventsQueueURL, 5)
	require.NoError(t, err)
	require.Len(t, events, 1, "the eventId is the deduplication id, so a republish is dropped by SQS")
	assert.Equal(t, rec.AggregateID.String(), events[0].Attributes["MessageGroupId"])
	assert.Equal(t, string(event.WalletBalanceChanged), aws.ToString(events[0].MessageAttributes["eventType"].StringValue))
	require.NoError(t, testutil.Delete(ctx, raw, cfg.EventsQueueURL, aws.ToString(events[0].ReceiptHandle)))
}

func TestConsumerAndRelayAgainstLocalStack(t *testing.T) {
	ctx := context.Background()
	s := newServices(t)
	client := newSQSClient(t)
	cfg := sqsConfig()
	raw := testutil.NewSQS(ls.URL)
	w := s.open(t, "100.00")

	backoff := wagering.Backoff{Base: cfg.RetryBackoffBase, Max: cfg.RetryBackoffMax}
	consumer := worker.NewConsumer(awssqs.NewQueue(client, cfg), s.wagers, worker.ConsumerOptions{
		ConsumerName:      cfg.ConsumerName,
		VisibilityTimeout: cfg.VisibilityTimeout,
		RetryBackoff:      backoff,
	}, application.NopMetrics{}, discard, worker.NewFault("", discard))
	require.NoError(t, consumer.Start(ctx))
	stopConsumer := func() {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		require.NoError(t, consumer.Stop(sctx))
	}

	bet := testutil.NewWager(w.ID.String(), w.PlayerID.String(), "bet-"+newID().String(), "BET", "30.00")
	messageID := "msg-" + bet.ExternalTransactionID
	require.NoError(t, testutil.Send(ctx, raw, cfg.WagerQueueURL, w.ID.String(), messageID, bet.Envelope(messageID)))
	require.NoError(t, testutil.Poll(ctx, 30*time.Second, func() (bool, error) {
		view, err := s.wagers.GetByExternalID(ctx, provider, testutil.ProviderAClient, bet.ExternalTransactionID)
		if errors.Is(err, application.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return view.Status == wager.Processed, nil
	}), "the consumer applies the message through the shared use case")
	assert.True(t, brl("70.00").Equal(s.balance(t, w.ID)))

	require.NoError(t, testutil.Send(ctx, raw, cfg.WagerQueueURL, w.ID.String(), messageID+"-redelivery", bet.Envelope(messageID)))
	poison := bet
	poison.ExternalTransactionID = bet.ExternalTransactionID + "-poison"
	poison.Kind = "BONUS"
	poisonID := "msg-" + poison.ExternalTransactionID
	require.NoError(t, testutil.Send(ctx, raw, cfg.WagerQueueURL, w.ID.String(), poisonID, poison.Envelope(poisonID)))

	found := false
	require.NoError(t, testutil.Poll(ctx, 30*time.Second, func() (bool, error) {
		msgs, err := testutil.Receive(ctx, raw, cfg.WagerDLQURL, 1)
		if err != nil {
			return false, err
		}
		for _, m := range msgs {
			if testutil.Payload(m)["messageId"] == poisonID {
				found = true
				assert.Equal(t, "invalid_input", aws.ToString(m.MessageAttributes["reason"].StringValue))
			}
			_ = testutil.Delete(ctx, raw, cfg.WagerDLQURL, aws.ToString(m.ReceiptHandle))
		}
		return found, nil
	}), "an unknown kind is permanent and lands in the dead-letter queue")
	require.NoError(t, testutil.Poll(ctx, 30*time.Second, func() (bool, error) {
		n, err := testutil.Depth(ctx, raw, cfg.WagerQueueURL)
		return n == 0, err
	}), "every message was acknowledged")
	stopConsumer()

	assert.True(t, brl("70.00").Equal(s.balance(t, w.ID)), "the redelivered message was a replay")
	require.NoError(t, s.do(t, func(r application.Repos) error {
		entries, err := r.Ledger.List(ctx, w.ID, application.LedgerCursor{}, 10)
		assert.Len(t, entries, 2)
		return err
	}))

	relay := worker.NewOutboxRelay(s.uow, awssqs.NewPublisher(client, cfg), application.SystemClock{}, worker.OutboxOptions{
		PollInterval: 200 * time.Millisecond,
		BatchSize:    50,
		Lease:        30 * time.Second,
		RetryBackoff: backoff,
	}, application.NopMetrics{}, discard, worker.NewFault("", discard))
	require.NoError(t, relay.Start(ctx))

	var versions []float64
	seen := map[string]bool{}
	require.NoError(t, testutil.Poll(ctx, 60*time.Second, func() (bool, error) {
		msgs, err := testutil.Receive(ctx, raw, cfg.EventsQueueURL, 1)
		if err != nil {
			return false, err
		}
		for _, m := range msgs {
			payload := testutil.Payload(m)
			eventID, _ := payload["eventId"].(string)
			assert.False(t, seen[eventID], "event %s delivered twice", eventID)
			seen[eventID] = true
			data, _ := payload["data"].(map[string]any)
			if payload["eventType"] == string(event.WalletBalanceChanged) && data["walletId"] == w.ID.String() {
				version, _ := data["walletVersion"].(float64)
				versions = append(versions, version)
			}
			_ = testutil.Delete(ctx, raw, cfg.EventsQueueURL, aws.ToString(m.ReceiptHandle))
		}
		return len(versions) >= 2, nil
	}), "the relay publishes the wallet events")
	assert.Equal(t, []float64{1, 2}, versions, "per-wallet order is preserved")

	require.NoError(t, testutil.Poll(ctx, 30*time.Second, func() (bool, error) {
		pending := -1
		err := s.do(t, func(r application.Repos) error {
			_, n, err := r.Outbox.OldestPending(ctx)
			pending = n
			return err
		})
		return pending == 0, err
	}), "nothing stays behind in the outbox")
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, relay.Stop(sctx))
}
