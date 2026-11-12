package agentclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

var ErrProbeUnsupported = errors.New("agent_probe_protocol_unavailable")
var ErrProbeExpired = errors.New("agent_probe_task_expired")

// ProbeOnce is serialized independently of Sample and Flush. A durable owner
// precedes claim; a durable task precedes execution; a durable result precedes
// every upload. Recovered started tasks are never executed a second time.
func (c *Client) ProbeOnce(ctx context.Context) (bool, error) {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if ctx.Err() != nil {
		return false, nil
	}
	identity := c.Journal.Meta().Identity
	if identity.AgentID == "" || identity.Token == "" || identity.Registration != nil {
		return false, ErrCredentials
	}
	work := c.Journal.Meta().Probe
	if work == nil {
		var bytes [32]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return false, ErrProtocol
		}
		if err := c.Journal.BeginProbe(hex.EncodeToString(bytes[:])); err != nil {
			return false, err
		}
		work = c.Journal.Meta().Probe
	}
	if work.Report != nil {
		return c.sendProbeResult(ctx, identity.Token, *work.Report)
	}
	if work.Task != nil {
		// Process exit, or failure between saving the start marker and the result.
		now := time.Now().UTC()
		code := "probe_interrupted"
		report := agentproto.ProbeReport{AgentID: identity.AgentID, TaskID: work.Task.ID, Owner: work.Owner, ErrorCode: &code, Result: netprobe.Result{StartedAt: now, FinishedAt: now, ResolvedAddresses: []string{}, Samples: []netprobe.Sample{}}}
		if err := c.Journal.FinishProbe(work.Owner, report); err != nil {
			return false, err
		}
		return c.sendProbeResult(ctx, identity.Token, *c.Journal.Meta().Probe.Report)
	}
	started := time.Now()
	var reply agentproto.ProbeClaimed
	if err := c.post(ctx, agentproto.ProbeClaimPath, identity.Token, agentproto.ProbeClaim{AgentID: identity.AgentID, Owner: work.Owner}, &reply); err != nil {
		return false, err
	}
	if reply.ServerTime.IsZero() {
		return false, ErrProtocol
	}
	if reply.Task == nil {
		return false, nil
	}
	task := reply.Task
	if task.Owner != work.Owner || task.ID == "" || len(task.ID) > 128 || task.Request.Validate() != nil || !task.ExpiresAt.After(reply.ServerTime) || task.ExpiresAt.Sub(reply.ServerTime) > 5*time.Minute {
		return false, ErrProtocol
	}
	if err := c.Journal.StartProbe(work.Owner, *task); err != nil {
		return false, err
	}
	// Derive a monotonic execution deadline from server remaining lease time.
	// Subtract the entire claim RTT and a five-second margin; never use the
	// agent's wall clock to extend the server lease.
	remaining := task.ExpiresAt.Sub(reply.ServerTime) - time.Since(started) - 5*time.Second
	now := time.Now().UTC()
	result := netprobe.Result{StartedAt: now, FinishedAt: now, ResolvedAddresses: []string{}, Samples: []netprobe.Sample{}}
	var probeErr error
	if remaining <= 0 || ctx.Err() != nil {
		probeErr = netprobe.ErrCancelled
	} else {
		runCtx, cancel := context.WithTimeout(ctx, min(remaining, 65*time.Second))
		run := c.probeRun
		if run == nil {
			run = netprobe.Run
		}
		result, probeErr = run(runCtx, task.Request)
		cancel()
	}
	report := agentproto.ProbeReport{AgentID: identity.AgentID, TaskID: task.ID, Owner: work.Owner, Result: result}
	if probeErr != nil {
		code := probeErrorCode(probeErr)
		report.ErrorCode = &code
	}
	if netprobe.ValidateResult(task.Request, &report.Result, report.ErrorCode, time.Now()) != nil {
		return false, ErrProtocol
	}
	if err := c.Journal.FinishProbe(work.Owner, report); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return true, nil
	} // Result remains encrypted for next start.
	return c.sendProbeResult(ctx, identity.Token, *c.Journal.Meta().Probe.Report)
}
func (c *Client) sendProbeResult(ctx context.Context, token string, report agentproto.ProbeReport) (bool, error) {
	var ack agentproto.ProbeAck
	err := c.post(ctx, agentproto.ProbeResultPath, token, report, &ack)
	if errors.Is(err, ErrProbeExpired) {
		return true, c.Journal.ClearProbe(report.Owner)
	}
	if err != nil {
		return false, err
	}
	if ack.TaskID != report.TaskID {
		return false, ErrProtocol
	}
	return true, c.Journal.ClearProbe(report.Owner)
}
func probeErrorCode(err error) string {
	for _, known := range []error{netprobe.ErrInvalid, netprobe.ErrTarget, netprobe.ErrDNS, netprobe.ErrUnavailable, netprobe.ErrIO, netprobe.ErrCancelled} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return netprobe.ErrIO.Error()
}
