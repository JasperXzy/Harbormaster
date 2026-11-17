package metering

import "math/big"

// Deviation compares exactly against ten percent; the floating ratio is only
// for presentation. A nonzero prediction against official zero is anomalous
// without inventing a finite percentage or emitting NaN/Infinity in JSON.
func Deviation(predicted, official int64) (*float64, bool, error) {
	if predicted < 0 || official < 0 {
		return nil, false, ErrInput
	}
	difference := predicted - official
	if difference < 0 {
		difference = -difference
	}
	if official == 0 {
		if difference > 0 {
			return nil, true, nil
		}
		zero := float64(0)
		return &zero, false, nil
	}
	ratio := float64(difference) / float64(official)
	left := new(big.Int).Mul(big.NewInt(difference), big.NewInt(10))
	return &ratio, left.Cmp(big.NewInt(official)) > 0, nil
}
