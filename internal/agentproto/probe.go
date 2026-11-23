package agentproto

import (
	"encoding/hex"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

const ProbeClaimPath = "/agent/v1/probes/claim"
const ProbeResultPath = "/agent/v1/probes/result"

type ProbeClaim struct {
	AgentID string `json:"agent_id"`
	Owner   string `json:"owner"`
}

func (c ProbeClaim) Valid() bool {
	raw, err := hex.DecodeString(c.Owner)
	return len(c.AgentID) > 0 && len(c.AgentID) <= 128 && err == nil && len(raw) == 32
}

type ProbeTask struct {
	ID        string           `json:"id"`
	Owner     string           `json:"owner"`
	Request   netprobe.Request `json:"request"`
	ExpiresAt time.Time        `json:"expires_at"`
}
type ProbeClaimed struct {
	Task       *ProbeTask `json:"task"`
	ServerTime time.Time  `json:"server_time"`
}
type ProbeReport struct {
	AgentID   string          `json:"agent_id"`
	TaskID    string          `json:"task_id"`
	Owner     string          `json:"owner"`
	Result    netprobe.Result `json:"result"`
	ErrorCode *string         `json:"error_code"`
}
type ProbeAck struct {
	TaskID    string `json:"task_id"`
	Duplicate bool   `json:"duplicate"`
}
