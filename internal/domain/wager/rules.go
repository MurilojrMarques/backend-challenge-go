package wager

import (
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

type Decision int

const (
	Proceed Decision = iota
	Await
	Reject
)

func (d Decision) String() string {
	switch d {
	case Proceed:
		return "PROCEED"
	case Await:
		return "AWAIT"
	case Reject:
		return "REJECT"
	default:
		return "UNKNOWN"
	}
}

type Verdict struct {
	Decision  Decision
	Code      FailureCode
	Moves     bool
	Direction wallet.Direction
}

func rejection(code FailureCode) Verdict {
	return Verdict{Decision: Reject, Code: code}
}

type Reference struct {
	Transaction     *Transaction
	AlreadyReversed bool
}

func Evaluate(tx *Transaction, w *wallet.Wallet, ref Reference) Verdict {
	if code := checkOwnership(tx, w); code != "" {
		return rejection(code)
	}
	if tx.HasReference() {
		if v, decided := checkReference(tx, ref); decided {
			return v
		}
	}
	return checkMovement(tx, w, ref.Transaction)
}

func checkOwnership(tx *Transaction, w *wallet.Wallet) FailureCode {
	if w.ID() != tx.WalletID() || w.PlayerID() != tx.PlayerID() {
		return WalletPlayerMismatch
	}
	if w.Currency() != tx.Amount().Currency() {
		return CurrencyMismatch
	}
	return ""
}

func checkReference(tx *Transaction, ref Reference) (Verdict, bool) {
	r := ref.Transaction
	if r == nil || !r.Status().Terminal() {
		return Verdict{Decision: Await}, true
	}
	if r.Status() != Processed {
		return rejection(ReferenceNotProcessed), true
	}
	if !tx.Kind().CanReference(r.Kind()) {
		return rejection(ReferenceKindNotAllowed), true
	}
	if !sameContext(tx, r) {
		return rejection(ReferenceMismatch), true
	}
	if tx.Kind().IsReversal() {
		if !r.Amount().Equal(tx.Amount()) {
			return rejection(ReferenceAmountMismatch), true
		}
		if ref.AlreadyReversed {
			return rejection(ReferenceAlreadyReversed), true
		}
	}
	return Verdict{}, false
}

func sameContext(tx, ref *Transaction) bool {
	txExt, _ := tx.External()
	refExt, ok := ref.External()
	return ok &&
		refExt.ProviderID == txExt.ProviderID &&
		refExt.RoundID == txExt.RoundID &&
		ref.PlayerID() == tx.PlayerID() &&
		ref.WalletID() == tx.WalletID() &&
		ref.Amount().Currency() == tx.Amount().Currency()
}

func checkMovement(tx *Transaction, w *wallet.Wallet, ref *Transaction) Verdict {
	if !tx.Kind().MovesBalance() {
		return Verdict{Decision: Proceed}
	}
	direction, ok := movement(tx.Kind(), ref)
	if !ok {
		return rejection(ReferenceKindNotAllowed)
	}
	if direction == wallet.Debit {
		if !w.CanDebit(tx.Amount()) {
			return rejection(insufficientFundsCode(tx.Kind()))
		}
	} else if _, err := w.Balance().Add(tx.Amount()); err != nil {
		return rejection(BalanceLimitExceeded)
	}
	return Verdict{Decision: Proceed, Moves: true, Direction: direction}
}

func movement(kind Kind, ref *Transaction) (wallet.Direction, bool) {
	switch kind {
	case Opening, Win, Refund:
		return wallet.Credit, true
	case Bet:
		return wallet.Debit, true
	case Rollback:
		if ref == nil {
			return "", false
		}
		original, ok := movement(ref.Kind(), nil)
		if !ok {
			return "", false
		}
		return opposite(original), true
	default:
		return "", false
	}
}

func opposite(d wallet.Direction) wallet.Direction {
	if d == wallet.Debit {
		return wallet.Credit
	}
	return wallet.Debit
}

func insufficientFundsCode(kind Kind) FailureCode {
	if kind == Rollback {
		return ReversalInsufficientFunds
	}
	return InsufficientFunds
}

type ReferencePolicy struct {
	MaxAttempts int
	TTL         time.Duration
}

func (p ReferencePolicy) Exhausted(tx *Transaction, now time.Time) bool {
	if p.MaxAttempts > 0 && tx.ReferenceAttempts() >= p.MaxAttempts {
		return true
	}
	return p.TTL > 0 && !now.Before(tx.CreatedAt().Add(p.TTL))
}
