package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

func minimal() map[string]string {
	return map[string]string{
		"DATABASE_URL":         "postgres://u:p@postgres:5432/wallet?sslmode=disable",
		"OIDC_ISSUER":          "http://keycloak:8080/realms/wallet",
		"OIDC_AUDIENCE":        "wallet-api",
		"SQS_WAGER_QUEUE_URL":  "http://localstack:4566/000000000000/wager-transactions.fifo",
		"SQS_WAGER_DLQ_URL":    "http://localstack:4566/000000000000/wager-transactions-dlq.fifo",
		"SQS_EVENTS_QUEUE_URL": "http://localstack:4566/000000000000/wallet-events.fifo",
	}
}

func getenv(env map[string]string) config.Getenv {
	return func(k string) string { return env[k] }
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(getenv(minimal()))
	require.NoError(t, err)

	assert.Equal(t, ":8080", cfg.HTTP.Addr)
	assert.Equal(t, int64(64*1024), cfg.HTTP.MaxBodyBytes)
	assert.Equal(t, "us-east-1", cfg.AWS.Region)
	assert.Empty(t, cfg.AWS.EndpointURL)
	assert.Equal(t, "wager-transactions", cfg.SQS.ConsumerName)
	assert.Equal(t, 20*time.Second, cfg.SQS.WaitTime)
	assert.Equal(t, 30*time.Second, cfg.SQS.VisibilityTimeout)
	assert.Equal(t, 10, cfg.SQS.MaxMessages)
	assert.Equal(t, 50, cfg.Outbox.BatchSize)
	assert.Equal(t, 10, cfg.Pending.MaxAttempts)
	assert.Equal(t, 15*time.Minute, cfg.Pending.TTL)
	assert.Empty(t, cfg.Fault.InjectPoint)
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	env := minimal()
	env["HTTP_ADDR"] = " :9090 "
	env["AWS_ENDPOINT_URL"] = "http://localstack:4566"
	env["SQS_WAIT_TIME_SECONDS"] = "5"
	env["OUTBOX_POLL_INTERVAL"] = "250ms"
	env["PENDING_REFERENCE_MAX_ATTEMPTS"] = "3"
	env["FAULT_INJECT"] = "consumer.after_commit_before_ack"

	cfg, err := config.Load(getenv(env))
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.HTTP.Addr)
	assert.Equal(t, "http://localstack:4566", cfg.AWS.EndpointURL)
	assert.Equal(t, 5*time.Second, cfg.SQS.WaitTime)
	assert.Equal(t, 250*time.Millisecond, cfg.Outbox.PollInterval)
	assert.Equal(t, 3, cfg.Pending.MaxAttempts)
	assert.Equal(t, "consumer.after_commit_before_ack", cfg.Fault.InjectPoint)
}

func TestLoadRejectsInvalid(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]string{
		"missing database url":      {"DATABASE_URL": ""},
		"database url wrong scheme": {"DATABASE_URL": "mysql://h/db"},
		"database url without host": {"DATABASE_URL": "postgres:///db"},
		"missing issuer":            {"OIDC_ISSUER": ""},
		"issuer not http":           {"OIDC_ISSUER": "keycloak/realms/wallet"},
		"missing audience":          {"OIDC_AUDIENCE": ""},
		"bad endpoint":              {"AWS_ENDPOINT_URL": "localstack:4566"},
		"missing queue":             {"SQS_WAGER_QUEUE_URL": ""},
		"wait time too high":        {"SQS_WAIT_TIME_SECONDS": "21"},
		"wait time not a number":    {"SQS_WAIT_TIME_SECONDS": "twenty"},
		"visibility zero":           {"SQS_VISIBILITY_TIMEOUT_SECONDS": "0"},
		"max messages too high":     {"SQS_MAX_MESSAGES": "11"},
		"bad duration":              {"OUTBOX_POLL_INTERVAL": "soon"},
		"negative duration":         {"OUTBOX_LOCK_TTL": "-1s"},
		"backoff max below base":    {"OUTBOX_BACKOFF_BASE": "10s", "OUTBOX_BACKOFF_MAX": "1s"},
		"batch size zero":           {"OUTBOX_BATCH_SIZE": "0"},
		"pending attempts zero":     {"PENDING_REFERENCE_MAX_ATTEMPTS": "0"},
		"pending backoff inverted":  {"PENDING_REFERENCE_BACKOFF_BASE": "1m", "PENDING_REFERENCE_BACKOFF_MAX": "1s"},
		"body limit too small":      {"HTTP_MAX_BODY_BYTES": "100"},
		"body limit not a number":   {"HTTP_MAX_BODY_BYTES": "big"},
		"read timeout zero":         {"HTTP_READ_TIMEOUT": "0s"},
	}

	for name, overrides := range cases {
		env := minimal()
		for k, v := range overrides {
			env[k] = v
		}
		_, err := config.Load(getenv(env))
		assert.Error(t, err, name)
	}
}

func TestBlankValuesFallBackToDefaults(t *testing.T) {
	t.Parallel()

	env := minimal()
	env["AWS_REGION"] = "   "
	env["SQS_CONSUMER_NAME"] = ""

	cfg, err := config.Load(getenv(env))
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", cfg.AWS.Region)
	assert.Equal(t, "wager-transactions", cfg.SQS.ConsumerName)
}

func TestLoadReportsAllErrorsAtOnce(t *testing.T) {
	t.Parallel()

	env := minimal()
	env["DATABASE_URL"] = ""
	env["OIDC_AUDIENCE"] = ""
	env["SQS_MAX_MESSAGES"] = "0"

	_, err := config.Load(getenv(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	assert.Contains(t, err.Error(), "OIDC_AUDIENCE")
	assert.Contains(t, err.Error(), "SQS_MAX_MESSAGES")
}
