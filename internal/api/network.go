package api

import (
	"github.com/JasperXzy/harbormaster/internal/model"
	"time"
)

func ageNetworkRates(items []map[string]any, now time.Time) {
	for _, item := range items {
		n, ok := item["cloud_network"].(map[string]any)
		if !ok {
			continue
		}
		streams, _ := n["streams"].([]any)
		for _, raw := range streams {
			stream, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, direction := range []string{"ingress", "egress"} {
				rate, ok := stream[direction].(map[string]any)
				if !ok || rate["status"] != "ok" {
					continue
				}
				at, _ := rate["observed_at"].(string)
				sampled, err := time.Parse(time.RFC3339Nano, at)
				if err != nil || sampled.After(now) || sampled.Add(model.NetworkRateFreshness).Before(now) {
					rate["status"] = "stale"
				}
			}
		}
	}
}
