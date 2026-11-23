package metering

import (
	"math"
	"testing"
)

func TestDeviationExactBoundaryZeroAndLargeValues(t *testing.T) {
	for _, tc := range []struct {
		predicted, official int64
		bad, finite         bool
	}{
		{110, 100, false, true}, {111, 100, true, true}, {90, 100, false, true}, {89, 100, true, true}, {0, 0, false, true}, {1, 0, true, false}, {math.MaxInt64, 1, true, true}, {math.MaxInt64, math.MaxInt64, false, true},
	} {
		ratio, bad, err := Deviation(tc.predicted, tc.official)
		if err != nil || bad != tc.bad || (ratio != nil) != tc.finite {
			t.Fatal(tc, ratio, bad, err)
		}
	}
}
