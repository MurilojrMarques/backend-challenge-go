package wager

import "fmt"

type FailureCode string

const (
	InsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	BalanceLimitExceeded      FailureCode = "BALANCE_LIMIT_EXCEEDED"
	CurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	WalletPlayerMismatch      FailureCode = "WALLET_PLAYER_MISMATCH"
	ReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	ReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	ReferenceKindNotAllowed   FailureCode = "REFERENCE_KIND_NOT_ALLOWED"
	ReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	ReferenceAmountMismatch   FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	ReferenceAlreadyReversed  FailureCode = "REFERENCE_ALREADY_REVERSED"
	PermanentFailure          FailureCode = "PERMANENT_FAILURE"
)

var correctable = map[FailureCode]bool{
	CurrencyMismatch:        true,
	WalletPlayerMismatch:    true,
	ReferenceKindNotAllowed: true,
	ReferenceMismatch:       true,
	ReferenceAmountMismatch: true,
}

func ParseFailureCode(s string) (FailureCode, error) {
	c := FailureCode(s)
	if !c.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidFailureCode, s)
	}
	return c, nil
}

func (c FailureCode) Valid() bool {
	switch c {
	case InsufficientFunds, ReversalInsufficientFunds, BalanceLimitExceeded,
		CurrencyMismatch, WalletPlayerMismatch,
		ReferenceNotFound, ReferenceNotProcessed, ReferenceKindNotAllowed, ReferenceMismatch,
		ReferenceAmountMismatch, ReferenceAlreadyReversed, PermanentFailure:
		return true
	default:
		return false
	}
}

func (c FailureCode) String() string {
	return string(c)
}

func (c FailureCode) Rejection() bool {
	return c.Valid() && c != PermanentFailure
}

func (c FailureCode) Correctable() bool {
	return correctable[c]
}
