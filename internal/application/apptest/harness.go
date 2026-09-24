package apptest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type RecordingMetrics struct {
	mu         sync.Mutex
	concluded  map[string]int
	replays    map[string]int
	conflicts  int
	reconciled map[bool]int
}

func NewRecordingMetrics() *RecordingMetrics {
	return &RecordingMetrics{concluded: map[string]int{}, replays: map[string]int{}, reconciled: map[bool]int{}}
}

func (m *RecordingMetrics) WagerConcluded(kind wager.Kind, status wager.Status, code wager.FailureCode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.concluded[string(kind)+"/"+string(status)+"/"+string(code)]++
}

func (m *RecordingMetrics) IdempotentReplay(source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replays[source]++
}

func (m *RecordingMetrics) ConcurrencyConflict(string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conflicts++
}

func (m *RecordingMetrics) ReconciliationChecked(consistent bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconciled[consistent]++
}

func (m *RecordingMetrics) Concluded(kind wager.Kind, status wager.Status, code wager.FailureCode) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.concluded[string(kind)+"/"+string(status)+"/"+string(code)]
}

func (m *RecordingMetrics) Replays(source string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.replays[source]
}

func (m *RecordingMetrics) Reconciled(consistent bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconciled[consistent]
}

var DefaultOptions = wagering.Options{
	ReferencePolicy:  wager.ReferencePolicy{MaxAttempts: 3, TTL: time.Hour},
	ReferenceBackoff: wagering.Backoff{Base: time.Second, Max: time.Minute},
}

type Harness struct {
	Store     *MemStore
	Clock     *FakeClock
	Metrics   *RecordingMetrics
	Wallets   *wallets.Service
	Wagers    *wagering.Service
	Internal  application.Principal
	ProviderA application.Principal
	ProviderB application.Principal
	Ctx       context.Context
}

func NewHarness(t *testing.T, opts ...func(*wagering.Options)) *Harness {
	t.Helper()
	options := DefaultOptions
	for _, apply := range opts {
		apply(&options)
	}
	store := NewMemStore()
	clock := NewFakeClock(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	metrics := NewRecordingMetrics()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Harness{
		Store:     store,
		Clock:     clock,
		Metrics:   metrics,
		Wallets:   wallets.NewService(store, clock, metrics, logger),
		Wagers:    wagering.NewService(store, clock, metrics, logger, options),
		Internal:  application.Principal{Subject: "wallet-service", Role: application.RoleInternal},
		ProviderA: application.Principal{Subject: "a", Role: application.RoleProvider, ProviderID: "provider-a"},
		ProviderB: application.Principal{Subject: "b", Role: application.RoleProvider, ProviderID: "provider-b"},
		Ctx:       context.Background(),
	}
}

func (h *Harness) OpenWallet(t *testing.T, amount string) wallets.View {
	t.Helper()
	view, err := h.Wallets.Open(h.Ctx, h.Internal, wallets.OpenCommand{
		PlayerID:       uuid.Must(uuid.NewV7()).String(),
		InitialBalance: application.MoneyInput{Amount: amount, Currency: "BRL"},
		CorrelationID:  "corr-open",
	})
	require.NoError(t, err)
	return view
}

type CommandOption func(*wagering.Command)

func WithReference(ref string) CommandOption {
	return func(c *wagering.Command) { c.ReferenceExternalTransactionID = ref }
}

func WithKey(key string) CommandOption {
	return func(c *wagering.Command) { c.IdempotencyKey = key }
}

func WithRound(round string) CommandOption {
	return func(c *wagering.Command) { c.RoundID = round }
}

func WithCurrency(code string) CommandOption {
	return func(c *wagering.Command) { c.Money.Currency = code }
}

func (h *Harness) Command(w wallets.View, kind, externalID, amount string, opts ...CommandOption) wagering.Command {
	cmd := wagering.Command{
		IdempotencyKey:        "provider-a:" + externalID,
		ProviderID:            "provider-a",
		ExternalTransactionID: externalID,
		PlayerID:              w.PlayerID.String(),
		WalletID:              w.ID.String(),
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 application.MoneyInput{Amount: amount, Currency: "BRL"},
		CorrelationID:         "corr-" + externalID,
	}
	for _, apply := range opts {
		apply(&cmd)
	}
	return cmd
}

func (h *Harness) Submit(t *testing.T, cmd wagering.Command) wagering.Result {
	t.Helper()
	res, err := h.Wagers.Submit(h.Ctx, h.ProviderA, cmd)
	require.NoError(t, err)
	return res
}

func (h *Harness) Balance(t *testing.T, walletID uuid.UUID) money.Money {
	t.Helper()
	view, err := h.Wallets.Get(h.Ctx, h.Internal, walletID)
	require.NoError(t, err)
	return view.Balance
}

func BRL(amount string) money.Money {
	return money.MustParse(amount, money.BRL)
}
