package money

import "errors"

var (
	ErrUninitialized    = errors.New("money: uninitialized value")
	ErrInvalidCurrency  = errors.New("money: invalid currency")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrInvalidAmount    = errors.New("money: invalid amount")
	ErrScaleExceeded    = errors.New("money: scale exceeded")
	ErrNegativeAmount   = errors.New("money: negative amount")
	ErrOverflow         = errors.New("money: overflow")
)
