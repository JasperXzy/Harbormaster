package agentproto

import (
	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"time"
)

const TraceClaimPath = "/agent/v1/traces/claim"
const TraceResultPath = "/agent/v1/traces/result"

// Claims/acks share the fixed identity/lease format. Request and result bodies
// deliberately use separate endpoints so old agents cannot consume traces.
type TraceTask struct {
	ID        string                `json:"id"`
	Owner     string                `json:"owner"`
	Request   netprobe.TraceRequest `json:"request"`
	ExpiresAt time.Time             `json:"expires_at"`
}
type TraceClaimed struct {
	Task       *TraceTask `json:"task"`
	ServerTime time.Time  `json:"server_time"`
}
type TraceReport struct {
	AgentID   string               `json:"agent_id"`
	TaskID    string               `json:"task_id"`
	Owner     string               `json:"owner"`
	Result    netprobe.TraceResult `json:"result"`
	ErrorCode *string              `json:"error_code"`
}
