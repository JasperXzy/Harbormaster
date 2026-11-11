package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) agentManagement(w http.ResponseWriter, r *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
	if !strings.HasPrefix(path, "/instances/") || len(parts) != 3 || parts[1] != "agent" || parts[2] != "token" {
		return false
	}
	if r.Method != "DELETE" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	if err := p.s.RevokeAgent(r.Context(), parts[0], p.now()); err != nil {
		businessError(w, err)
	} else {
		w.WriteHeader(204)
	}
	return true
}

func (p *productionPanel) overlayAgents(ctx context.Context, items []map[string]any) error {
	views, err := p.s.AgentViews(ctx, p.now())
	if err != nil {
		return err
	}
	for _, item := range items {
		id, _ := item["id"].(string)
		v, ok := views[id]
		if !ok {
			v.Agent = model.AgentStatus{Status: "not_installed"}
		}
		item["agent"] = v.Agent
		item["realtime"] = v.Realtime
		// Until an aligned CDT basis exists, online does not mean protected.
		item["realtime_protection"] = "unprotected"
		if eligible, ok := item["counts_toward_cdt"].(bool); ok && !eligible {
			item["realtime_protection"] = "not_applicable"
		}
	}
	return nil
}

// SSE coalesces changes within five seconds. Reconnecting clients re-fetch the
// snapshot; no report bodies or credentials are sent to the browser.
func (p *productionPanel) agentEvents(ctx context.Context, previous map[string]string, send func(string, any) bool) bool {
	items, err := p.s.Snapshots(ctx, []string{"ecs", "swas"}, "")
	if err != nil {
		return false
	}
	if err = p.overlayAgents(ctx, items); err != nil {
		return false
	}
	for _, item := range items {
		id, _ := item["id"].(string)
		for _, event := range []string{"agent.status", "instance.realtime"} {
			payload := map[string]any{"instance_id": id}
			if event == "agent.status" {
				payload["agent"] = item["agent"]
				payload["realtime_protection"] = item["realtime_protection"]
			} else {
				payload["realtime"] = item["realtime"]
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return false
			}
			key := event + ":" + id
			if previous[key] != string(raw) {
				if !send(event, payload) {
					return false
				}
				previous[key] = string(raw)
			}
		}
	}
	return true
}
