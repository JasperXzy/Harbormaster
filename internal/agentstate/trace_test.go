package agentstate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func TestTraceJournalDurabilityOwnershipAndTrafficIsolation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	box := testBox(t)
	j, err := Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { j.Close() }()
	if err = j.SetIdentity(testIdentity()); err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 64)
	probeOwner := strings.Repeat("b", 64)
	if err = j.BeginProbe(probeOwner); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginTrace(owner); err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if j.Meta().Trace == nil || j.Meta().Trace.Owner != owner || j.Meta().Trace.Task != nil {
		t.Fatal("claim marker lost")
	}
	now := time.Now().UTC()
	q := netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 1, TimeoutMS: 1000, MaxHops: 30}
	task := agentproto.TraceTask{ID: "job_probe", Owner: owner, Request: q, ExpiresAt: now.Add(5 * time.Minute)}
	if err = j.StartTrace("wrong-owner", task); err != ErrState {
		t.Fatal(err)
	}
	if err = j.StartTrace(owner, task); err != nil {
		t.Fatal(err)
	}
	if err = j.StartTrace(owner, task); err != ErrState {
		t.Fatal("started twice", err)
	}
	if err = j.Append("kernel", sample(1)); err != nil {
		t.Fatal(err)
	}
	j.records = 999
	report := agentproto.TraceReport{AgentID: testIdentity().AgentID, TaskID: task.ID, Owner: owner, Result: netprobe.TraceResult{StartedAt: now, FinishedAt: now, Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{{Sequence: 1, StartedAt: now, Address: new("1.1.1.1"), RTTMS: new(float64(2)), Reply: new("tcp_reset")}}}}}}
	if err = j.FinishTrace(owner, report); err != nil {
		t.Fatal(err)
	}
	*report.Result.Hops[0].Samples[0].RTTMS = 999 // cannot mutate the committed in-memory copy.
	if err = j.Ack(agentproto.Ack{CounterEpoch: "epoch", AcceptedSequence: 1}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	state := j.View()
	if len(state.Queue) != 0 || state.Trace == nil || state.Trace.Report == nil || *state.Trace.Report.Result.Hops[0].Samples[0].RTTMS != 2 || !state.Trace.Report.Result.Reached || state.Probe == nil || state.Probe.Owner != probeOwner {
		t.Fatal("probe/traffic state lost")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testIdentity().Token, owner, "job_probe", "1.1.1.1"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext probe state")
		}
	}
	if err = j.ClearTrace("wrong-owner"); err != ErrState {
		t.Fatal(err)
	}
	if err = j.ClearTrace(owner); err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = Open(dir, box)
	if err != nil || j.Meta().Trace != nil || j.Meta().Probe == nil || j.Meta().Probe.Owner != probeOwner {
		t.Fatal("ack not durable", err)
	}
	if err = j.BeginTrace(owner); err != nil {
		t.Fatal(err)
	}
	rotated := testIdentity()
	rotated.Token = "replacement-test-token"
	if err = j.SetIdentity(rotated); err != nil || j.Meta().Trace != nil || j.Meta().Probe != nil {
		t.Fatal("old binding retained", err)
	}
}
