//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/test/testutil"
)

type stack struct {
	baseURLs   []string
	internal   string
	providerA  string
	providerB  string
	sqs        *sqs.Client
	wagerQueue string
	dlq        string
	events     string
}

var env stack

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := connect(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\nstart the stack first: make e2e-up\n", err)
		os.Exit(1)
	}
	env = s
	os.Exit(m.Run())
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func connect(ctx context.Context) (stack, error) {
	s := stack{
		baseURLs: strings.Split(envOr("E2E_BASE_URLS", "http://localhost:8081,http://localhost:8082,http://localhost:8083"), ","),
	}
	keycloak := envOr("E2E_KEYCLOAK_URL", "http://localhost:8180")
	localstack := envOr("E2E_LOCALSTACK_URL", "http://localhost:4566")

	for _, base := range s.baseURLs {
		err := testutil.Poll(ctx, 90*time.Second, func() (bool, error) {
			res, err := testutil.NewClient(base, "").Get(ctx, "/health/ready")
			if err != nil {
				return false, nil
			}
			return res.Status == http.StatusOK, nil
		})
		if err != nil {
			return stack{}, fmt.Errorf("%s is not ready: %w", base, err)
		}
	}

	var err error
	if s.internal, err = testutil.Token(ctx, keycloak, testutil.InternalClient); err != nil {
		return stack{}, err
	}
	if s.providerA, err = testutil.Token(ctx, keycloak, testutil.ProviderAClient); err != nil {
		return stack{}, err
	}
	if s.providerB, err = testutil.Token(ctx, keycloak, testutil.ProviderBClient); err != nil {
		return stack{}, err
	}
	s.sqs = testutil.NewSQS(localstack)
	s.wagerQueue = testutil.QueueURL(localstack, testutil.WagerQueue)
	s.dlq = testutil.QueueURL(localstack, testutil.WagerDLQ)
	s.events = testutil.QueueURL(localstack, testutil.EventsQueue)
	if _, err := testutil.Depth(ctx, s.sqs, s.wagerQueue); err != nil {
		return stack{}, fmt.Errorf("localstack at %s: %w", localstack, err)
	}
	return s, nil
}

func (s stack) at(i int, token string) *testutil.Client {
	return testutil.NewClient(s.baseURLs[i%len(s.baseURLs)], token)
}

func (s stack) internalAt(i int) *testutil.Client  { return s.at(i, s.internal) }
func (s stack) providerAAt(i int) *testutil.Client { return s.at(i, s.providerA) }

type wallet struct {
	id     string
	player string
}

func openWallet(t *testing.T, amount string) wallet {
	t.Helper()
	player := uuid.Must(uuid.NewV7()).String()
	res, err := env.internalAt(0).OpenWallet(context.Background(), player, amount)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.Status, string(res.Raw))
	return wallet{id: res.String("id"), player: player}
}

func externalID(prefix string) string {
	return prefix + "-" + uuid.Must(uuid.NewV7()).String()[24:]
}

func TestEveryReplicaIsReady(t *testing.T) {
	t.Parallel()
	for i := range env.baseURLs {
		res, err := env.at(i, "").Get(context.Background(), "/health/ready")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, res.Status, string(res.Raw))
		checks, _ := res.Body["checks"].(map[string]any)
		assert.Equal(t, "up", checks["postgres"])
		assert.Equal(t, "up", checks["sqs"])
	}
}

func TestWalletLifecycleAcrossReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "1000.00")

	res, err := env.internalAt(1).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	assert.Equal(t, "1000.00", res.Amount("balance"))
	assert.Equal(t, float64(1), res.Number("version"))

	res, err = env.internalAt(2).OpenWallet(ctx, w.player, "5.00")
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, res.Status)
	assert.Equal(t, "WALLET_ALREADY_EXISTS", res.String("code"))

	res, err = env.providerAAt(0).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, res.Status, "providers never read wallets directly")

	res, err = env.at(0, "").Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, res.Status)
	assert.Contains(t, res.Header.Get("WWW-Authenticate"), "Bearer")

	res, err = env.internalAt(1).Get(ctx, "/wallets/"+w.id+"/ledger")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	assert.Len(t, res.List("entries"), 1, "the opening credit is the only entry")

	res, err = env.internalAt(2).Do(ctx, http.MethodPost, "/wallets/"+w.id+"/reconciliation", nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	assert.True(t, res.Bool("consistent"))
	assert.Equal(t, "0.00", res.Amount("difference"))
	assert.Equal(t, float64(1), res.Number("checkedEntries"))
}

func TestSameBetFiftyTimesAcrossReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "1000.00")
	bet := testutil.NewWager(w.id, w.player, externalID("bet"), "BET", "25.00")

	results := make([]testutil.Response, 50)
	errs := make([]error, 50)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = env.providerAAt(i).Submit(ctx, bet, bet.Key())
		}(i)
	}
	wg.Wait()

	replays := 0
	for i, res := range results {
		require.NoError(t, errs[i])
		require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
		assert.Equal(t, "PROCESSED", res.String("status"))
		assert.Equal(t, "975.00", res.Amount("balance"), "every answer reports the balance observed by the original processing")
		assert.Equal(t, results[0].String("transactionId"), res.String("transactionId"))
		if res.Bool("idempotentReplay") {
			replays++
		}
	}
	assert.Equal(t, 49, replays)

	res, err := env.internalAt(0).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, "975.00", res.Amount("balance"))
	assert.Equal(t, float64(2), res.Number("version"))

	res, err = env.internalAt(1).Get(ctx, "/wallets/"+w.id+"/ledger")
	require.NoError(t, err)
	assert.Len(t, res.List("entries"), 2, "opening credit and a single debit")
}

func TestTwoBetsOfEightyOnOneHundred(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "100.00")

	results := make([]testutil.Response, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bet := testutil.NewWager(w.id, w.player, externalID(fmt.Sprintf("bet%d", i)), "BET", "80.00")
			results[i], errs[i] = env.providerAAt(i).Submit(ctx, bet, bet.Key())
		}(i)
	}
	wg.Wait()

	statuses := map[string]int{}
	for i, res := range results {
		require.NoError(t, errs[i])
		statuses[res.String("status")]++
		if res.String("status") == "REJECTED" {
			assert.Equal(t, http.StatusUnprocessableEntity, res.Status)
			assert.Equal(t, "INSUFFICIENT_FUNDS", res.String("failureCode"))
		}
	}
	assert.Equal(t, map[string]int{"PROCESSED": 1, "REJECTED": 1}, statuses)

	res, err := env.internalAt(2).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, "20.00", res.Amount("balance"))

	res, err = env.internalAt(0).Do(ctx, http.MethodPost, "/wallets/"+w.id+"/reconciliation", nil, nil)
	require.NoError(t, err)
	assert.True(t, res.Bool("consistent"), string(res.Raw))
	assert.Equal(t, float64(2), res.Number("checkedEntries"))
}

func TestIdempotencyContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "100.00")
	bet := testutil.NewWager(w.id, w.player, externalID("bet"), "BET", "10.00")

	first, err := env.providerAAt(0).Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.Status, string(first.Raw))

	changed := bet
	changed.Amount = "11.00"
	res, err := env.providerAAt(1).Submit(ctx, changed, bet.Key())
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, res.Status)
	assert.Equal(t, "IDEMPOTENCY_PAYLOAD_CONFLICT", res.String("code"))

	res, err = env.providerAAt(2).Submit(ctx, bet, bet.Key()+"-other")
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, res.Status)
	assert.Equal(t, "IDEMPOTENCY_KEY_MISMATCH", res.String("code"))

	res, err = env.providerAAt(0).Do(ctx, http.MethodPost, "/wagering/transactions", bet.Body(), nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, res.Status)
	assert.Equal(t, "MISSING_IDEMPOTENCY_KEY", res.String("code"))

	res, err = env.at(1, env.providerB).Get(ctx, "/providers/provider-a/wagering/transactions/"+bet.ExternalTransactionID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, res.Status, "another provider cannot see the transaction")

	res, err = env.at(2, env.providerB).Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, res.Status, "a provider cannot submit on behalf of another")
}

func TestReversalBeforeReference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "100.00")
	betID := externalID("bet")

	rollback := testutil.NewWager(w.id, w.player, externalID("rb"), "ROLLBACK", "30.00")
	rollback.Reference = betID
	res, err := env.providerAAt(0).Submit(ctx, rollback, rollback.Key())
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, res.Status, string(res.Raw))
	assert.Equal(t, "PENDING_REFERENCE", res.String("status"))
	txID := res.String("transactionId")

	bet := testutil.NewWager(w.id, w.player, betID, "BET", "30.00")
	res, err = env.providerAAt(1).Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	assert.Equal(t, "70.00", res.Amount("balance"))

	require.NoError(t, testutil.Poll(ctx, 45*time.Second, func() (bool, error) {
		res, err := env.providerAAt(2).Get(ctx, "/wagering/transactions/"+txID)
		if err != nil {
			return false, err
		}
		return res.String("status") == "PROCESSED", nil
	}), "the pending resolver processes the rollback once its reference exists")

	res, err = env.internalAt(0).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, "100.00", res.Amount("balance"), "the rollback credited the bet back")

	again := testutil.NewWager(w.id, w.player, externalID("refund"), "REFUND", "30.00")
	again.Reference = betID
	res, err = env.providerAAt(1).Submit(ctx, again, again.Key())
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, res.Status)
	assert.Equal(t, "REFERENCE_ALREADY_REVERSED", res.String("failureCode"))
}

func TestSQSAndHTTPShareIdempotency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "100.00")
	bet := testutil.NewWager(w.id, w.player, externalID("sqs"), "BET", "40.00")
	messageID := "msg-" + bet.ExternalTransactionID

	require.NoError(t, testutil.Send(ctx, env.sqs, env.wagerQueue, w.id, messageID, bet.Envelope(messageID)))
	require.NoError(t, testutil.Poll(ctx, 45*time.Second, func() (bool, error) {
		res, err := env.providerAAt(0).Get(ctx, "/providers/provider-a/wagering/transactions/"+bet.ExternalTransactionID)
		if err != nil {
			return false, err
		}
		return res.Status == http.StatusOK && res.String("status") == "PROCESSED", nil
	}))

	require.NoError(t, testutil.Send(ctx, env.sqs, env.wagerQueue, w.id, messageID+"-redelivery", bet.Envelope(messageID)))
	res, err := env.providerAAt(1).Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	assert.True(t, res.Bool("idempotentReplay"), "HTTP after SQS is a replay")
	assert.Equal(t, "60.00", res.Amount("balance"))

	time.Sleep(3 * time.Second)
	res, err = env.internalAt(2).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, "60.00", res.Amount("balance"), "the redelivered message did not debit twice")
	res, err = env.internalAt(0).Get(ctx, "/wallets/"+w.id+"/ledger")
	require.NoError(t, err)
	assert.Len(t, res.List("entries"), 2)

	poison := bet
	poison.ExternalTransactionID = externalID("poison")
	poison.Kind = "BONUS"
	poisonID := "msg-" + poison.ExternalTransactionID
	require.NoError(t, testutil.Send(ctx, env.sqs, env.wagerQueue, w.id, poisonID, poison.Envelope(poisonID)))
	found := false
	require.NoError(t, testutil.Poll(ctx, 45*time.Second, func() (bool, error) {
		msgs, err := testutil.Receive(ctx, env.sqs, env.dlq, 2)
		if err != nil {
			return false, err
		}
		for _, m := range msgs {
			if testutil.Payload(m)["messageId"] == poisonID {
				found = true
				assert.Equal(t, "invalid_input", aws.ToString(m.MessageAttributes["reason"].StringValue))
				_ = testutil.Delete(ctx, env.sqs, env.dlq, aws.ToString(m.ReceiptHandle))
			}
		}
		return found, nil
	}), "an invalid kind is permanent and lands in the dead-letter queue")
}

func TestWalletEventsArePublishedInOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "100.00")
	for i, amount := range []string{"10.00", "20.00", "30.00"} {
		bet := testutil.NewWager(w.id, w.player, externalID(fmt.Sprintf("ev%d", i)), "BET", amount)
		res, err := env.providerAAt(i).Submit(ctx, bet, bet.Key())
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	}

	var versions []float64
	seen := map[string]bool{}
	require.NoError(t, testutil.Poll(ctx, 60*time.Second, func() (bool, error) {
		msgs, err := testutil.Receive(ctx, env.sqs, env.events, 2)
		if err != nil {
			return false, err
		}
		for _, m := range msgs {
			payload := testutil.Payload(m)
			data, _ := payload["data"].(map[string]any)
			eventID, _ := payload["eventId"].(string)
			assert.False(t, seen[eventID], "event %s delivered twice", eventID)
			seen[eventID] = true
			if payload["eventType"] == "WalletBalanceChanged" && data["walletId"] == w.id {
				assert.Equal(t, w.id, m.Attributes["MessageGroupId"], "wallet events share the wallet as message group")
				versions = append(versions, data["walletVersion"].(float64))
			}
			_ = testutil.Delete(ctx, env.sqs, env.events, aws.ToString(m.ReceiptHandle))
		}
		return len(versions) >= 4, nil
	}))
	assert.Equal(t, []float64{1, 2, 3, 4}, versions, "balance events arrive in version order")
}

func TestHTTPAndSQSRaceForTheSameOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := openWallet(t, "100.00")
	bet := testutil.NewWager(w.id, w.player, externalID("race"), "BET", "30.00")
	messageID := "msg-" + bet.ExternalTransactionID

	replicas := len(env.baseURLs)
	results := make([]testutil.Response, replicas)
	errs := make([]error, replicas+1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errs[replicas] = testutil.Send(ctx, env.sqs, env.wagerQueue, w.id, messageID, bet.Envelope(messageID))
	}()
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = env.providerAAt(i).Submit(ctx, bet, bet.Key())
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	transactionID := results[0].String("transactionId")
	applied := 0
	for _, res := range results {
		require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
		assert.Equal(t, "PROCESSED", res.String("status"))
		assert.Equal(t, "70.00", res.Amount("balance"), "every channel reports the balance of the single application")
		assert.Equal(t, transactionID, res.String("transactionId"))
		if !res.Bool("idempotentReplay") {
			applied++
		}
	}
	assert.LessOrEqual(t, applied, 1, "at most one HTTP call applies the operation; when SQS wins, every HTTP call is a replay")

	require.NoError(t, testutil.Poll(ctx, 60*time.Second, func() (bool, error) {
		n, err := testutil.Depth(ctx, env.sqs, env.wagerQueue)
		return n == 0, err
	}), "the queued copy of the operation is consumed")

	res, err := env.internalAt(0).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, "70.00", res.Amount("balance"))
	assert.Equal(t, float64(2), res.Number("version"))
	res, err = env.internalAt(1).Get(ctx, "/wallets/"+w.id+"/ledger")
	require.NoError(t, err)
	assert.Len(t, res.List("entries"), 2, "opening credit and one debit, whichever channel won")
	res, err = env.internalAt(2).Do(ctx, http.MethodPost, "/wallets/"+w.id+"/reconciliation", nil, nil)
	require.NoError(t, err)
	assert.True(t, res.Bool("consistent"), string(res.Raw))

	again, err := env.providerAAt(1).Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	assert.True(t, again.Bool("idempotentReplay"))
	assert.Equal(t, transactionID, again.String("transactionId"))
}

func TestIndependentWalletsProgressInParallelAcrossReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const walletCount, betsPerWallet = 6, 5

	wallets := make([]wallet, walletCount)
	for i := range wallets {
		wallets[i] = openWallet(t, "100.00")
	}

	type outcome struct {
		res testutil.Response
		err error
	}
	outcomes := make([][]outcome, walletCount)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for wi, w := range wallets {
		outcomes[wi] = make([]outcome, betsPerWallet)
		for bi := 0; bi < betsPerWallet; bi++ {
			wg.Add(1)
			go func(wi, bi int, w wallet) {
				defer wg.Done()
				<-start
				bet := testutil.NewWager(w.id, w.player, externalID(fmt.Sprintf("par%d-%d", wi, bi)), "BET", "10.00")
				res, err := env.providerAAt(wi+bi).Submit(ctx, bet, bet.Key())
				outcomes[wi][bi] = outcome{res: res, err: err}
			}(wi, bi, w)
		}
	}
	close(start)
	wg.Wait()

	for wi, w := range wallets {
		for bi, o := range outcomes[wi] {
			require.NoError(t, o.err)
			require.Equal(t, http.StatusOK, o.res.Status, "wallet %d bet %d: %s", wi, bi, o.res.Raw)
			assert.Equal(t, "PROCESSED", o.res.String("status"))
		}
		res, err := env.internalAt(wi).Get(ctx, "/wallets/"+w.id)
		require.NoError(t, err)
		assert.Equal(t, "50.00", res.Amount("balance"), "wallet %d", wi)
		assert.Equal(t, float64(1+betsPerWallet), res.Number("version"), "wallet %d", wi)
		res, err = env.internalAt(wi+1).Do(ctx, http.MethodPost, "/wallets/"+w.id+"/reconciliation", nil, nil)
		require.NoError(t, err)
		assert.True(t, res.Bool("consistent"), "wallet %d: %s", wi, res.Raw)
		assert.Equal(t, float64(1+betsPerWallet), res.Number("checkedEntries"), "wallet %d", wi)
	}
}
