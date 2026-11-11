package agentproto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"time"
)

const RegisterPath = "/agent/v1/register"
const BatchPath = "/agent/v1/reports/batch"
const MaxBatch = 100

type Metadata struct {
	InstanceID string `json:"instance_id"`
	RegionID   string `json:"region_id"`
}
type Registration struct {
	Version      int      `json:"version"`
	Code         string   `json:"registration_code"`
	Nonce        string   `json:"nonce"`
	Metadata     Metadata `json:"metadata"`
	AgentVersion string   `json:"agent_version"`
}
type Registered struct {
	AgentID               string    `json:"agent_id"`
	TokenScheme           string    `json:"token_scheme"`
	ReportIntervalSeconds int       `json:"report_interval_seconds"`
	ServerTime            time.Time `json:"server_time"`
}
type Batch struct {
	Reports []Report `json:"reports"`
}

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var regionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`)

func (r Registration) Validate() error {
	if r.Version != 1 || len(r.Code) < 32 || len(r.Code) > 128 || len(r.Nonce) != 64 || len(r.AgentVersion) > 64 || !identityPattern.MatchString(r.Metadata.InstanceID) || !regionIDPattern.MatchString(r.Metadata.RegionID) || len(r.Metadata.RegionID) > 64 {
		return errors.New("invalid_registration")
	}
	if _, err := hex.DecodeString(r.Nonce); err != nil {
		return errors.New("invalid_registration")
	}
	return nil
}

// The client saves its nonce and code encrypted before sending registration.
// Both sides derive the same high-entropy token, allowing an identical retry
// after response loss without storing a recoverable long-lived token on server.
func DeriveToken(code, nonce string) string {
	h := hmac.New(sha256.New, []byte(code))
	h.Write([]byte("harbormaster-agent-token-v1\x00" + nonce))
	return "hm_agent_" + hex.EncodeToString(h.Sum(nil))
}
