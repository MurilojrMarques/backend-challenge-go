package money_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
)

func TestParseValid(t *testing.T) {
	t.Parallel()

	cases := map[string]int64{
		"0.00":                 0,
		"0.05":                 5,
		"0.10":                 10,
		"1.05":                 105,
		"25.00":                2500,
		"25.50":                2550,
		"1000000.99":           100000099,
		"92233720368547757.99": 9223372036854775799,
		"92233720368547758.07": money.MaxUnits,
	}

	for in, want := range cases {
		m, err := money.Parse(in, money.BRL)
		require.NoError(t, err, in)
		assert.Equal(t, want, m.Units(), in)
		assert.Equal(t, money.BRL, m.Currency(), in)
		assert.Equal(t, in, m.Amount(), "round trip of %q", in)
	}
}

func TestParseInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		amount string
		want   error
	}{
		{"", money.ErrInvalidAmount},
		{" 25.00 ", money.ErrInvalidAmount},
		{"25", money.ErrInvalidAmount},
		{"25.5", money.ErrInvalidAmount},
		{"25.", money.ErrInvalidAmount},
		{".50", money.ErrInvalidAmount},
		{"+1.00", money.ErrInvalidAmount},
		{"025.00", money.ErrInvalidAmount},
		{"00.00", money.ErrInvalidAmount},
		{"25,00", money.ErrInvalidAmount},
		{"1,000.00", money.ErrInvalidAmount},
		{"1.0.0", money.ErrInvalidAmount},
		{"abc", money.ErrInvalidAmount},
		{"NaN", money.ErrInvalidAmount},
		{"Infinity", money.ErrInvalidAmount},
		{"+Infinity", money.ErrInvalidAmount},
		{"1e3", money.ErrInvalidAmount},
		{"1E3", money.ErrInvalidAmount},
		{"1.5e2", money.ErrInvalidAmount},
		{"0x10", money.ErrInvalidAmount},
		{"٢٥.٠٠", money.ErrInvalidAmount},
		{"25.00\x00", money.ErrInvalidAmount},

		{"25.000", money.ErrScaleExceeded},
		{"25.123456", money.ErrScaleExceeded},
		{"0.001", money.ErrScaleExceeded},
		{"0.005", money.ErrScaleExceeded},

		{"-1.00", money.ErrNegativeAmount},
		{"-0.00", money.ErrNegativeAmount},
		{"-abc", money.ErrNegativeAmount},

		{"92233720368547758.08", money.ErrOverflow},
		{"92233720368547759.00", money.ErrOverflow},
		{"99999999999999999999999.00", money.ErrOverflow},
	}

	for _, tc := range cases {
		m, err := money.Parse(tc.amount, money.BRL)
		assert.ErrorIs(t, err, tc.want, "%q", tc.amount)
		assert.Equal(t, money.Money{}, m, "%q must return zero value on error", tc.amount)
	}
}

func TestParseRejectsZeroCurrency(t *testing.T) {
	t.Parallel()

	_, err := money.Parse("25.00", money.Currency{})
	assert.ErrorIs(t, err, money.ErrInvalidCurrency)
}

func TestMustParse(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(1000), money.MustParse("10.00", money.BRL).Units())
	assert.Panics(t, func() { money.MustParse("nope", money.BRL) })
}

func TestAmountFormatting(t *testing.T) {
	t.Parallel()

	cases := map[int64]string{
		0:              "0.00",
		1:              "0.01",
		-1:             "-0.01",
		99:             "0.99",
		100:            "1.00",
		2500:           "25.00",
		-2500:          "-25.00",
		-5:             "-0.05",
		123456789:      "1234567.89",
		money.MaxUnits: "92233720368547758.07",
		money.MinUnits: "-92233720368547758.07",
	}

	for units, want := range cases {
		m, err := money.FromUnits(units, money.BRL)
		require.NoError(t, err)
		assert.Equal(t, want, m.Amount(), "units=%d", units)
	}
}

func TestAmountRoundTrip(t *testing.T) {
	t.Parallel()

	for _, units := range []int64{0, 1, 7, 99, 100, 101, 2500, 999999, 123456789012, money.MaxUnits} {
		m, err := money.FromUnits(units, money.BRL)
		require.NoError(t, err)

		parsed, err := money.Parse(m.Amount(), money.BRL)
		require.NoError(t, err, m.Amount())
		assert.True(t, parsed.Equal(m), "round trip %d -> %q -> %d", units, m.Amount(), parsed.Units())
	}
}
