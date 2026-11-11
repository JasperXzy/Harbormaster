package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

func TestReportIntervalValidationAndDemo(t *testing.T) {
	for _, tc := range []struct {
		interval      string
		seconds, exit int
	}{
		{"1s", 1, 0}, {"10s", 10, 0}, {"1m", 60, 0},
		{"0s", 0, 1}, {"-1s", 0, 1}, {"1500ms", 0, 1}, {"61s", 0, 1},
	} {
		t.Run(tc.interval, func(t *testing.T) {
			var out, logs bytes.Buffer
			if code := Agent(context.Background(), []string{"run-once", "--dev", "--report-interval", tc.interval}, &out, &logs); code != tc.exit {
				t.Fatal(code, logs.String())
			}
			if tc.exit != 0 {
				if out.Len() != 0 {
					t.Fatal("invalid cadence emitted sample")
				}
				return
			}
			var report agentproto.Report
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.IntervalSeconds() != tc.seconds {
				t.Fatal(report)
			}
			if tc.seconds == 10 && bytes.Contains(out.Bytes(), []byte("report_interval_seconds")) {
				t.Fatal("default report breaks legacy decoder")
			}
		})
	}
}
