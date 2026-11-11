package agentstate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func testBox(t *testing.T) *security.Box {
	t.Helper()
	b, err := security.NewBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func testIdentity() Identity {
	return Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "test-token-never-real-but-private"}
}
func sample(seq int64) agentproto.Report {
	return agentproto.Report{Version: 1, AgentID: "agent-test", CounterEpoch: "epoch", Sequence: seq, SampledAt: time.Now().UTC(), UptimeMS: seq * 10000, RXBytes: seq * 100, TXBytes: seq * 50}
}
func TestEncryptedJournalRecoveryAckAndCompaction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	box := testBox(t)
	j, err := Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, box); err == nil {
		t.Fatal("second process acquired journal")
	}
	if err = j.SetIdentity(testIdentity()); err != nil {
		t.Fatal(err)
	}
	if err = j.Append("kernel", sample(1)); err != nil {
		t.Fatal(err)
	}
	if err = j.Append("kernel", sample(2)); err != nil {
		t.Fatal(err)
	}
	j.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(testIdentity().Token)) || bytes.Contains(raw, []byte("panel.example")) || bytes.Contains(raw, []byte("rx_bytes")) {
		t.Fatal("plaintext in state")
	}
	// Crash while writing the next frame: preserve committed samples only.
	f, err := os.OpenFile(filepath.Join(dir, "state.enc"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte{0, 0, 1, 0, 3, 5}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	j, err = Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if v := j.View(); len(v.Queue) != 2 || v.Last.Sequence != 2 || v.Identity.Token != testIdentity().Token {
		t.Fatal("recovery lost state")
	}
	if err = j.Ack(agentproto.Ack{CounterEpoch: "different", AcceptedSequence: 2}); err != nil {
		t.Fatal(err)
	}
	if len(j.Batch(100)) != 2 {
		t.Fatal("wrong epoch cleared queue")
	}
	j.records = 999 // Force next operation through the real compaction path.
	if err = j.Ack(agentproto.Ack{CounterEpoch: "epoch", AcceptedSequence: 1}); err != nil {
		t.Fatal(err)
	}
	if err = j.Append("kernel", sample(3)); err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	v := j.View()
	if len(v.Queue) != 2 || v.Queue[0].Sequence != 2 || v.Last.Sequence != 3 {
		t.Fatal("compaction lost ack or following append")
	}
}
func TestAuthenticatedCorruptionFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	box := testBox(t)
	j, err := Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.SetIdentity(testIdentity()); err != nil {
		t.Fatal(err)
	}
	j.Close()
	path := filepath.Join(dir, "state.enc")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, box); err == nil {
		t.Fatal("authenticated corruption accepted as incomplete tail")
	}
}
func TestBoundedQueueReportsGapWithoutPoisoningLaterSamples(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "state"), testBox(t))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.SetIdentity(testIdentity()); err != nil {
		t.Fatal(err)
	}
	// Build a valid full snapshot without 8640 fsync calls in this unit test.
	j.state.Queue = make([]agentproto.Report, MaxSamples)
	for i := range j.state.Queue {
		j.state.Queue[i] = sample(int64(i + 1))
	}
	last := j.state.Queue[MaxSamples-1]
	j.state.Last = &last
	if err = j.compact(); err != nil {
		t.Fatal(err)
	}
	if err = j.Append("kernel", sample(MaxSamples+1)); err != nil {
		t.Fatal(err)
	}
	v := j.View()
	if len(v.Queue) != MaxSamples || v.Queue[0].Sequence != 2 || v.Last.DroppedSamples != 1 || v.Dropped != 1 {
		t.Fatal("overflow not tracked")
	}
	if err = j.Ack(agentproto.Ack{CounterEpoch: "epoch", AcceptedSequence: MaxSamples + 1}); err != nil {
		t.Fatal(err)
	}
	if err = j.Append("kernel", sample(MaxSamples+2)); err != nil {
		t.Fatal(err)
	}
	if j.Meta().Last.DroppedSamples != 0 {
		t.Fatal("historical overflow poisoned new samples")
	}
}

func TestExpiredBacklogGapSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	box := testBox(t)
	j, err := Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.SetIdentity(testIdentity()); err != nil {
		t.Fatal(err)
	}
	old := sample(1)
	old.SampledAt = time.Now().Add(-8 * 24 * time.Hour)
	if err = j.Append("kernel", old); err != nil {
		t.Fatal(err)
	}
	if count, err := j.PruneBefore(time.Now().Add(-7 * 24 * time.Hour)); count != 1 || err != nil {
		t.Fatal(count, err)
	}
	j.Close()
	j, err = Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.Append("kernel", sample(2)); err != nil {
		t.Fatal(err)
	}
	if state := j.Meta(); state.Last.DroppedSamples != 1 || state.PendingDropped != 0 || state.Dropped != 1 {
		t.Fatal("pruned data lost quality marker")
	}
}
