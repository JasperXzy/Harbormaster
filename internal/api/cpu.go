package api

import (
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Snapshots do not become permanently fresh merely because collection stopped.
// This changes only the response status, preserving sample and query times.
func ageCPU(items []map[string]any, now time.Time) {
	for _, item := range items {
		cpu, ok := item["cpu"].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"usage", "credit_balance"} {
			m, ok := cpu[key].(map[string]any)
			if !ok || m["status"] != "ok" {
				continue
			}
			raw, _ := m["observed_at"].(string)
			observed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil || observed.After(now) || observed.Add(model.CPUFreshness).Before(now) {
				m["status"] = "stale"
			}
		}
	}
}
