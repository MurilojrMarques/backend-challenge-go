package wager

import "fmt"

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

var transitions = map[Status][]Status{
	Pending:          {PendingReference, Processed, Rejected, Failed},
	PendingReference: {Processed, Rejected, Failed},
}

func ParseStatus(s string) (Status, error) {
	st := Status(s)
	if !st.Valid() {
		return "", fmt.Errorf("%w: unknown status %q", ErrInvalidTransaction, s)
	}
	return st, nil
}

func (s Status) Valid() bool {
	switch s {
	case Pending, PendingReference, Processed, Rejected, Failed:
		return true
	default:
		return false
	}
}

func (s Status) String() string {
	return string(s)
}

func (s Status) Terminal() bool {
	return s == Processed || s == Rejected || s == Failed
}

func (s Status) CanTransitionTo(to Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}
