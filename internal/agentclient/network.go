package agentclient

import "context"

// NetworkOnce alternates fixed measurement actions under one lock. Neither a
// slow trace nor a backlog of probes can run concurrently or starve the other
// action. Traffic sampling and delivery never acquire this lock.
func (c *Client) NetworkOnce(ctx context.Context) (bool, error) {
	c.networkMu.Lock()
	defer c.networkMu.Unlock()
	if ctx.Err() != nil {
		return false, nil
	}
	trace := c.nextTrace
	c.nextTrace = !trace
	if trace {
		return c.traceOnce(ctx)
	}
	return c.probeOnce(ctx)
}
