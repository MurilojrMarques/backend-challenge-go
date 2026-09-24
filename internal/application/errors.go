package application

import "errors"

var (
	ErrNotFound               = errors.New("app: not found")
	ErrConflict               = errors.New("app: conflict")
	ErrConcurrentModification = errors.New("app: concurrent modification")
	ErrIntegrity              = errors.New("app: integrity violation")
	ErrUnavailable            = errors.New("app: dependency unavailable")
	ErrForbidden              = errors.New("app: forbidden")
	ErrInvalidInput           = errors.New("app: invalid input")
	ErrMessageInFlight        = errors.New("app: message is being processed by another consumer")
)
