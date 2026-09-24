package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToPgxURL(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"postgres://u:p@h:5432/db?sslmode=disable":   "pgx5://u:p@h:5432/db?sslmode=disable",
		"postgresql://u:p@h:5432/db":                 "pgx5://u:p@h:5432/db",
		"pgx5://u:p@h:5432/db":                       "pgx5://u:p@h:5432/db",
		"postgres://u:p@h/db?x-migrations-table=foo": "pgx5://u:p@h/db?x-migrations-table=foo",
	}
	for in, want := range cases {
		assert.Equal(t, want, toPgxURL(in), in)
	}
}

func TestDownSteps(t *testing.T) {
	t.Parallel()

	steps, err := downSteps(nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, steps)

	steps, err = downSteps([]string{"3"})
	assert.NoError(t, err)
	assert.Equal(t, 3, steps)

	steps, err = downSteps([]string{"all"})
	assert.NoError(t, err)
	assert.Equal(t, 0, steps)

	for _, bad := range []string{"0", "-1", "x"} {
		_, err = downSteps([]string{bad})
		assert.ErrorIs(t, err, errUsage, bad)
	}
}

func TestRunUsageErrors(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 2, run(nil, "postgres://x"))
	assert.Equal(t, 2, run([]string{"up"}, ""))
}
