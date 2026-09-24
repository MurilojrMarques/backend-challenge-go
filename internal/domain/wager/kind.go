package wager

import "fmt"

type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

var allowedReferences = map[Kind][]Kind{
	Win:      {Bet},
	Refund:   {Bet},
	Rollback: {Bet, Win, Refund},
}

func ParseKind(s string) (Kind, error) {
	k := Kind(s)
	if !k.Valid() {
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, s)
	}
	return k, nil
}

func (k Kind) Valid() bool {
	switch k {
	case Opening, Bet, Win, Loss, Refund, Rollback:
		return true
	default:
		return false
	}
}

func (k Kind) String() string {
	return string(k)
}

func (k Kind) External() bool {
	return k.Valid() && k != Opening
}

func (k Kind) IsReversal() bool {
	return k == Refund || k == Rollback
}

func (k Kind) AllowsReference() bool {
	return k == Win || k.IsReversal()
}

func (k Kind) RequiresZeroAmount() bool {
	return k == Loss
}

func (k Kind) MovesBalance() bool {
	return k.Valid() && k != Loss
}

func (k Kind) CanReference(ref Kind) bool {
	for _, allowed := range allowedReferences[k] {
		if allowed == ref {
			return true
		}
	}
	return false
}
