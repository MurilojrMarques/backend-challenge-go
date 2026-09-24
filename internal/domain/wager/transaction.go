package wager

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
)

var (
	ErrInvalidTransaction     = errors.New("wager: invalid transaction")
	ErrKindNotAllowed         = errors.New("wager: kind not allowed for external operations")
	ErrInvalidAmountForKind   = errors.New("wager: amount not allowed for kind")
	ErrInvalidReference       = errors.New("wager: invalid reference")
	ErrInvalidTransition      = errors.New("wager: invalid status transition")
	ErrInvalidFailureCode     = errors.New("wager: invalid failure code")
	ErrIdempotencyKeyMismatch = errors.New("wager: operation already registered with another idempotency key")
	ErrPayloadConflict        = errors.New("wager: idempotency key reused with a different payload")
)

const MaxFieldLength = 128

type External struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
}

func (e External) HasReference() bool {
	return e.ReferenceExternalTransactionID != ""
}

func (e External) validate(kind Kind) error {
	required := []struct{ name, value string }{
		{"providerId", e.ProviderID},
		{"externalTransactionId", e.ExternalTransactionID},
		{"idempotencyKey", e.IdempotencyKey},
		{"payloadHash", e.PayloadHash},
		{"roundId", e.RoundID},
		{"gameId", e.GameID},
	}
	for _, f := range required {
		if !cleanField(f.value) {
			return fmt.Errorf("%w: %s must be non-empty, unpadded and at most %d bytes", ErrInvalidTransaction, f.name, MaxFieldLength)
		}
	}

	if kind.IsReversal() && !e.HasReference() {
		return fmt.Errorf("%w: %s requires referenceExternalTransactionId", ErrInvalidReference, kind)
	}
	if !e.HasReference() {
		return nil
	}
	if !kind.AllowsReference() {
		return fmt.Errorf("%w: %s does not accept a reference", ErrInvalidReference, kind)
	}
	if !cleanField(e.ReferenceExternalTransactionID) {
		return fmt.Errorf("%w: referenceExternalTransactionId must be unpadded and at most %d bytes", ErrInvalidReference, MaxFieldLength)
	}
	if e.ReferenceExternalTransactionID == e.ExternalTransactionID {
		return fmt.Errorf("%w: transaction cannot reference itself", ErrInvalidReference)
	}
	return nil
}

func cleanField(s string) bool {
	return s != "" && len(s) <= MaxFieldLength && strings.TrimSpace(s) == s
}

type Transaction struct {
	id                  uuid.UUID
	kind                Kind
	status              Status
	walletID            uuid.UUID
	playerID            uuid.UUID
	amount              money.Money
	external            *External
	resolvedReferenceID uuid.UUID
	failureCode         FailureCode
	balanceAfter        money.Money
	referenceAttempts   int
	createdAt           time.Time
	updatedAt           time.Time
	completedAt         time.Time
}

type ExternalParams struct {
	ID       uuid.UUID
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Kind     Kind
	Amount   money.Money
	External External
	Now      time.Time
}

func NewExternal(p ExternalParams) (*Transaction, error) {
	if !p.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, p.Kind)
	}
	if !p.Kind.External() {
		return nil, fmt.Errorf("%w: %s", ErrKindNotAllowed, p.Kind)
	}
	if err := validIdentity(p.ID, p.WalletID, p.PlayerID, p.Now); err != nil {
		return nil, err
	}
	if err := validAmount(p.Kind, p.Amount); err != nil {
		return nil, err
	}
	if err := p.External.validate(p.Kind); err != nil {
		return nil, err
	}

	now := p.Now.UTC()
	external := p.External
	return &Transaction{
		id:        p.ID,
		kind:      p.Kind,
		status:    Pending,
		walletID:  p.WalletID,
		playerID:  p.PlayerID,
		amount:    p.Amount,
		external:  &external,
		createdAt: now,
		updatedAt: now,
	}, nil
}

type OpeningParams struct {
	ID       uuid.UUID
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Amount   money.Money
	Now      time.Time
}

func NewOpening(p OpeningParams) (*Transaction, error) {
	if err := validIdentity(p.ID, p.WalletID, p.PlayerID, p.Now); err != nil {
		return nil, err
	}
	if err := validAmount(Opening, p.Amount); err != nil {
		return nil, err
	}

	now := p.Now.UTC()
	return &Transaction{
		id:           p.ID,
		kind:         Opening,
		status:       Processed,
		walletID:     p.WalletID,
		playerID:     p.PlayerID,
		amount:       p.Amount,
		balanceAfter: p.Amount,
		createdAt:    now,
		updatedAt:    now,
		completedAt:  now,
	}, nil
}

func validIdentity(id, walletID, playerID uuid.UUID, now time.Time) error {
	if id == uuid.Nil || walletID == uuid.Nil || playerID == uuid.Nil {
		return fmt.Errorf("%w: missing identifiers", ErrInvalidTransaction)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: missing timestamp", ErrInvalidTransaction)
	}
	return nil
}

func validAmount(kind Kind, amount money.Money) error {
	if amount.Validate() != nil {
		return fmt.Errorf("%w: uninitialized amount", ErrInvalidTransaction)
	}
	if amount.IsNegative() {
		return fmt.Errorf("%w: %s cannot be negative", ErrInvalidAmountForKind, kind)
	}
	if kind.RequiresZeroAmount() && !amount.IsZero() {
		return fmt.Errorf("%w: %s requires amount 0.00, got %s", ErrInvalidAmountForKind, kind, amount.Amount())
	}
	if !kind.RequiresZeroAmount() && !amount.IsPositive() {
		return fmt.Errorf("%w: %s requires a positive amount", ErrInvalidAmountForKind, kind)
	}
	return nil
}

func (t *Transaction) AwaitReference(now time.Time) error {
	if !t.HasReference() {
		return fmt.Errorf("%w: %s has no reference to wait for", ErrInvalidReference, t.kind)
	}
	if t.status != PendingReference {
		if err := t.ensureCanTransitionTo(PendingReference); err != nil {
			return err
		}
	}
	if now.IsZero() {
		return fmt.Errorf("%w: missing timestamp", ErrInvalidTransaction)
	}
	t.status = PendingReference
	t.referenceAttempts++
	t.touch(now)
	return nil
}

func (t *Transaction) ResolveReference(referenceID uuid.UUID, now time.Time) error {
	if !t.HasReference() {
		return fmt.Errorf("%w: %s has no reference to resolve", ErrInvalidReference, t.kind)
	}
	if referenceID == uuid.Nil || referenceID == t.id {
		return fmt.Errorf("%w: invalid reference id", ErrInvalidReference)
	}
	if t.status.Terminal() {
		return fmt.Errorf("%w: %s is terminal", ErrInvalidTransition, t.status)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: missing timestamp", ErrInvalidTransaction)
	}
	t.resolvedReferenceID = referenceID
	t.touch(now)
	return nil
}

func (t *Transaction) MarkProcessed(balanceAfter money.Money, now time.Time) error {
	if err := t.ensureCanTransitionTo(Processed); err != nil {
		return err
	}
	if err := validResultBalance(balanceAfter, t.amount); err != nil {
		return err
	}
	if t.HasReference() && t.resolvedReferenceID == uuid.Nil {
		return fmt.Errorf("%w: reference not resolved", ErrInvalidReference)
	}
	if err := t.transition(Processed, now); err != nil {
		return err
	}
	t.balanceAfter = balanceAfter
	t.completedAt = t.updatedAt
	return nil
}

func (t *Transaction) Reject(code FailureCode, observed money.Money, now time.Time) error {
	if err := t.ensureCanTransitionTo(Rejected); err != nil {
		return err
	}
	if !code.Rejection() {
		return fmt.Errorf("%w: %q is not a rejection code", ErrInvalidFailureCode, code)
	}
	if observed.Validate() == nil {
		if err := validResultBalance(observed, t.amount); err != nil {
			return err
		}
	}
	if err := t.transition(Rejected, now); err != nil {
		return err
	}
	t.failureCode = code
	t.balanceAfter = observed
	t.completedAt = t.updatedAt
	return nil
}

func validResultBalance(balance, amount money.Money) error {
	if balance.Validate() != nil || balance.IsNegative() {
		return fmt.Errorf("%w: result balance must be a non-negative amount", ErrInvalidTransaction)
	}
	if balance.Currency() != amount.Currency() {
		return fmt.Errorf("%w: result balance currency %s differs from %s", ErrInvalidTransaction, balance.Currency(), amount.Currency())
	}
	return nil
}

func (t *Transaction) Fail(now time.Time) error {
	if err := t.transition(Failed, now); err != nil {
		return err
	}
	t.failureCode = PermanentFailure
	t.completedAt = t.updatedAt
	return nil
}

func (t *Transaction) transition(to Status, now time.Time) error {
	if err := t.ensureCanTransitionTo(to); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: missing timestamp", ErrInvalidTransaction)
	}
	t.status = to
	t.touch(now)
	return nil
}

func (t *Transaction) ensureCanTransitionTo(to Status) error {
	if !t.status.CanTransitionTo(to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.status, to)
	}
	return nil
}

func (t *Transaction) touch(now time.Time) {
	t.updatedAt = now.UTC()
}

func (t *Transaction) CheckReplay(idempotencyKey, payloadHash string) error {
	if t.external == nil {
		return fmt.Errorf("%w: internal transactions cannot be replayed", ErrInvalidTransaction)
	}
	if t.external.IdempotencyKey != idempotencyKey {
		return ErrIdempotencyKeyMismatch
	}
	if t.external.PayloadHash != payloadHash {
		return ErrPayloadConflict
	}
	return nil
}

func (t *Transaction) HasReference() bool {
	return t.external != nil && t.external.HasReference()
}

func (t *Transaction) ReferenceKey() (providerID, externalTransactionID string, ok bool) {
	if !t.HasReference() {
		return "", "", false
	}
	return t.external.ProviderID, t.external.ReferenceExternalTransactionID, true
}

func (t *Transaction) ID() uuid.UUID          { return t.id }
func (t *Transaction) Kind() Kind             { return t.kind }
func (t *Transaction) Status() Status         { return t.status }
func (t *Transaction) WalletID() uuid.UUID    { return t.walletID }
func (t *Transaction) PlayerID() uuid.UUID    { return t.playerID }
func (t *Transaction) Amount() money.Money    { return t.amount }
func (t *Transaction) ReferenceAttempts() int { return t.referenceAttempts }
func (t *Transaction) CreatedAt() time.Time   { return t.createdAt }
func (t *Transaction) UpdatedAt() time.Time   { return t.updatedAt }
func (t *Transaction) Terminal() bool         { return t.status.Terminal() }

func (t *Transaction) External() (External, bool) {
	if t.external == nil {
		return External{}, false
	}
	return *t.external, true
}

func (t *Transaction) ResolvedReferenceID() (uuid.UUID, bool) {
	return t.resolvedReferenceID, t.resolvedReferenceID != uuid.Nil
}

func (t *Transaction) FailureCode() (FailureCode, bool) {
	return t.failureCode, t.failureCode != ""
}

func (t *Transaction) BalanceAfter() (money.Money, bool) {
	return t.balanceAfter, t.balanceAfter.Validate() == nil
}

func (t *Transaction) CompletedAt() (time.Time, bool) {
	return t.completedAt, !t.completedAt.IsZero()
}

type Snapshot struct {
	ID                  uuid.UUID
	Kind                Kind
	Status              Status
	WalletID            uuid.UUID
	PlayerID            uuid.UUID
	Amount              money.Money
	External            *External
	ResolvedReferenceID uuid.UUID
	FailureCode         FailureCode
	BalanceAfter        money.Money
	ReferenceAttempts   int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CompletedAt         time.Time
}

func (t *Transaction) Snapshot() Snapshot {
	var external *External
	if t.external != nil {
		copied := *t.external
		external = &copied
	}
	return Snapshot{
		ID:                  t.id,
		Kind:                t.kind,
		Status:              t.status,
		WalletID:            t.walletID,
		PlayerID:            t.playerID,
		Amount:              t.amount,
		External:            external,
		ResolvedReferenceID: t.resolvedReferenceID,
		FailureCode:         t.failureCode,
		BalanceAfter:        t.balanceAfter,
		ReferenceAttempts:   t.referenceAttempts,
		CreatedAt:           t.createdAt,
		UpdatedAt:           t.updatedAt,
		CompletedAt:         t.completedAt,
	}
}

func Rehydrate(s Snapshot) (*Transaction, error) {
	if err := s.validShape(); err != nil {
		return nil, err
	}
	if err := s.validState(); err != nil {
		return nil, err
	}
	if err := s.validTimes(); err != nil {
		return nil, err
	}

	var external *External
	if s.External != nil {
		copied := *s.External
		external = &copied
	}
	return &Transaction{
		id:                  s.ID,
		kind:                s.Kind,
		status:              s.Status,
		walletID:            s.WalletID,
		playerID:            s.PlayerID,
		amount:              s.Amount,
		external:            external,
		resolvedReferenceID: s.ResolvedReferenceID,
		failureCode:         s.FailureCode,
		balanceAfter:        s.BalanceAfter,
		referenceAttempts:   s.ReferenceAttempts,
		createdAt:           s.CreatedAt.UTC(),
		updatedAt:           s.UpdatedAt.UTC(),
		completedAt:         s.CompletedAt.UTC(),
	}, nil
}

func (s Snapshot) validShape() error {
	if !s.Kind.Valid() || !s.Status.Valid() {
		return fmt.Errorf("%w: unknown kind or status", ErrInvalidTransaction)
	}
	if err := validIdentity(s.ID, s.WalletID, s.PlayerID, s.CreatedAt); err != nil {
		return err
	}
	if err := validAmount(s.Kind, s.Amount); err != nil {
		return err
	}
	if s.Kind.External() != (s.External != nil) {
		return fmt.Errorf("%w: external metadata must be present exactly for external kinds", ErrInvalidTransaction)
	}
	if s.External != nil {
		return s.External.validate(s.Kind)
	}
	return nil
}

func (s Snapshot) validState() error {
	hasReference := s.External != nil && s.External.HasReference()
	if s.ReferenceAttempts < 0 {
		return fmt.Errorf("%w: negative reference attempts", ErrInvalidTransaction)
	}
	if !hasReference && (s.Status == PendingReference || s.ResolvedReferenceID != uuid.Nil || s.ReferenceAttempts > 0) {
		return fmt.Errorf("%w: reference state without a reference", ErrInvalidReference)
	}
	if s.ResolvedReferenceID == s.ID && s.ID != uuid.Nil {
		return fmt.Errorf("%w: transaction cannot reference itself", ErrInvalidReference)
	}

	hasBalance := s.BalanceAfter.Validate() == nil
	switch s.Status {
	case Processed:
		if err := validResultBalance(s.BalanceAfter, s.Amount); err != nil {
			return err
		}
		if s.FailureCode != "" {
			return fmt.Errorf("%w: processed transaction cannot carry a failure code", ErrInvalidTransaction)
		}
		if hasReference && s.ResolvedReferenceID == uuid.Nil {
			return fmt.Errorf("%w: processed transaction with unresolved reference", ErrInvalidReference)
		}
	case Rejected:
		if !s.FailureCode.Rejection() {
			return fmt.Errorf("%w: rejected transaction requires a rejection code", ErrInvalidFailureCode)
		}
		if hasBalance {
			if err := validResultBalance(s.BalanceAfter, s.Amount); err != nil {
				return err
			}
		}
	case Failed:
		if s.FailureCode != PermanentFailure {
			return fmt.Errorf("%w: failed transaction requires %s", ErrInvalidFailureCode, PermanentFailure)
		}
		if hasBalance {
			return fmt.Errorf("%w: failed transaction cannot carry a result balance", ErrInvalidTransaction)
		}
	default:
		if s.FailureCode != "" || hasBalance {
			return fmt.Errorf("%w: non-terminal transaction cannot carry a result", ErrInvalidTransaction)
		}
	}
	return nil
}

func (s Snapshot) validTimes() error {
	if s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return fmt.Errorf("%w: invalid timestamps", ErrInvalidTransaction)
	}
	if s.Status.Terminal() != !s.CompletedAt.IsZero() {
		return fmt.Errorf("%w: completedAt must be set exactly for terminal statuses", ErrInvalidTransaction)
	}
	if !s.CompletedAt.IsZero() && s.CompletedAt.Before(s.CreatedAt) {
		return fmt.Errorf("%w: completedAt precedes createdAt", ErrInvalidTransaction)
	}
	return nil
}
