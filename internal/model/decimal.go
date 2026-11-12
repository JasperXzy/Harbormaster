package model

import (
	"math/big"
	"regexp"
)

var canonicalDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]{1,12})?$`)
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// DecimalAmount parses finance decimal text without binary floating-point
// rounding or accepting fractions, exponents or thousands separators.
func DecimalAmount(text string) (*big.Rat, bool) {
	if len(text) > 80 || !canonicalDecimal.MatchString(text) {
		return nil, false
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, false
	}
	scaled := new(big.Rat).Mul(value, big.NewRat(1000000000000, 1))
	if scaled.Num().BitLen() > 256 {
		return nil, false
	}
	return value, true
}
