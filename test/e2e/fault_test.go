//go:build e2e

package e2e

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/test/testutil"
)

const faultExitCode = 3

func requireFaultMode(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_FAULT") != "1" {
		t.Skip("fault scenarios need E2E_FAULT=1 and a stack started without consumer and outbox roles: make e2e-fault")
	}
}

func compose(ctx context.Context, args ...string) *exec.Cmd {
	base := []string{"compose", "-f", "docker-compose.yml", "-f", "docker-compose.test.yml", "--profile", "fault"}
	cmd := exec.CommandContext(ctx, "docker", append(base, args...)...)
	cmd.Dir = testutil.RepoRoot()
	return cmd
}

func faultArgs(settings map[string]string) []string {
	args := []string{"run", "--rm", "--no-deps"}
	for k, v := range settings {
		args = append(args, "-e", k+"="+v)
	}
	return args
}

func runUntilExit(t *testing.T, ctx context.Context, settings map[string]string) (int, string) {
	t.Helper()
	cmd := compose(ctx, append(faultArgs(settings), "app-fault")...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out)
	}
	require.NoError(t, err, string(out))
	return 0, string(out)
}

func runDetached(t *testing.T, ctx context.Context, settings map[string]string) {
	t.Helper()
	cmd := compose(ctx, append(append(faultArgs(settings), "-d"), "app-fault")...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	lines := strings.Fields(string(out))
	require.NotEmpty(t, lines, "docker compose run -d prints the container id")
	id := lines[len(lines)-1]
	t.Cleanup(func() {
		stop := exec.Command("docker", "stop", "-t", "30", id)
		if out, err := stop.CombinedOutput(); err != nil {
			t.Logf("docker stop %s: %v: %s", id, err, out)
		}
	})
}

func TestFaultConsumerCrashAfterCommitIsIdempotent(t *testing.T) {
	requireFaultMode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	w := openWallet(t, "100.00")
	bet := testutil.NewWager(w.id, w.player, externalID("crash"), "BET", "30.00")
	messageID := "msg-" + bet.ExternalTransactionID
	require.NoError(t, testutil.Send(ctx, env.sqs, env.wagerQueue, w.id, messageID, bet.Envelope(messageID)))

	code, out := runUntilExit(t, ctx, map[string]string{
		"FAULT_INJECT":                   "consumer.after_commit_before_ack",
		"APP_ROLES":                      "consumer",
		"SQS_VISIBILITY_TIMEOUT_SECONDS": "5",
	})
	require.Equal(t, faultExitCode, code, out)

	res, err := env.providerAAt(0).Get(ctx, "/providers/provider-a/wagering/transactions/"+bet.ExternalTransactionID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	assert.Equal(t, "PROCESSED", res.String("status"), "the commit happened before the crash")

	runDetached(t, ctx, map[string]string{"APP_ROLES": "consumer", "SQS_VISIBILITY_TIMEOUT_SECONDS": "5"})
	require.NoError(t, testutil.Poll(ctx, 90*time.Second, func() (bool, error) {
		n, err := testutil.Depth(ctx, env.sqs, env.wagerQueue)
		return n == 0, err
	}), "the restarted consumer drains the redelivered message")

	res, err = env.internalAt(1).Get(ctx, "/wallets/"+w.id)
	require.NoError(t, err)
	assert.Equal(t, "70.00", res.Amount("balance"), "the redelivery was a replay, not a second debit")
	res, err = env.internalAt(2).Get(ctx, "/wallets/"+w.id+"/ledger")
	require.NoError(t, err)
	assert.Len(t, res.List("entries"), 2)
	dlq, err := testutil.Depth(ctx, env.sqs, env.dlq)
	require.NoError(t, err)
	assert.Equal(t, 0, dlq)
}

func TestFaultOutboxCrashAfterPublishNeverDuplicates(t *testing.T) {
	requireFaultMode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	w := openWallet(t, "50.00")
	settings := map[string]string{"APP_ROLES": "outbox", "OUTBOX_LOCK_TTL": "5s", "OUTBOX_POLL_INTERVAL": "200ms"}
	crash := map[string]string{"FAULT_INJECT": "outbox.after_publish_before_mark"}
	for k, v := range settings {
		crash[k] = v
	}
	code, out := runUntilExit(t, ctx, crash)
	require.Equal(t, faultExitCode, code, out)

	runDetached(t, ctx, settings)
	msgs, err := testutil.Drain(ctx, env.sqs, env.events, 8*time.Second)
	require.NoError(t, err)

	seen := map[string]int{}
	walletEvents := map[string]int{}
	for _, m := range msgs {
		payload := testutil.Payload(m)
		eventID, _ := payload["eventId"].(string)
		seen[eventID]++
		data, _ := payload["data"].(map[string]any)
		if data["walletId"] == w.id {
			eventType, _ := payload["eventType"].(string)
			walletEvents[eventType]++
		}
	}
	for id, n := range seen {
		assert.Equal(t, 1, n, "event %s published more than once despite the crash", id)
	}
	assert.Equal(t, 1, walletEvents["WagerTransactionProcessed"], "opening event for the wallet")
	assert.Equal(t, 1, walletEvents["WalletBalanceChanged"], "balance event for the wallet")
}

func TestRestartPreservesIdempotencyAndPendencies(t *testing.T) {
	requireFaultMode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	w := openWallet(t, "100.00")
	bet := testutil.NewWager(w.id, w.player, externalID("bet"), "BET", "30.00")
	first, err := env.providerAAt(0).Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.Status, string(first.Raw))

	lateID := externalID("late")
	rollback := testutil.NewWager(w.id, w.player, externalID("rb"), "ROLLBACK", "30.00")
	rollback.Reference = lateID
	pending, err := env.providerAAt(1).Submit(ctx, rollback, rollback.Key())
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, pending.Status, string(pending.Raw))

	out, err := compose(ctx, "restart", "app").CombinedOutput()
	require.NoError(t, err, string(out))
	for i := range env.baseURLs {
		require.NoError(t, testutil.Poll(ctx, 2*time.Minute, func() (bool, error) {
			res, err := env.at(i, "").Get(ctx, "/health/ready")
			return err == nil && res.Status == http.StatusOK, nil
		}), "replica %d is back", i)
	}

	replay, err := env.providerAAt(2).Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	assert.True(t, replay.Bool("idempotentReplay"), "idempotency survived the restart")
	assert.Equal(t, "70.00", replay.Amount("balance"), "the replay still reports the originally observed balance")

	late := testutil.NewWager(w.id, w.player, lateID, "BET", "30.00")
	res, err := env.providerAAt(0).Submit(ctx, late, late.Key())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	require.NoError(t, testutil.Poll(ctx, 60*time.Second, func() (bool, error) {
		res, err := env.providerAAt(1).Get(ctx, "/wagering/transactions/"+pending.String("transactionId"))
		return err == nil && res.String("status") == "PROCESSED", nil
	}), "the pending rollback is resumed by the restarted resolver")

	res, err = env.internalAt(2).Do(ctx, http.MethodPost, "/wallets/"+w.id+"/reconciliation", nil, nil)
	require.NoError(t, err)
	assert.True(t, res.Bool("consistent"), string(res.Raw))
	assert.Equal(t, "70.00", res.Amount("storedBalance"))
}
