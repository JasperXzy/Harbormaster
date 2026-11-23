package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestRunOnceDoesNotExecuteOrClaimLocalProbes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("HM_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	t.Setenv("HM_MASTER_KEY_FILE", "")
	t.Setenv("HM_DEV", "false")
	s, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.EnqueueServerProbe(ctx, strings.Repeat("k", 32), netprobe.Request{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 1, TimeoutMS: 1000, IntervalMS: 100}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	var out, logs bytes.Buffer
	if code := Server(ctx, []string{"run-once", "--data-dir", dir}, strings.NewReader(""), &out, &logs); code != 0 {
		t.Fatal(code, logs.String())
	}
	s, err = store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.Job(ctx, j.ID)
	if err != nil || job.Status != "queued" || job.Attempts != 0 {
		t.Fatal("run-once consumed probe", job, err)
	}
}
