package application

import (
	"errors"
	"fmt"
)

type ConflictError struct {
	Constraint string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("app: conflict on %s", e.Constraint)
}

func (e *ConflictError) Is(target error) bool {
	return target == ErrConflict
}

var (
	ErrNotFound               = errors.New("app: not found")
	ErrConflict               = errors.New("app: conflict")
	ErrConcurrentModification = errors.New("app: concurrent modification")
	ErrIntegrity              = errors.New("app: integrity violation")
	ErrUnavailable            = errors.New("app: dependency unavailable")
	ErrForbidden              = errors.New("app: forbidden")
	ErrInvalidInput           = errors.New("app: invalid input")
	ErrMessageInFlight        = errors.New("app: message is being processed by another consumer")
	ErrWalletExists           = fmt.Errorf("%w: a wallet already exists for this player and currency", ErrConflict)
)
