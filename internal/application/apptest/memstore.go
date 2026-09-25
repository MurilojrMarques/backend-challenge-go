package apptest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/event"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

type MemStore struct {
	mu       sync.Mutex
	wallets  map[uuid.UUID]wallet.Snapshot
	wagers   map[uuid.UUID]wager.Snapshot
	schedule map[uuid.UUID]time.Time
	ledger   []wallet.LedgerEntry
	inbox    map[string]application.InboxRecord
	outbox   map[uuid.UUID]application.OutboxRecord
	failNext error
}

func NewMemStore() *MemStore {
	return &MemStore{
		wallets:  map[uuid.UUID]wallet.Snapshot{},
		wagers:   map[uuid.UUID]wager.Snapshot{},
		schedule: map[uuid.UUID]time.Time{},
		inbox:    map[string]application.InboxRecord{},
		outbox:   map[uuid.UUID]application.OutboxRecord{},
	}
}

type memTx struct {
	wallets  map[uuid.UUID]wallet.Snapshot
	wagers   map[uuid.UUID]wager.Snapshot
	schedule map[uuid.UUID]time.Time
	ledger   []wallet.LedgerEntry
	inbox    map[string]application.InboxRecord
	outbox   map[uuid.UUID]application.OutboxRecord
}

func (s *MemStore) Do(ctx context.Context, _ application.TxOptions, fn func(context.Context, application.Repos) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return err
	}
	tx := &memTx{
		wallets:  cloneMap(s.wallets),
		wagers:   cloneMap(s.wagers),
		schedule: cloneMap(s.schedule),
		ledger:   append([]wallet.LedgerEntry(nil), s.ledger...),
		inbox:    cloneMap(s.inbox),
		outbox:   cloneMap(s.outbox),
	}
	repos := application.Repos{
		Wallets: &memWallets{tx},
		Wagers:  &memWagers{tx},
		Ledger:  &memLedger{tx},
		Inbox:   &memInbox{tx},
		Outbox:  &memOutbox{tx},
	}
	if err := fn(ctx, repos); err != nil {
		return err
	}
	s.wallets, s.wagers, s.schedule, s.ledger, s.inbox, s.outbox =
		tx.wallets, tx.wagers, tx.schedule, tx.ledger, tx.inbox, tx.outbox
	return nil
}

func (s *MemStore) FailNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = err
}

func (s *MemStore) Wallet(id uuid.UUID) wallet.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wallets[id]
}

func (s *MemStore) WalletCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.wallets)
}

func (s *MemStore) TamperBalance(id uuid.UUID, balance money.Money) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.wallets[id]
	w.Balance = balance
	s.wallets[id] = w
}

func (s *MemStore) Wager(id uuid.UUID) wager.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wagers[id]
}

func (s *MemStore) WagerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.wagers)
}

func (s *MemStore) LedgerEntries(walletID uuid.UUID) []wallet.LedgerEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []wallet.LedgerEntry
	for _, e := range s.ledger {
		if e.WalletID() == walletID {
			out = append(out, e)
		}
	}
	return out
}

func (s *MemStore) OutboxByType(t event.Type) []application.OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []application.OutboxRecord
	for _, rec := range s.outbox {
		if rec.EventType == t {
			out = append(out, rec)
		}
	}
	return out
}

func (s *MemStore) OutboxCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.outbox)
}

func (s *MemStore) Inbox(consumerName, messageID string) (application.InboxRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.inbox[inboxKey(consumerName, messageID)]
	return rec, ok
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func conflict(constraint string) error {
	return &application.ConflictError{Constraint: constraint}
}

type memWallets struct{ tx *memTx }

func (r *memWallets) Create(_ context.Context, w *wallet.Wallet) error {
	s := w.Snapshot()
	if _, ok := r.tx.wallets[s.ID]; ok {
		return conflict("wallets_pkey")
	}
	for _, other := range r.tx.wallets {
		if other.PlayerID == s.PlayerID && other.Balance.Currency() == s.Balance.Currency() {
			return conflict("wallets_player_currency_key")
		}
	}
	r.tx.wallets[s.ID] = s
	return nil
}

func (r *memWallets) Get(_ context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	s, ok := r.tx.wallets[id]
	if !ok {
		return nil, application.ErrNotFound
	}
	return wallet.Rehydrate(s)
}

func (r *memWallets) GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return r.Get(ctx, id)
}

func (r *memWallets) Save(_ context.Context, w *wallet.Wallet, expectedVersion int64) error {
	s := w.Snapshot()
	current, ok := r.tx.wallets[s.ID]
	if !ok || current.Version != expectedVersion {
		return application.ErrConcurrentModification
	}
	if s.Balance.IsNegative() {
		return fmt.Errorf("%w: wallets_balance_non_negative", application.ErrIntegrity)
	}
	r.tx.wallets[s.ID] = s
	return nil
}

type memWagers struct{ tx *memTx }

func (r *memWagers) Insert(_ context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error {
	s := tx.Snapshot()
	if _, ok := r.tx.wagers[s.ID]; ok {
		return conflict("wager_transactions_pkey")
	}
	if s.External != nil {
		for _, other := range r.tx.wagers {
			if other.External == nil {
				continue
			}
			if other.External.ProviderID == s.External.ProviderID && other.External.IdempotencyKey == s.External.IdempotencyKey {
				return conflict("wager_transactions_idempotency_key")
			}
			if other.External.ProviderID == s.External.ProviderID && other.External.ExternalTransactionID == s.External.ExternalTransactionID {
				return conflict("wager_transactions_provider_external_key")
			}
		}
	} else {
		for _, other := range r.tx.wagers {
			if other.Kind == wager.Opening && other.WalletID == s.WalletID {
				return conflict("wager_transactions_one_opening_per_wallet")
			}
		}
	}
	if err := r.checkSchedule(s, nextAttemptAt); err != nil {
		return err
	}
	if err := r.checkSingleReversal(s); err != nil {
		return err
	}
	r.tx.wagers[s.ID] = s
	r.tx.schedule[s.ID] = nextAttemptAt
	return nil
}

func (r *memWagers) Update(_ context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error {
	s := tx.Snapshot()
	current, ok := r.tx.wagers[s.ID]
	if !ok || current.Status.Terminal() {
		return application.ErrConcurrentModification
	}
	if err := r.checkSchedule(s, nextAttemptAt); err != nil {
		return err
	}
	if err := r.checkSingleReversal(s); err != nil {
		return err
	}
	r.tx.wagers[s.ID] = s
	r.tx.schedule[s.ID] = nextAttemptAt
	return nil
}

func (r *memWagers) checkSchedule(s wager.Snapshot, nextAttemptAt time.Time) error {
	if (s.Status == wager.PendingReference) != !nextAttemptAt.IsZero() {
		return fmt.Errorf("%w: wager_pending_reference_is_scheduled", application.ErrIntegrity)
	}
	return nil
}

func (r *memWagers) checkSingleReversal(s wager.Snapshot) error {
	if s.Status != wager.Processed || !s.Kind.IsReversal() {
		return nil
	}
	for id, other := range r.tx.wagers {
		if id != s.ID && other.Status == wager.Processed && other.Kind.IsReversal() && other.ResolvedReferenceID == s.ResolvedReferenceID {
			return conflict("wager_transactions_one_reversal_per_reference")
		}
	}
	return nil
}

func (r *memWagers) Get(_ context.Context, id uuid.UUID) (*wager.Transaction, error) {
	s, ok := r.tx.wagers[id]
	if !ok {
		return nil, application.ErrNotFound
	}
	return wager.Rehydrate(s)
}

func (r *memWagers) GetByIdempotencyKey(_ context.Context, providerID, key string) (*wager.Transaction, error) {
	for _, s := range r.tx.wagers {
		if s.External != nil && s.External.ProviderID == providerID && s.External.IdempotencyKey == key {
			return wager.Rehydrate(s)
		}
	}
	return nil, application.ErrNotFound
}

func (r *memWagers) GetByExternalID(_ context.Context, providerID, externalID string) (*wager.Transaction, error) {
	for _, s := range r.tx.wagers {
		if s.External != nil && s.External.ProviderID == providerID && s.External.ExternalTransactionID == externalID {
			return wager.Rehydrate(s)
		}
	}
	return nil, application.ErrNotFound
}

func (r *memWagers) HasSuccessfulReversal(_ context.Context, referenceID uuid.UUID) (bool, error) {
	for _, s := range r.tx.wagers {
		if s.Status == wager.Processed && s.Kind.IsReversal() && s.ResolvedReferenceID == referenceID {
			return true, nil
		}
	}
	return false, nil
}

func (r *memWagers) ListDuePendingReferences(_ context.Context, now time.Time, limit int) ([]*wager.Transaction, error) {
	var due []wager.Snapshot
	for id, s := range r.tx.wagers {
		if s.Status == wager.PendingReference && !r.tx.schedule[id].After(now) {
			due = append(due, s)
		}
	}
	sort.Slice(due, func(i, j int) bool { return r.tx.schedule[due[i].ID].Before(r.tx.schedule[due[j].ID]) })
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]*wager.Transaction, 0, len(due))
	for _, s := range due {
		tx, err := wager.Rehydrate(s)
		if err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, nil
}

type memLedger struct{ tx *memTx }

func (r *memLedger) Append(_ context.Context, e wallet.LedgerEntry) error {
	for _, other := range r.tx.ledger {
		if other.WalletID() == e.WalletID() && other.TransactionID() == e.TransactionID() {
			return conflict("ledger_wallet_transaction_key")
		}
	}
	r.tx.ledger = append(r.tx.ledger, e)
	return nil
}

func (r *memLedger) List(_ context.Context, walletID uuid.UUID, after application.LedgerCursor, limit int) ([]wallet.LedgerEntry, error) {
	var out []wallet.LedgerEntry
	for _, e := range r.tx.ledger {
		if e.WalletID() != walletID {
			continue
		}
		if !after.IsZero() && !(e.CreatedAt().After(after.CreatedAt) || (e.CreatedAt().Equal(after.CreatedAt) && e.ID().String() > after.ID.String())) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt().Equal(out[j].CreatedAt()) {
			return out[i].ID().String() < out[j].ID().String()
		}
		return out[i].CreatedAt().Before(out[j].CreatedAt())
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *memLedger) Totals(_ context.Context, walletID uuid.UUID) (application.LedgerTotals, error) {
	var t application.LedgerTotals
	for _, e := range r.tx.ledger {
		if e.WalletID() != walletID {
			continue
		}
		t.Entries++
		if e.Direction() == wallet.Credit {
			t.CreditUnits += e.Amount().Units()
		} else {
			t.DebitUnits += e.Amount().Units()
		}
	}
	return t, nil
}

type memInbox struct{ tx *memTx }

func inboxKey(consumer, message string) string {
	return consumer + "\x00" + message
}

func (r *memInbox) Insert(_ context.Context, rec application.InboxRecord) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	key := inboxKey(rec.ConsumerName, rec.MessageID)
	if _, ok := r.tx.inbox[key]; ok {
		return conflict("inbox_messages_pkey")
	}
	r.tx.inbox[key] = rec
	return nil
}

func (r *memInbox) Get(_ context.Context, consumer, message string) (application.InboxRecord, error) {
	rec, ok := r.tx.inbox[inboxKey(consumer, message)]
	if !ok {
		return application.InboxRecord{}, application.ErrNotFound
	}
	return rec, nil
}

func (r *memInbox) Complete(_ context.Context, consumer, message string, transactionID uuid.UUID, at time.Time) error {
	key := inboxKey(consumer, message)
	rec, ok := r.tx.inbox[key]
	if !ok || rec.Completed() {
		return application.ErrConcurrentModification
	}
	if transactionID != uuid.Nil {
		rec.TransactionID = transactionID
	}
	rec.CompletedAt = at
	r.tx.inbox[key] = rec
	return nil
}

type memOutbox struct{ tx *memTx }

func (r *memOutbox) Append(_ context.Context, events ...event.Event) error {
	for _, e := range events {
		if _, ok := r.tx.outbox[e.ID]; ok {
			return conflict("outbox_events_pkey")
		}
		payload, err := e.Encode()
		if err != nil {
			return err
		}
		r.tx.outbox[e.ID] = application.OutboxRecord{
			EventID:       e.ID,
			EventType:     e.Type,
			AggregateID:   e.AggregateID,
			Payload:       payload,
			OccurredAt:    e.OccurredAt,
			CreatedAt:     e.OccurredAt,
			NextAttemptAt: e.OccurredAt,
		}
	}
	return nil
}

func (r *memOutbox) Claim(_ context.Context, publisher string, now time.Time, lease time.Duration, limit int) ([]application.OutboxRecord, error) {
	pending := make([]application.OutboxRecord, 0, len(r.tx.outbox))
	for _, rec := range r.tx.outbox {
		if !rec.Published() {
			pending = append(pending, rec)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return before(pending[i], pending[j]) })

	var out []application.OutboxRecord
	for _, rec := range pending {
		if len(out) == limit {
			break
		}
		if rec.NextAttemptAt.After(now) || (rec.Locked() && rec.LockedUntil.After(now)) || r.olderPending(pending, rec) {
			continue
		}
		rec.LockedBy, rec.LockedUntil = publisher, now.Add(lease)
		r.tx.outbox[rec.EventID] = rec
		out = append(out, rec)
	}
	return out, nil
}

func (r *memOutbox) olderPending(pending []application.OutboxRecord, rec application.OutboxRecord) bool {
	for _, other := range pending {
		if other.AggregateID == rec.AggregateID && other.EventID != rec.EventID && before(other, rec) {
			return true
		}
	}
	return false
}

func before(a, b application.OutboxRecord) bool {
	if a.OccurredAt.Equal(b.OccurredAt) {
		return a.EventID.String() < b.EventID.String()
	}
	return a.OccurredAt.Before(b.OccurredAt)
}

func (r *memOutbox) MarkPublished(_ context.Context, eventID uuid.UUID, publisher string, at time.Time) error {
	rec, ok := r.tx.outbox[eventID]
	if !ok || rec.LockedBy != publisher || rec.Published() {
		return application.ErrConcurrentModification
	}
	rec.PublishedAt, rec.LockedBy, rec.LockedUntil, rec.LastError = at, "", time.Time{}, ""
	r.tx.outbox[eventID] = rec
	return nil
}

func (r *memOutbox) Release(_ context.Context, eventID uuid.UUID, publisher string, nextAttemptAt time.Time, lastError string) error {
	rec, ok := r.tx.outbox[eventID]
	if !ok || rec.LockedBy != publisher || rec.Published() {
		return application.ErrConcurrentModification
	}
	rec.Attempts++
	rec.NextAttemptAt, rec.LastError, rec.LockedBy, rec.LockedUntil = nextAttemptAt, lastError, "", time.Time{}
	r.tx.outbox[eventID] = rec
	return nil
}

func (r *memOutbox) OldestPending(context.Context) (time.Time, int, error) {
	var (
		oldest time.Time
		count  int
	)
	for _, rec := range r.tx.outbox {
		if rec.Published() {
			continue
		}
		count++
		if oldest.IsZero() || rec.OccurredAt.Before(oldest) {
			oldest = rec.OccurredAt
		}
	}
	return oldest, count, nil
}
