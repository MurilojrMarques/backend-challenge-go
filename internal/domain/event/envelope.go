package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
)

var (
	ErrInvalidEvent   = errors.New("event: invalid event")
	ErrUnknownType    = errors.New("event: unknown event type")
	ErrInvalidPayload = errors.New("event: invalid payload")
)

type Type string

const (
	WagerTransactionProcessed        Type = "WagerTransactionProcessed"
	WagerTransactionRejected         Type = "WagerTransactionRejected"
	WagerTransactionPendingReference Type = "WagerTransactionPendingReference"
	WalletBalanceChanged             Type = "WalletBalanceChanged"
)

var versions = map[Type]int{
	WagerTransactionProcessed:        1,
	WagerTransactionRejected:         1,
	WagerTransactionPendingReference: 1,
	WalletBalanceChanged:             1,
}

func ParseType(s string) (Type, error) {
	t := Type(s)
	if !t.Valid() {
		return "", fmt.Errorf("%w: %q", ErrUnknownType, s)
	}
	return t, nil
}

func (t Type) Valid() bool {
	_, ok := versions[t]
	return ok
}

func (t Type) String() string {
	return string(t)
}

func (t Type) Version() int {
	return versions[t]
}

type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func moneyOf(m money.Money) Money {
	return Money{Amount: m.Amount(), Currency: m.Currency().Code()}
}

type Metadata struct {
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

func (m Metadata) validate() error {
	if m.CorrelationID == "" {
		return fmt.Errorf("%w: missing correlationId", ErrInvalidEvent)
	}
	if m.OccurredAt.IsZero() {
		return fmt.Errorf("%w: missing occurredAt", ErrInvalidEvent)
	}
	return nil
}

type Event struct {
	ID            uuid.UUID `json:"eventId"`
	Type          Type      `json:"eventType"`
	AggregateID   uuid.UUID `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          any       `json:"data"`
}

func newEvent(t Type, aggregateID uuid.UUID, m Metadata, data any) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	if aggregateID == uuid.Nil {
		return Event{}, fmt.Errorf("%w: missing aggregateId", ErrInvalidEvent)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	return Event{
		ID:            id,
		Type:          t,
		AggregateID:   aggregateID,
		CorrelationID: m.CorrelationID,
		CausationID:   m.CausationID,
		OccurredAt:    m.OccurredAt.UTC(),
		Version:       t.Version(),
		Data:          data,
	}, nil
}

func (e Event) Encode() ([]byte, error) {
	return json.Marshal(e)
}

type rawEvent struct {
	ID            uuid.UUID       `json:"eventId"`
	Type          Type            `json:"eventType"`
	AggregateID   uuid.UUID       `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	CausationID   string          `json:"causationId,omitempty"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

func Decode(b []byte) (Event, error) {
	var raw rawEvent
	if err := strictDecode(b, &raw); err != nil {
		return Event{}, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	if !raw.Type.Valid() {
		return Event{}, fmt.Errorf("%w: %q", ErrUnknownType, raw.Type)
	}
	if raw.Version != raw.Type.Version() {
		return Event{}, fmt.Errorf("%w: %s version %d not supported", ErrInvalidPayload, raw.Type, raw.Version)
	}

	data, err := decodeData(raw.Type, raw.Data)
	if err != nil {
		return Event{}, err
	}
	e := Event{
		ID:            raw.ID,
		Type:          raw.Type,
		AggregateID:   raw.AggregateID,
		CorrelationID: raw.CorrelationID,
		CausationID:   raw.CausationID,
		OccurredAt:    raw.OccurredAt.UTC(),
		Version:       raw.Version,
		Data:          data,
	}
	if e.ID == uuid.Nil || e.AggregateID == uuid.Nil || e.CorrelationID == "" || e.OccurredAt.IsZero() {
		return Event{}, fmt.Errorf("%w: incomplete envelope", ErrInvalidPayload)
	}
	return e, nil
}

func decodeData(t Type, raw json.RawMessage) (any, error) {
	switch t {
	case WagerTransactionProcessed:
		return decodeInto[WagerTransactionProcessedData](t, raw)
	case WagerTransactionRejected:
		return decodeInto[WagerTransactionRejectedData](t, raw)
	case WagerTransactionPendingReference:
		return decodeInto[WagerTransactionPendingReferenceData](t, raw)
	case WalletBalanceChanged:
		return decodeInto[WalletBalanceChangedData](t, raw)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, t)
	}
}

func decodeInto[T any](t Type, raw json.RawMessage) (T, error) {
	var data T
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return data, fmt.Errorf("%w: %s: missing data", ErrInvalidPayload, t)
	}
	if err := strictDecode(raw, &data); err != nil {
		var zero T
		return zero, fmt.Errorf("%w: %s: %w", ErrInvalidPayload, t, err)
	}
	return data, nil
}

func strictDecode(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after json value")
	}
	return nil
}
