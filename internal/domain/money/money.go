package money

import (
	"fmt"
	"math"
)

const (
	MinUnits int64 = math.MinInt64 + 1
	MaxUnits int64 = math.MaxInt64
)

type Money struct {
	units    int64
	currency Currency
}

func FromUnits(units int64, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrInvalidCurrency
	}
	if units < MinUnits {
		return Money{}, fmt.Errorf("%w: %d is below the representable range", ErrOverflow, units)
	}
	return Money{units: units, currency: currency}, nil
}

func Zero(currency Currency) Money {
	return Money{currency: currency}
}

func (m Money) Units() int64 {
	return m.units
}

func (m Money) Currency() Currency {
	return m.currency
}

func (m Money) Validate() error {
	if m.currency.IsZero() {
		return ErrUninitialized
	}
	return nil
}

func (m Money) IsZero() bool {
	return m.units == 0
}

func (m Money) IsPositive() bool {
	return m.units > 0
}

func (m Money) IsNegative() bool {
	return m.units < 0
}

func (m Money) Sign() int {
	switch {
	case m.units > 0:
		return 1
	case m.units < 0:
		return -1
	default:
		return 0
	}
}

func (m Money) SameCurrency(o Money) bool {
	return m.currency == o.currency
}

func (m Money) Equal(o Money) bool {
	return m == o
}

func (m Money) Cmp(o Money) (int, error) {
	if err := m.compatible(o); err != nil {
		return 0, err
	}
	switch {
	case m.units < o.units:
		return -1, nil
	case m.units > o.units:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Add(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	sum, err := addUnits(m.units, o.units)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %s + %s", err, m, o)
	}
	return Money{units: sum, currency: m.currency}, nil
}

func (m Money) Sub(o Money) (Money, error) {
	return m.Add(o.Negate())
}

func (m Money) Negate() Money {
	return Money{units: -m.units, currency: m.currency}
}

func (m Money) Amount() string {
	return formatUnits(m.units)
}

func (m Money) String() string {
	if m.currency.IsZero() {
		return formatUnits(m.units) + " ???"
	}
	return formatUnits(m.units) + " " + m.currency.code
}

func (m Money) compatible(o Money) error {
	if m.Validate() != nil || o.Validate() != nil {
		return ErrUninitialized
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}

func addUnits(a, b int64) (int64, error) {
	if b > 0 && a > MaxUnits-b {
		return 0, ErrOverflow
	}
	if b < 0 && a < MinUnits-b {
		return 0, ErrOverflow
	}
	return a + b, nil
}
