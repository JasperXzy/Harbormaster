// Package agentstate is an encrypted, bounded, crash-recoverable agent journal.
// One fsynced record commits either a sample and its sequence or an ACK cursor.
package agentstate

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/security"
)

const MaxSamples = 8640 // 24 hours at the default 10-second interval.
const maxRecord = 16 << 20
const owner = "harbormaster:agent-journal:v1"

var ErrState = errors.New("agent_state_invalid")

type Identity struct {
	Server       string                   `json:"server"`
	AgentID      string                   `json:"agent_id,omitempty"`
	Token        string                   `json:"token,omitempty"`
	Registration *agentproto.Registration `json:"registration,omitempty"`
}
type State struct {
	Probe          *ProbeWork          `json:"probe,omitempty"`
	Identity       Identity            `json:"identity"`
	Generation     string              `json:"generation"`
	Last           *agentproto.Report  `json:"last"`
	Queue          []agentproto.Report `json:"queue"`
	Dropped        int64               `json:"dropped"`
	PendingDropped int64               `json:"pending_dropped"`
}
type operation struct {
	Probe       *ProbeWork         `json:"probe,omitempty"`
	ClearProbe  string             `json:"clear_probe,omitempty"`
	Snapshot    *State             `json:"snapshot,omitempty"`
	Identity    *Identity          `json:"identity,omitempty"`
	Sample      *agentproto.Report `json:"sample,omitempty"`
	Generation  string             `json:"generation,omitempty"`
	Ack         *agentproto.Ack    `json:"ack,omitempty"`
	PruneBefore *time.Time         `json:"prune_before,omitempty"`
}
type Journal struct {
	mu      sync.Mutex
	box     *security.Box
	path    string
	file    *os.File
	lock    *os.File
	state   State
	records int
}

func Open(dir string, box *security.Box) (*Journal, error) {
	if box == nil {
		return nil, ErrState
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, ErrState
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return nil, ErrState
	}
	lock, err := lockDirectory(dir)
	if err != nil {
		return nil, err
	}
	j := &Journal{box: box, path: filepath.Join(dir, "state.enc"), lock: lock}
	f, err := os.OpenFile(j.path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		lock.Close()
		return nil, ErrState
	}
	j.file = f
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		j.Close()
		return nil, ErrState
	}
	var offset int64
	for {
		var size [4]byte
		_, err = io.ReadFull(f, size[:])
		if err == io.EOF {
			break
		}
		if err == io.ErrUnexpectedEOF {
			err = f.Truncate(offset)
			break
		}
		if err != nil {
			break
		}
		n := binary.BigEndian.Uint32(size[:])
		if n == 0 || n > maxRecord {
			err = ErrState
			break
		}
		sealed := make([]byte, n)
		_, err = io.ReadFull(f, sealed)
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			err = f.Truncate(offset)
			break
		}
		if err != nil {
			break
		}
		var raw []byte
		raw, err = box.Decrypt(sealed, owner)
		if err != nil {
			err = ErrState
			break
		}
		var op operation
		err = json.Unmarshal(raw, &op)
		clear(raw)
		if err != nil {
			err = ErrState
			break
		}
		if err = j.apply(op); err != nil {
			break
		}
		j.records++
		offset += int64(n) + 4
	}
	if err != nil && err != io.EOF {
		j.Close()
		return nil, ErrState
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		j.Close()
		return nil, ErrState
	}
	return j, nil
}
func (j *Journal) Close() error {
	if j.file != nil {
		_ = j.file.Close()
	}
	if j.lock != nil {
		return j.lock.Close()
	}
	return nil
}
func (j *Journal) View() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	// Copy pointer fields too: callers may never mutate the durable state in place.
	raw, _ := json.Marshal(j.state)
	var state State
	_ = json.Unmarshal(raw, &state)
	clear(raw)
	return state
}

// Meta omits the backlog so normal sampling does not copy a day's reports.
func (j *Journal) Meta() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.state
	s.Queue = nil
	raw, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(raw, &out)
	clear(raw)
	return out
}
func (j *Journal) Batch(limit int) []agentproto.Report {
	j.mu.Lock()
	defer j.mu.Unlock()
	if limit < 1 || limit > agentproto.MaxBatch {
		limit = agentproto.MaxBatch
	}
	if len(j.state.Queue) < limit {
		limit = len(j.state.Queue)
	}
	raw, _ := json.Marshal(j.state.Queue[:limit])
	var out []agentproto.Report
	_ = json.Unmarshal(raw, &out)
	clear(raw)
	return out
}
func (j *Journal) SetIdentity(identity Identity) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Identity.Server != "" && identity.Server != j.state.Identity.Server {
		return ErrState
	}
	if len(j.state.Queue) > 0 && j.state.Identity.AgentID != "" && identity.AgentID != j.state.Identity.AgentID {
		return ErrState
	}
	if identity.Registration != nil {
		copy := *identity.Registration
		identity.Registration = &copy
	}
	return j.write(operation{Identity: &identity})
}
func (j *Journal) Append(generation string, sample agentproto.Report) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if generation == "" || sample.AgentID != j.state.Identity.AgentID || sample.Sequence < 1 {
		return ErrState
	}
	if last := j.state.Last; last != nil && sample.CounterEpoch == last.CounterEpoch && sample.Sequence <= last.Sequence {
		return ErrState
	}
	if sample.FamilyCounters != nil {
		copy := *sample.FamilyCounters
		sample.FamilyCounters = &copy
	}
	sample.DroppedSamples = j.state.PendingDropped
	if len(j.state.Queue) == MaxSamples {
		sample.DroppedSamples++
	}
	return j.write(operation{Sample: &sample, Generation: generation})
}
func (j *Journal) PruneBefore(before time.Time) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	count := 0
	for _, r := range j.state.Queue {
		if r.SampledAt.Before(before) {
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	return count, j.write(operation{PruneBefore: &before})
}
func (j *Journal) Ack(ack agentproto.Ack) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, r := range j.state.Queue {
		if r.CounterEpoch == ack.CounterEpoch && r.Sequence == ack.AcceptedSequence {
			return j.write(operation{Ack: &ack})
		}
	}
	// A batch may have fallen off the bounded queue while in flight.
	return nil
}
func (j *Journal) apply(op operation) error {
	count := 0
	if op.Probe != nil {
		count++
	}
	if op.ClearProbe != "" {
		count++
	}
	if op.Snapshot != nil {
		count++
	}
	if op.Identity != nil {
		count++
	}
	if op.Sample != nil {
		count++
	}
	if op.Ack != nil {
		count++
	}
	if op.PruneBefore != nil {
		count++
	}
	if count != 1 {
		return ErrState
	}
	switch {
	case op.Probe != nil:
		j.state.Probe = op.Probe
	case op.ClearProbe != "":
		if j.state.Probe == nil || j.state.Probe.Owner != op.ClearProbe {
			return ErrState
		}
		j.state.Probe = nil
	case op.Snapshot != nil:
		if len(op.Snapshot.Queue) > MaxSamples || op.Snapshot.Dropped < 0 {
			return ErrState
		}
		j.state = *op.Snapshot
	case op.Identity != nil:
		if op.Identity.AgentID != j.state.Identity.AgentID || op.Identity.Token != j.state.Identity.Token || op.Identity.Registration != nil {
			j.state.Probe = nil
		}
		j.state.Identity = *op.Identity
	case op.Sample != nil:
		j.state.PendingDropped = 0
		if len(j.state.Queue) == MaxSamples {
			j.state.Queue = j.state.Queue[1:]
			j.state.Dropped++
		}
		j.state.Queue = append(j.state.Queue, *op.Sample)
		copy := *op.Sample
		j.state.Last = &copy
		j.state.Generation = op.Generation
	case op.Ack != nil:
		for i, r := range j.state.Queue {
			if r.CounterEpoch == op.Ack.CounterEpoch && r.Sequence == op.Ack.AcceptedSequence {
				j.state.Queue = j.state.Queue[i+1:]
				break
			}
		}
	case op.PruneBefore != nil:
		kept := make([]agentproto.Report, 0, len(j.state.Queue))
		for _, r := range j.state.Queue {
			if r.SampledAt.Before(*op.PruneBefore) {
				j.state.Dropped++
				j.state.PendingDropped++
			} else {
				kept = append(kept, r)
			}
		}
		j.state.Queue = kept
	}
	return nil
}
func (j *Journal) frame(op operation) ([]byte, error) {
	raw, err := json.Marshal(op)
	if err != nil {
		return nil, ErrState
	}
	defer clear(raw)
	sealed, err := j.box.Encrypt(raw, owner)
	if err != nil || len(sealed) > maxRecord {
		return nil, ErrState
	}
	frame := make([]byte, 4+len(sealed))
	binary.BigEndian.PutUint32(frame, uint32(len(sealed)))
	copy(frame[4:], sealed)
	return frame, nil
}
func (j *Journal) write(op operation) error {
	frame, err := j.frame(op)
	if err != nil {
		return err
	}
	if _, err = j.file.Write(frame); err != nil {
		return ErrState
	}
	if err = j.file.Sync(); err != nil {
		return ErrState
	}
	if err = j.apply(op); err != nil {
		return err
	}
	j.records++
	if j.records >= 1000 {
		return j.compact()
	}
	return nil
}
func (j *Journal) compact() error {
	frame, err := j.frame(operation{Snapshot: &j.state})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(j.path), ".agent-state-*")
	if err != nil {
		return ErrState
	}
	name := f.Name()
	defer os.Remove(name)
	transferred := false
	defer func() {
		if !transferred {
			_ = f.Close()
		}
	}()
	if _, err = f.Write(frame); err != nil {
		return ErrState
	}
	if err = f.Sync(); err != nil {
		return ErrState
	}
	if err = os.Rename(name, j.path); err != nil {
		return ErrState
	}
	// Keep writing to the renamed file, never the unlinked old journal.
	old := j.file
	j.file = f
	transferred = true
	j.records = 1
	_ = old.Close()
	dir, err := os.Open(filepath.Dir(j.path))
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrState
	}
	return nil
}
