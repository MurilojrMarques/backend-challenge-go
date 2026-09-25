package postgres

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

func pgErr(code, constraint string) error {
	return &pgconn.PgError{Code: code, ConstraintName: constraint, TableName: "t", Message: "m"}
}

func TestTranslate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in   error
		want error
	}{
		"nil":                     {nil, nil},
		"no rows":                 {pgx.ErrNoRows, application.ErrNotFound},
		"unique violation":        {pgErr("23505", "wallets_player_currency_key"), application.ErrConflict},
		"check violation":         {pgErr("23514", "wallets_balance_non_negative"), application.ErrIntegrity},
		"foreign key":             {pgErr("23503", "fk"), application.ErrIntegrity},
		"not null":                {pgErr("23502", ""), application.ErrIntegrity},
		"restrict from trigger":   {pgErr("23001", ""), application.ErrIntegrity},
		"serialization failure":   {pgErr("40001", ""), application.ErrUnavailable},
		"deadlock":                {pgErr("40P01", ""), application.ErrUnavailable},
		"lock not available":      {pgErr("55P03", ""), application.ErrUnavailable},
		"admin shutdown":          {pgErr("57P01", ""), application.ErrUnavailable},
		"too many connections":    {pgErr("53300", ""), application.ErrUnavailable},
		"connection exception":    {pgErr("08006", ""), application.ErrUnavailable},
		"net timeout":             {&net.DNSError{IsTimeout: true}, application.ErrUnavailable},
		"context canceled":        {context.Canceled, context.Canceled},
		"context deadline":        {context.DeadlineExceeded, context.DeadlineExceeded},
		"unique is not integrity": {pgErr("23505", "x"), application.ErrConflict},
	}

	for name, tc := range cases {
		got := translate(tc.in)
		if tc.want == nil {
			assert.NoError(t, got, name)
			continue
		}
		assert.ErrorIs(t, got, tc.want, name)
	}

	assert.NotErrorIs(t, translate(pgErr("23505", "x")), application.ErrIntegrity)
	assert.NotErrorIs(t, translate(pgErr("23514", "x")), application.ErrConflict)
}

func TestConstraintErrorExposesName(t *testing.T) {
	t.Parallel()

	err := translate(pgErr("23505", "wager_transactions_idempotency_key"))
	var conflict *application.ConflictError
	require.True(t, errors.As(err, &conflict))
	assert.Equal(t, "wager_transactions_idempotency_key", conflict.Constraint)
	assert.Contains(t, conflict.Error(), "wager_transactions_idempotency_key")

	err = translate(pgErr("23514", "wallets_balance_non_negative"))
	var ce *ConstraintError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, "wallets_balance_non_negative", ce.Constraint)
	assert.Equal(t, "23514", ce.Code)
}

func TestTranslateKeepsUnknownErrors(t *testing.T) {
	t.Parallel()

	unknown := errors.New("boom")
	assert.Equal(t, unknown, translate(unknown))

	syntax := translate(pgErr("42601", ""))
	assert.NotErrorIs(t, syntax, application.ErrUnavailable)
	assert.NotErrorIs(t, syntax, application.ErrIntegrity)
	var pg *pgconn.PgError
	assert.True(t, errors.As(syntax, &pg))
}
