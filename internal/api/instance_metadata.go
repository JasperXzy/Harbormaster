package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) instanceMetadata(w http.ResponseWriter, r *http.Request, path string) bool {
	if !strings.HasPrefix(path, "/instances/") || !strings.HasSuffix(path, "/metadata") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "metadata" {
		return false
	}
	if r.Method != "PATCH" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	var raw map[string]json.RawMessage
	if !readBody(w, r, &raw) {
		return true
	}
	var in model.InstanceMetadataUpdate
	for k, v := range raw {
		switch k {
		case "note":
			var s string
			if string(v) == "null" || json.Unmarshal(v, &s) != nil {
				fieldError(w, "note", "invalid_format")
				return true
			}
			in.Note = &s
		case "group":
			in.GroupSet = true
			if string(v) != "null" {
				var s string
				if json.Unmarshal(v, &s) != nil {
					fieldError(w, "group", "invalid_format")
					return true
				}
				in.Group = &s
			}
		case "tags":
			if string(v) == "null" {
				fieldError(w, "tags", "invalid_format")
				return true
			}
			var tags []string
			if json.Unmarshal(v, &tags) != nil {
				fieldError(w, "tags", "invalid_format")
				return true
			}
			in.Tags = &tags
		default:
			writeError(w, 422, "validation_failed")
			return true
		}
	}
	meta, err := p.s.UpdateInstanceMetadata(r.Context(), parts[0], in, p.now().UTC())
	var validation *store.SettingsValidationError
	if errors.As(err, &validation) {
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": validation.Fields}})
		return true
	}
	if err != nil {
		businessError(w, err)
		return true
	}
	writeJSON(w, 200, meta)
	return true
}

func (p *productionPanel) overlayAndFilterMetadata(ctx context.Context, items []map[string]any, groupFilter, tagFilter string) ([]map[string]any, error) {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item["id"].(string))
	}
	metaMap, err := p.s.InstanceMetadataMap(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, ok := metaMap[item["id"].(string)]
		if !ok {
			m = model.InstanceMetadata{Note: "", Tags: []string{}}
		}
		item["note"] = m.Note
		item["group"] = m.Group
		item["tags"] = m.Tags
		if groupFilter != "" && (m.Group == nil || *m.Group != groupFilter) {
			continue
		}
		if tagFilter != "" && !slices.Contains(m.Tags, tagFilter) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}
