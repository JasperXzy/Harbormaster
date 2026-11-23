package model

import (
	"strings"
	"testing"
)

func TestDecimalAmountsStayExactAndBounded(t *testing.T) {
	a, ok := DecimalAmount("0.100000000001")
	b, valid := DecimalAmount("0.1")
	if !ok || !valid || a.Cmp(b) != 1 {
		t.Fatal("fractional boundary rounded")
	}
	for _, text := range []string{"0", "-0", "-100.25", "100.000000000000", "0.000000000001"} {
		if _, ok := DecimalAmount(text); !ok {
			t.Fatal("valid decimal rejected", text)
		}
	}
	for _, text := range []string{"", "NaN", "1/3", "1e2", "1,000.00", "+1", "01", ".1", "1.", "0.0000000000001", strings.Repeat("9", 80)} {
		if _, ok := DecimalAmount(text); ok {
			t.Fatal("invalid decimal accepted", text)
		}
	}
	var nilSlice []BalanceThreshold
	rule := AlertRule{Key: "balance_low", Params: AlertParams{BalanceThresholds: &nilSlice}}
	if len(rule.Validate()) == 0 {
		t.Fatal("nil list would persist as null and break rule reads")
	}
}
