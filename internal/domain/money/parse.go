package money

import (
	"fmt"
	"strconv"
	"strings"
)

const maxMajorUnits = MaxUnits / unitsPerMajor

func Parse(amount string, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrInvalidCurrency
	}
	if amount == "" {
		return Money{}, fmt.Errorf("%w: empty", ErrInvalidAmount)
	}
	if amount[0] == '-' {
		return Money{}, fmt.Errorf("%w: %q", ErrNegativeAmount, amount)
	}

	intPart, fracPart, _ := strings.Cut(amount, ".")
	if !isCanonicalInteger(intPart) || !allDigits(fracPart) {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}
	switch {
	case len(fracPart) > Scale:
		return Money{}, fmt.Errorf("%w: %q has more than %d decimal places", ErrScaleExceeded, amount, Scale)
	case len(fracPart) < Scale:
		return Money{}, fmt.Errorf("%w: %q must have exactly %d decimal places", ErrInvalidAmount, amount, Scale)
	}

	major, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || major > maxMajorUnits {
		return Money{}, fmt.Errorf("%w: %q exceeds %s", ErrOverflow, amount, formatUnits(MaxUnits))
	}
	minor, _ := strconv.ParseInt(fracPart, 10, 64)

	units := major * unitsPerMajor
	if units > MaxUnits-minor {
		return Money{}, fmt.Errorf("%w: %q exceeds %s", ErrOverflow, amount, formatUnits(MaxUnits))
	}
	return Money{units: units + minor, currency: currency}, nil
}

func MustParse(amount string, currency Currency) Money {
	m, err := Parse(amount, currency)
	if err != nil {
		panic(err)
	}
	return m
}

func formatUnits(units int64) string {
	sign := ""
	if units < 0 {
		sign = "-"
		units = -units
	}
	return fmt.Sprintf("%s%d.%02d", sign, units/unitsPerMajor, units%unitsPerMajor)
}

func isCanonicalInteger(s string) bool {
	if s == "" || !allDigits(s) {
		return false
	}
	return len(s) == 1 || s[0] != '0'
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
