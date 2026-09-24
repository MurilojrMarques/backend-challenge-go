package money_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
)

var usd = money.MustCurrency("USD")

func brl(t *testing.T, units int64) money.Money {
	t.Helper()
	m, err := money.FromUnits(units, money.BRL)
	require.NoError(t, err)
	return m
}

func TestParseCurrency(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"BRL", "USD", "EUR"} {
		c, err := money.ParseCurrency(code)
		require.NoError(t, err, code)
		assert.Equal(t, code, c.Code())
		assert.Equal(t, code, c.String())
		assert.False(t, c.IsZero())
	}

	for _, code := range []string{"", "BR", "BRLL", "brl", "Brl", "BR1", "BRL ", " BRL", "XXX", "GBP", "JPY", "KWD", "€UR"} {
		c, err := money.ParseCurrency(code)
		assert.ErrorIs(t, err, money.ErrInvalidCurrency, "%q", code)
		assert.True(t, c.IsZero(), "%q", code)
	}
}

func TestMustCurrency(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "USD", money.MustCurrency("USD").Code())
	assert.Panics(t, func() { money.MustCurrency("xx") })
}

func TestFromUnits(t *testing.T) {
	t.Parallel()

	m := brl(t, -250)
	assert.Equal(t, int64(-250), m.Units())
	assert.Equal(t, money.BRL, m.Currency())
	assert.NoError(t, m.Validate())
	assert.True(t, m.IsNegative())

	_, err := money.FromUnits(100, money.Currency{})
	assert.ErrorIs(t, err, money.ErrInvalidCurrency)

	_, err = money.FromUnits(math.MinInt64, money.BRL)
	assert.ErrorIs(t, err, money.ErrOverflow)

	edge, err := money.FromUnits(money.MinUnits, money.BRL)
	require.NoError(t, err)
	assert.Equal(t, money.MinUnits, edge.Units())
}

func TestZeroAndUninitialized(t *testing.T) {
	t.Parallel()

	z := money.Zero(money.BRL)
	assert.True(t, z.IsZero())
	assert.NoError(t, z.Validate())
	assert.Equal(t, "0.00", z.Amount())
	assert.Equal(t, z, z.Negate())

	var uninitialized money.Money
	assert.ErrorIs(t, uninitialized.Validate(), money.ErrUninitialized)
	assert.Equal(t, "0.00 ???", uninitialized.String())
}

func TestPredicates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		units    int64
		zero     bool
		positive bool
		negative bool
		sign     int
	}{
		{0, true, false, false, 0},
		{1, false, true, false, 1},
		{-1, false, false, true, -1},
		{money.MaxUnits, false, true, false, 1},
		{money.MinUnits, false, false, true, -1},
	}

	for _, tc := range cases {
		m := brl(t, tc.units)
		assert.Equal(t, tc.zero, m.IsZero(), "IsZero %d", tc.units)
		assert.Equal(t, tc.positive, m.IsPositive(), "IsPositive %d", tc.units)
		assert.Equal(t, tc.negative, m.IsNegative(), "IsNegative %d", tc.units)
		assert.Equal(t, tc.sign, m.Sign(), "Sign %d", tc.units)
	}
}

func TestAdd(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b int64
		want int64
	}{
		{"simple", 1000, 2550, 3550},
		{"with zero", 1000, 0, 1000},
		{"negative operand", 1000, -300, 700},
		{"both negative", -1000, -300, -1300},
		{"cancel out", 500, -500, 0},
		{"max plus zero", money.MaxUnits, 0, money.MaxUnits},
		{"min plus zero", money.MinUnits, 0, money.MinUnits},
		{"reach max", money.MaxUnits - 1, 1, money.MaxUnits},
		{"reach min", money.MinUnits + 1, -1, money.MinUnits},
	}

	for _, tc := range cases {
		got, err := brl(t, tc.a).Add(brl(t, tc.b))
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got.Units(), tc.name)
		assert.Equal(t, money.BRL, got.Currency(), tc.name)
	}
}

func TestSub(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b int64
		want int64
	}{
		{"simple", 10000, 8000, 2000},
		{"to zero", 8000, 8000, 0},
		{"goes negative", 8000, 10000, -2000},
		{"subtract negative", 1000, -500, 1500},
		{"min minus zero", money.MinUnits, 0, money.MinUnits},
		{"reach min", money.MinUnits + 1, 1, money.MinUnits},
		{"max minus min", money.MaxUnits, money.MaxUnits, 0},
	}

	for _, tc := range cases {
		got, err := brl(t, tc.a).Sub(brl(t, tc.b))
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got.Units(), tc.name)
		assert.Equal(t, money.BRL, got.Currency(), tc.name)
	}
}

func TestOverflow(t *testing.T) {
	t.Parallel()

	maxM, minM, one := brl(t, money.MaxUnits), brl(t, money.MinUnits), brl(t, 1)

	ops := map[string]func() (money.Money, error){
		"max + 1":        func() (money.Money, error) { return maxM.Add(one) },
		"1 + max":        func() (money.Money, error) { return one.Add(maxM) },
		"min + (-1)":     func() (money.Money, error) { return minM.Add(one.Negate()) },
		"min - 1":        func() (money.Money, error) { return minM.Sub(one) },
		"max - (-1)":     func() (money.Money, error) { return maxM.Sub(one.Negate()) },
		"max - min":      func() (money.Money, error) { return maxM.Sub(minM) },
		"min - max":      func() (money.Money, error) { return minM.Sub(maxM) },
		"half + half+2":  func() (money.Money, error) { return brl(t, money.MaxUnits/2).Add(brl(t, money.MaxUnits/2+2)) },
		"-half + -half2": func() (money.Money, error) { return brl(t, money.MinUnits/2).Add(brl(t, money.MinUnits/2-2)) },
	}

	for name, op := range ops {
		_, err := op()
		assert.ErrorIs(t, err, money.ErrOverflow, name)
	}
}

func TestNegate(t *testing.T) {
	t.Parallel()

	for _, units := range []int64{0, 1, -1, 2500, -2500, money.MaxUnits, money.MinUnits} {
		m := brl(t, units)
		neg := m.Negate()
		assert.Equal(t, -units, neg.Units(), "units=%d", units)
		assert.Equal(t, money.BRL, neg.Currency())
		assert.Equal(t, m, neg.Negate(), "double negation")

		sum, err := m.Add(neg)
		require.NoError(t, err)
		assert.True(t, sum.IsZero(), "x + (-x) = 0 for %d", units)
	}

	assert.Equal(t, brl(t, -money.MaxUnits), brl(t, money.MinUnits), "range is symmetric")
}

func TestCmpAndEqual(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b int64
		want int
	}{
		{0, 0, 0},
		{100, 100, 0},
		{100, 200, -1},
		{200, 100, 1},
		{-100, 100, -1},
		{money.MinUnits, money.MaxUnits, -1},
	}

	for _, tc := range cases {
		a, b := brl(t, tc.a), brl(t, tc.b)
		got, err := a.Cmp(b)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "Cmp(%d, %d)", tc.a, tc.b)
		assert.Equal(t, tc.want == 0, a.Equal(b), "Equal(%d, %d)", tc.a, tc.b)
	}
}

func TestCurrencyMismatch(t *testing.T) {
	t.Parallel()

	a := brl(t, 1000)
	b, err := money.FromUnits(1000, usd)
	require.NoError(t, err)

	_, err = a.Add(b)
	assert.ErrorIs(t, err, money.ErrCurrencyMismatch)
	_, err = a.Sub(b)
	assert.ErrorIs(t, err, money.ErrCurrencyMismatch)
	_, err = a.Cmp(b)
	assert.ErrorIs(t, err, money.ErrCurrencyMismatch)

	assert.False(t, a.Equal(b), "same amount, different currency")
	assert.False(t, a.SameCurrency(b))
	assert.True(t, a.SameCurrency(brl(t, 5)))
}

func TestOperationsRejectUninitialized(t *testing.T) {
	t.Parallel()

	var zero money.Money
	valid := brl(t, 100)

	ops := map[string]func() error{
		"valid + zero": func() error { _, err := valid.Add(zero); return err },
		"zero + valid": func() error { _, err := zero.Add(valid); return err },
		"valid - zero": func() error { _, err := valid.Sub(zero); return err },
		"zero - valid": func() error { _, err := zero.Sub(valid); return err },
		"valid cmp zero": func() error {
			_, err := valid.Cmp(zero)
			return err
		},
		"zero + zero": func() error { _, err := zero.Add(zero); return err },
	}

	for name, op := range ops {
		assert.ErrorIs(t, op(), money.ErrUninitialized, name)
	}
}

func TestImmutability(t *testing.T) {
	t.Parallel()

	original, other := brl(t, 1000), brl(t, 250)

	_, err := original.Add(other)
	require.NoError(t, err)
	_, err = original.Sub(other)
	require.NoError(t, err)
	_ = original.Negate()

	assert.Equal(t, int64(1000), original.Units())
	assert.Equal(t, int64(250), other.Units())
}

func TestString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "25.50 BRL", brl(t, 2550).String())
	assert.Equal(t, "-0.05 BRL", brl(t, -5).String())
	assert.Equal(t, "0.00 BRL", money.Zero(money.BRL).String())
}
