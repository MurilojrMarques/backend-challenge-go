package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

type ConstraintError struct {
	Code       string
	Constraint string
	Table      string
}

func (e *ConstraintError) Error() string {
	return fmt.Sprintf("postgres: %s violates %s on %s", e.Code, e.Constraint, e.Table)
}

func (e *ConstraintError) Is(target error) bool {
	switch e.Code {
	case "23505":
		return target == application.ErrConflict
	default:
		return target == application.ErrIntegrity
	}
}

func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return translatePgError(pgErr)
	}

	var netErr net.Error
	if errors.As(err, &netErr) || pgconn.Timeout(err) || pgconn.SafeToRetry(err) {
		return fmt.Errorf("%w: %v", application.ErrUnavailable, err)
	}
	return err
}

func translatePgError(pgErr *pgconn.PgError) error {
	switch pgErr.Code {
	case "23505", "23514", "23503", "23502", "23001", "23000":
		return &ConstraintError{Code: pgErr.Code, Constraint: pgErr.ConstraintName, Table: pgErr.TableName}
	case "40001", "40P01", "55P03", "57P01", "57P02", "57P03", "53300", "53400":
		return fmt.Errorf("%w: %s (%s)", application.ErrUnavailable, pgErr.Message, pgErr.Code)
	}
	if len(pgErr.Code) >= 2 {
		switch pgErr.Code[:2] {
		case "08", "53", "57":
			return fmt.Errorf("%w: %s (%s)", application.ErrUnavailable, pgErr.Message, pgErr.Code)
		}
	}
	return fmt.Errorf("postgres: %s (%s): %w", pgErr.Message, pgErr.Code, pgErr)
}
