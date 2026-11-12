package netprobe

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestValidateRemoteProbeRecomputesSummaryAndRejectsMalformed(t *testing.T) {
	now := time.Now().UTC()
	q := request()
	q.Count = 1
	good := Result{StartedAt: now, FinishedAt: now.Add(time.Millisecond), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []Sample{{Sequence: 1, StartedAt: now, RTTMS: new(float64(1))}}, Summary: &Summary{LossPercent: 999}}
	raw, _ := json.Marshal(good)
	for _, mutate := range []func(*Result){func(r *Result) { r.Address = "10.0.0.1" }, func(r *Result) { r.Address = "8.8.8.8" }, func(r *Result) { r.ResolvedAddresses = []string{"127.0.0.1"} }, func(r *Result) { r.Samples[0].RTTMS = new(math.NaN()) }, func(r *Result) { r.Samples[0].RTTMS = new(math.Inf(1)) }, func(r *Result) { r.Samples[0].RTTMS = new(float64(-1)) }, func(r *Result) { r.Samples[0].RTTMS = new(float64(10000)) }, func(r *Result) { r.Samples[0].ErrorCode = new("private detail") }, func(r *Result) { r.Samples[0].Sequence = 2 }, func(r *Result) { r.Samples[0].StartedAt = now.Add(time.Hour) }, func(r *Result) { r.FinishedAt = now.Add(-time.Second) }, func(r *Result) { r.Samples = nil }} {
		var r Result
		_ = json.Unmarshal(raw, &r)
		mutate(&r)
		if ValidateResult(q, &r, nil, now) == nil {
			t.Fatal("invalid accepted", r)
		}
	}
	if err := ValidateResult(q, &good, nil, now); err != nil || good.Summary.LossPercent != 0 || *good.Summary.AverageMS != 1 {
		t.Fatal(good, err)
	}
	r := Result{StartedAt: now, FinishedAt: now, Samples: []Sample{}, ResolvedAddresses: []string{}}
	code := "probe_interrupted"
	if err := ValidateResult(q, &r, &code, now); err != nil || r.Summary != nil {
		t.Fatal(r, err)
	}
	code = "arbitrary error with raw details"
	if ValidateResult(q, &r, &code, now) == nil {
		t.Fatal("raw error accepted")
	}
}
