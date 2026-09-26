package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

const (
	namespace      = "wallet"
	unmatchedRoute = "unmatched"
)

var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}

type Metrics struct {
	registry        *prometheus.Registry
	wagers          *prometheus.CounterVec
	replays         *prometheus.CounterVec
	conflicts       *prometheus.CounterVec
	reconciliations *prometheus.CounterVec
	messages        *prometheus.CounterVec
	published       *prometheus.CounterVec
	retried         *prometheus.CounterVec
	outboxAge       prometheus.Gauge
	outboxPending   prometheus.Gauge
	requests        *prometheus.CounterVec
	latency         *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	f := promauto.With(registry)
	return &Metrics{
		registry: registry,
		wagers: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "wager_transactions_total",
			Help: "Wager transactions concluded, by kind, final status and failure code.",
		}, []string{"kind", "status", "code"}),
		replays: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "idempotent_replays_total",
			Help: "Submissions answered from an earlier identical request or message.",
		}, []string{"source"}),
		conflicts: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "concurrency_conflicts_total",
			Help: "Version or unique-constraint conflicts detected while committing.",
		}, []string{"operation"}),
		reconciliations: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "reconciliations_total",
			Help: "Wallet reconciliations, by whether the ledger matched the stored balance.",
		}, []string{"consistent"}),
		messages: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "messages_handled_total",
			Help: "Inbound queue messages, by handling outcome.",
		}, []string{"outcome"}),
		published: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "outbox_published_total",
			Help: "Outbox events published, by event type.",
		}, []string{"event_type"}),
		retried: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "outbox_retries_total",
			Help: "Outbox publish attempts that failed and were rescheduled, by event type.",
		}, []string{"event_type"}),
		outboxAge: f.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "outbox_oldest_pending_seconds",
			Help: "Age of the oldest unpublished outbox event.",
		}),
		outboxPending: f.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "outbox_pending_events",
			Help: "Unpublished outbox events.",
		}),
		requests: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: "http", Subsystem: "server", Name: "requests_total",
			Help: "HTTP requests, by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		latency: f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "http", Subsystem: "server", Name: "request_duration_seconds",
			Help: "HTTP request latency, by method and route pattern.", Buckets: latencyBuckets,
		}, []string{"method", "route"}),
	}
}

func (m *Metrics) WagerConcluded(kind wager.Kind, status wager.Status, code wager.FailureCode) {
	m.wagers.WithLabelValues(string(kind), string(status), string(code)).Inc()
}

func (m *Metrics) IdempotentReplay(source string) {
	m.replays.WithLabelValues(source).Inc()
}

func (m *Metrics) ConcurrencyConflict(operation string) {
	m.conflicts.WithLabelValues(operation).Inc()
}

func (m *Metrics) ReconciliationChecked(consistent bool) {
	m.reconciliations.WithLabelValues(strconv.FormatBool(consistent)).Inc()
}

func (m *Metrics) MessageHandled(outcome string) {
	m.messages.WithLabelValues(outcome).Inc()
}

func (m *Metrics) OutboxPublished(t event.Type) {
	m.published.WithLabelValues(string(t)).Inc()
}

func (m *Metrics) OutboxRetried(t event.Type) {
	m.retried.WithLabelValues(string(t)).Inc()
}

func (m *Metrics) OutboxLag(oldest time.Duration, pending int) {
	m.outboxAge.Set(oldest.Seconds())
	m.outboxPending.Set(float64(pending))
}

func (m *Metrics) ObserveRequest(method, route string, status int, elapsed time.Duration) {
	if route == "" {
		route = unmatchedRoute
	}
	if _, known := knownMethods[method]; !known {
		method = "other"
	}
	m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.latency.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

var knownMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {}, http.MethodPut: {},
	http.MethodPatch: {}, http.MethodDelete: {}, http.MethodOptions: {},
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
}
