package model

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func TestNextTraceTimeCivilDateDSTAndSkippedDate(t *testing.T) {
	for _, c := range []struct{ zone, now, clock, want string }{
		{"UTC", "2026-11-13T02:59:59Z", "03:00", "2026-11-13T03:00:00Z"},
		{"UTC", "2026-11-13T03:00:00Z", "03:00", "2026-11-14T03:00:00Z"},
		{"Asia/Singapore", "2026-11-13T12:00:00Z", "03:00", "2026-11-13T19:00:00Z"},
		{"America/New_York", "2026-03-08T06:59:00Z", "02:30", "2026-03-08T07:00:00Z"},
		{"America/New_York", "2026-11-01T04:00:00Z", "01:30", "2026-11-01T05:30:00Z"},
		{"America/New_York", "2026-11-01T05:31:00Z", "01:30", "2026-11-02T06:30:00Z"},
		{"Pacific/Apia", "2011-12-30T09:00:00Z", "03:00", "2011-12-30T13:00:00Z"},
	} {
		t.Run(c.zone+c.now, func(t *testing.T) {
			loc, err := time.LoadLocation(c.zone)
			if err != nil {
				t.Fatal(err)
			}
			now, _ := time.Parse(time.RFC3339, c.now)
			at, err := NextTraceTime(now, c.clock, loc)
			if err != nil || at.Format(time.RFC3339) != c.want {
				t.Fatal(at, err, c.want)
			}
		})
	}
	for _, clock := range []string{"3:00", "24:00", "03:0", "03:00:00"} {
		if _, err := NextTraceTime(time.Now(), clock, time.UTC); err == nil {
			t.Fatal("invalid clock", clock)
		}
	}
	s := DefaultTraceSchedule()
	if !s.Validate() {
		t.Fatal(s)
	}
	s.Count = 5
	if s.Validate() {
		t.Fatal("over-budget schedule accepted")
	}
}

func TestTraceASPathRequiresCompleteReachedUniqueEvidence(t *testing.T) {
	base := TraceDetail{TraceRun: TraceRun{Status: "succeeded", Outcome: &TraceOutcome{Reached: true, Address: "1.1.1.1", HopCount: 4}, Request: netprobe.TraceRequest{Count: 3}}, Enrichment: &TraceEnrichment{Status: "partial", ASNDatabase: &ipinfo.Dataset{Name: "synthetic", Edition: "2026-11"}, Hops: []TraceHopInfo{
		{TTL: 1, Attempts: 3, Responses: 3, ASNCandidates: []uint32{4134}},
		{TTL: 2, Attempts: 3, Responses: 3, ASNCandidates: []uint32{4134}},
		{TTL: 3, Attempts: 3, Responses: 3, ASNCandidates: []uint32{4809}},
		{TTL: 4, Attempts: 3, Responses: 3, ASNCandidates: []uint32{13335}},
	}}}
	path, reason := TraceASPath(base)
	if reason != "" || !slices.Equal(path, []uint32{4134, 4809, 13335}) {
		t.Fatal(path, reason)
	}
	for _, c := range []struct {
		name, reason string
		change       func(*TraceDetail)
	}{
		{"failed", "measurement_failed", func(d *TraceDetail) { d.Status = "failed" }},
		{"unreached", "measurement_failed", func(d *TraceDetail) { d.Outcome.Reached = false }},
		{"empty address", "measurement_failed", func(d *TraceDetail) { d.Outcome.Address = "" }},
		{"missing", "enrichment_unavailable", func(d *TraceDetail) { d.Enrichment = nil }},
		{"gap", "incomplete_responses", func(d *TraceDetail) { d.Enrichment.Hops[1].Responses = 2 }},
		{"partial attempts", "incomplete_responses", func(d *TraceDetail) { d.Enrichment.Hops[1].Attempts = 2; d.Enrichment.Hops[1].Responses = 2 }},
		{"unknown", "unknown_asn", func(d *TraceDetail) { d.Enrichment.Hops[1].UnknownASN = true }},
		{"multipath", "multiple_asns", func(d *TraceDetail) { d.Enrichment.Hops[1].ASNCandidates = []uint32{4134, 4809} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			data, _ := json.Marshal(base)
			var d TraceDetail
			json.Unmarshal(data, &d)
			c.change(&d)
			path, reason := TraceASPath(d)
			if path != nil || reason != c.reason {
				t.Fatal(path, reason)
			}
		})
	}
}
