package application

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
)

type MoneyInput struct {
	Amount   string
	Currency string
}

func (m MoneyInput) Parse() (money.Money, error) {
	currency, err := money.ParseCurrency(strings.TrimSpace(m.Currency))
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	amount, err := money.Parse(m.Amount, currency)
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return amount, nil
}

func ParseUUID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a non-nil uuid", ErrInvalidInput, field)
	}
	return id, nil
}

func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

func CorrelationID(given string, fallback uuid.UUID) string {
	if given != "" {
		return given
	}
	return fallback.String()
}
