package money

import "fmt"

const (
	Scale         = 2
	unitsPerMajor = 100
)

type Currency struct {
	code string
}

var supportedCurrencies = map[string]struct{}{
	"EUR": {},
	"BRL": {},
	"USD": {},
}

var BRL = MustCurrency("BRL")

func ParseCurrency(code string) (Currency, error) {
	if _, ok := supportedCurrencies[code]; !ok {
		return Currency{}, fmt.Errorf("%w: %q is not a supported ISO 4217 code with %d decimal places", ErrInvalidCurrency, code, Scale)
	}
	return Currency{code: code}, nil
}

func MustCurrency(code string) Currency {
	c, err := ParseCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}

func (c Currency) Code() string {
	return c.code
}

func (c Currency) String() string {
	return c.code
}

func (c Currency) IsZero() bool {
	return c.code == ""
}
