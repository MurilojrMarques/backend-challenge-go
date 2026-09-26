package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

func TestMetricsRecordApplicationSignals(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	m.WagerConcluded(wager.Bet, wager.Processed, "")
	m.WagerConcluded(wager.Bet, wager.Rejected, wager.InsufficientFunds)
	m.WagerConcluded(wager.Bet, wager.Rejected, wager.InsufficientFunds)
	m.IdempotentReplay("http")
	m.ConcurrencyConflict("wallet.save")
	m.ReconciliationChecked(true)
	m.ReconciliationChecked(false)
	m.MessageHandled("processed", 12*time.Millisecond)
	m.OutboxPublished(event.WagerTransactionProcessed)
	m.OutboxRetried(event.WalletBalanceChanged)
	m.OutboxLag(1500*time.Millisecond, 7)

	assert.Equal(t, 1.0, testutil.ToFloat64(m.wagers.WithLabelValues("BET", "PROCESSED", "")))
	assert.Equal(t, 2.0, testutil.ToFloat64(m.wagers.WithLabelValues("BET", "REJECTED", "INSUFFICIENT_FUNDS")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.replays.WithLabelValues("http")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.conflicts.WithLabelValues("wallet.save")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.reconciliations.WithLabelValues("true")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.reconciliations.WithLabelValues("false")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.messages.WithLabelValues("processed")))
	assert.Equal(t, 1, testutil.CollectAndCount(m.messageLatency))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.published.WithLabelValues("WagerTransactionProcessed")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.retried.WithLabelValues("WalletBalanceChanged")))
	assert.Equal(t, 1.5, testutil.ToFloat64(m.outboxAge))
	assert.Equal(t, 7.0, testutil.ToFloat64(m.outboxPending))

	m.OutboxLag(0, 0)
	assert.Equal(t, 0.0, testutil.ToFloat64(m.outboxAge))
	assert.Equal(t, 0.0, testutil.ToFloat64(m.outboxPending))
}

func TestMetricsObserveHTTPRequests(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	m.ObserveRequest(http.MethodPost, "/wallets", http.StatusCreated, 12*time.Millisecond)
	m.ObserveRequest(http.MethodPost, "/wallets", http.StatusCreated, 3*time.Millisecond)
	m.ObserveRequest(http.MethodGet, "", http.StatusNotFound, time.Millisecond)

	assert.Equal(t, 2.0, testutil.ToFloat64(m.requests.WithLabelValues("POST", "/wallets", "201")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.requests.WithLabelValues("GET", unmatchedRoute, "404")))
	assert.Equal(t, 2, testutil.CollectAndCount(m.latency))
}

func TestHandlerExposesPrivateRegistry(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.MessageHandled("processed", time.Millisecond)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `wallet_messages_handled_total{outcome="processed"} 1`)
	assert.Contains(t, body, "go_goroutines")
	assert.Contains(t, body, "process_start_time_seconds")

	other := NewMetrics()
	rec = httptest.NewRecorder()
	other.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.NotContains(t, rec.Body.String(), `outcome="processed"`, "each instance owns its registry")
}

func TestModuleProvidesPorts(t *testing.T) {
	t.Parallel()
	require.NoError(t, fx.ValidateApp(Module, fx.Invoke(func(p struct {
		fx.In
		Handler http.Handler `name:"metrics"`
	}) {
	})))
}
