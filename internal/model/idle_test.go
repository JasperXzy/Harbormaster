package model

import (
	"testing"
	"time"
)

func TestIdleSourceAgeAndUnknownCoverage(t *testing.T) {
	now := time.Now().UTC()
	v := IdleSource{Enabled: true}
	v.Age(now)
	if v.Status != "never_fetched" || v.Scanned != nil || v.Candidates != nil {
		t.Fatal(v)
	}
	unknown := 1
	v.FetchedAt, v.Unclassified = &now, &unknown
	v.Age(now)
	if v.Status != "partial" {
		t.Fatal(v)
	}
	v.Age(now.Add(10*time.Minute + time.Nanosecond))
	if v.Status != "stale" {
		t.Fatal(v)
	}
	v.LastError = Error("permission_denied")
	v.Age(now)
	if v.Status != "failing" {
		t.Fatal(v)
	}
	v.Enabled = false
	v.Age(now)
	if v.Status != "disabled" {
		t.Fatal(v)
	}
}

func TestIdleCursorBindsFiltersAndRejectsMalformedData(t *testing.T) {
	f := IdleFilter{AccountID: "a", RegionID: "cn-shanghai", Kind: "disk", Limit: 51}
	encoded := EncodeIdleCursor("last-id", f)
	if after, ok := DecodeIdleCursor(encoded, f); !ok || after != "last-id" {
		t.Fatal(after, ok)
	}
	for _, changed := range []IdleFilter{{AccountID: "b", RegionID: f.RegionID, Kind: f.Kind}, {AccountID: f.AccountID, RegionID: "cn-hongkong", Kind: f.Kind}, {AccountID: f.AccountID, RegionID: f.RegionID, Kind: "eip"}} {
		if _, ok := DecodeIdleCursor(encoded, changed); ok {
			t.Fatal("cursor reused across filters", changed)
		}
	}
	for _, raw := range []string{"!", "e30", EncodeIdleCursor("", f)} {
		if _, ok := DecodeIdleCursor(raw, f); ok {
			t.Fatal("malformed cursor accepted")
		}
	}
}
