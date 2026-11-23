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

// TraceOnce is serialized independently of Sample and Flush. A durable owner
// precedes claim; a durable task precedes execution; a durable result precedes
// every upload. Recovered started tasks are never executed a second time.
func (c *Client) TraceOnce(ctx context.Context) (bool, error) {
	c.networkMu.Lock()
	defer c.networkMu.Unlock()
	return c.traceOnce(ctx)
}
func (c *Client) traceOnce(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, nil
	}
	identity := c.Journal.Meta().Identity
	if identity.AgentID == "" || identity.Token == "" || identity.Registration != nil {
		return false, ErrCredentials
	}
	work := c.Journal.Meta().Trace
	if work == nil {
		var bytes [32]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return false, ErrProtocol
		}
		if err := c.Journal.BeginTrace(hex.EncodeToString(bytes[:])); err != nil {
			return false, err
		}
		work = c.Journal.Meta().Trace
	}
	if work.Report != nil {
		return c.sendTraceResult(ctx, identity.Token, *work.Report)
	}
	if work.Task != nil {
		// Process exit, or failure between saving the start marker and the result.
		now := time.Now().UTC()
		code := "probe_interrupted"
		report := agentproto.TraceReport{AgentID: identity.AgentID, TaskID: work.Task.ID, Owner: work.Owner, ErrorCode: &code, Result: netprobe.TraceResult{StartedAt: now, FinishedAt: now, ResolvedAddresses: []string{}, Hops: []netprobe.TraceHop{}}}
		if err := c.Journal.FinishTrace(work.Owner, report); err != nil {
			return false, err
		}
		return c.sendTraceResult(ctx, identity.Token, *c.Journal.Meta().Trace.Report)
	}
	started := time.Now()
	var reply agentproto.TraceClaimed
	if err := c.post(ctx, agentproto.TraceClaimPath, identity.Token, agentproto.ProbeClaim{AgentID: identity.AgentID, Owner: work.Owner}, &reply); err != nil {
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
	if err := c.Journal.StartTrace(work.Owner, *task); err != nil {
		return false, err
	}
	// Derive a monotonic execution deadline from server remaining lease time.
	// Subtract the entire claim RTT and a five-second margin; never use the
	// agent's wall clock to extend the server lease.
	remaining := task.ExpiresAt.Sub(reply.ServerTime) - time.Since(started) - 5*time.Second
	now := time.Now().UTC()
	result := netprobe.TraceResult{StartedAt: now, FinishedAt: now, ResolvedAddresses: []string{}, Hops: []netprobe.TraceHop{}}
	var traceErr error
	if remaining <= 0 || ctx.Err() != nil {
		traceErr = netprobe.ErrCancelled
	} else {
		runCtx, cancel := context.WithTimeout(ctx, min(remaining, 125*time.Second))
		run := c.traceRun
		if run == nil {
			run = netprobe.Trace
		}
		result, traceErr = run(runCtx, task.Request)
		cancel()
	}
	report := agentproto.TraceReport{AgentID: identity.AgentID, TaskID: task.ID, Owner: work.Owner, Result: result}
	if traceErr != nil {
		code := netprobe.FailureCode(traceErr)
		report.ErrorCode = &code
	}
	if netprobe.ValidateTraceResult(task.Request, &report.Result, report.ErrorCode, time.Now()) != nil {
		return false, ErrProtocol
	}
	if err := c.Journal.FinishTrace(work.Owner, report); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return true, nil
	} // Result remains encrypted for next start.
	return c.sendTraceResult(ctx, identity.Token, *c.Journal.Meta().Trace.Report)
}
func (c *Client) sendTraceResult(ctx context.Context, token string, report agentproto.TraceReport) (bool, error) {
	var ack agentproto.ProbeAck
	err := c.post(ctx, agentproto.TraceResultPath, token, report, &ack)
	if errors.Is(err, ErrProbeExpired) {
		return true, c.Journal.ClearTrace(report.Owner)
	}
	if err != nil {
		return false, err
	}
	if ack.TaskID != report.TaskID {
		return false, ErrProtocol
	}
	return true, c.Journal.ClearTrace(report.Owner)
}
