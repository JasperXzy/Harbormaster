package agentstate

import (
	"encoding/json"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

// Task presence is the durable start marker, written before opening a socket.
// A recovered task without Report must be reported interrupted, never re-run.
type ProbeWork struct {
	Owner  string                  `json:"owner"`
	Task   *agentproto.ProbeTask   `json:"task,omitempty"`
	Report *agentproto.ProbeReport `json:"report,omitempty"`
}

func (j *Journal) BeginProbe(owner string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Probe != nil || j.state.Identity.Token == "" || !(agentproto.ProbeClaim{AgentID: j.state.Identity.AgentID, Owner: owner}).Valid() {
		return ErrState
	}
	return j.write(operation{Probe: &ProbeWork{Owner: owner}})
}
func (j *Journal) StartProbe(owner string, task agentproto.ProbeTask) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.state.Probe
	if current == nil || current.Owner != owner || current.Task != nil || task.Owner != owner || task.ID == "" || len(task.ID) > 128 || task.ExpiresAt.IsZero() || task.Request.Validate() != nil {
		return ErrState
	}
	return j.write(operation{Probe: &ProbeWork{Owner: owner, Task: &task}})
}
func (j *Journal) FinishProbe(owner string, report agentproto.ProbeReport) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.state.Probe
	if current == nil || current.Owner != owner || current.Task == nil || current.Report != nil || report.AgentID != j.state.Identity.AgentID || report.Owner != owner || report.TaskID != current.Task.ID {
		return ErrState
	}
	// Own the nested slices/pointers; a caller cannot mutate a committed frame.
	raw, err := json.Marshal(report)
	if err != nil {
		return ErrState
	}
	defer clear(raw)
	var owned agentproto.ProbeReport
	if json.Unmarshal(raw, &owned) != nil || netprobe.ValidateResult(current.Task.Request, &owned.Result, owned.ErrorCode, time.Now()) != nil {
		return ErrState
	}
	return j.write(operation{Probe: &ProbeWork{Owner: owner, Task: current.Task, Report: &owned}})
}
func (j *Journal) ClearProbe(owner string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Probe == nil || j.state.Probe.Owner != owner {
		return ErrState
	}
	return j.write(operation{ClearProbe: owner})
}
