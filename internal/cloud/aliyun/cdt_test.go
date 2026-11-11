package aliyun

import (
	"testing"
	"time"
)

func cdtRow(region string, value any) map[string]any {
	return map[string]any{"BusinessRegionId": region, "ISPType": "bgp", "Traffic": value}
}
func TestCDTCounterParsingKeepsUnknownSeparateFromZero(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	o := parseCDT("account", map[string]any{"TrafficDetails": []any{cdtRow("cn-hongkong", float64(0))}}, now)
	if o.Error != nil || o.Mainland != nil || o.Overseas == nil || *o.Overseas != 0 || o.Quality != "inferred" || !o.FetchedAt.Equal(now) {
		t.Fatal("zero and absent scope conflated")
	}
	o = parseCDT("account", map[string]any{"Data": map[string]any{"TrafficDetails": []any{cdtRow("cn-shanghai", int64(100)), cdtRow("ap-southeast-1", "200"), cdtRow("cn-hongkong", float64(300))}}}, now)
	if o.Error != nil || o.Mainland == nil || *o.Mainland != 100 || o.Overseas == nil || *o.Overseas != 500 {
		t.Fatal("incorrect region aggregation")
	}
	o = parseCDT("account", map[string]any{"TrafficDetails": []any{}}, now)
	if o.Error != nil || o.Mainland != nil || o.Overseas != nil {
		t.Fatal("empty response treated as zero")
	}
}
func TestCDTInvalidOrUnverifiedResponseDoesNotProduceUsableTotal(t *testing.T) {
	premium := cdtRow("cn-hongkong", 10)
	premium["ISPType"] = "bgp_pro"
	for _, body := range []map[string]any{
		{}, {"TrafficDetails": "unexpected"},
		{"TrafficDetails": []any{cdtRow("cn-hongkong", -1)}},
		{"TrafficDetails": []any{cdtRow("cn-hongkong", 1.5)}},
		{"TrafficDetails": []any{cdtRow("cn-hongkong", int64(1<<54))}},
		{"TrafficDetails": []any{cdtRow("cn-hongkong", int64(1<<53)), cdtRow("ap-southeast-1", 1)}},
		{"TrafficDetails": []any{cdtRow("cn-hongkong", 1), cdtRow("cn-hongkong", 2)}},
		{"TrafficDetails": []any{premium}},
	} {
		o := parseCDT("account", body, time.Now())
		if o.Error == nil || o.Mainland != nil || o.Overseas != nil || o.Quality != "unknown" {
			t.Fatal("invalid or unsupported response used", o)
		}
	}
}
