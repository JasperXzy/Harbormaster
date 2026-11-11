package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func businessError(w http.ResponseWriter, err error) {
	var validation *collect.ValidationError
	switch {
	case errors.As(err, &validation):
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": validation.Fields}})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, store.ErrNameTaken):
		writeError(w, 409, "account_name_taken")
	case errors.Is(err, store.ErrAccountDisabled):
		writeError(w, 409, "account_disabled")
	default:
		writeError(w, 503, "internal_error")
	}
}
func (p *productionPanel) business(w http.ResponseWriter, r *http.Request, path string) bool {
	if p.engine == nil {
		return false
	}
	if p.traffic(w, r, path) {
		return true
	}
	switch {
	case path == "/accounts":
		switch r.Method {
		case "GET":
			a, err := p.s.Accounts(r.Context())
			if err != nil {
				businessError(w, err)
			} else {
				writeJSON(w, 200, map[string]any{"items": a})
			}
		case "POST":
			var in model.AccountCreate
			if !readBody(w, r, &in) {
				return true
			}
			a, j, err := p.engine.CreateAccount(r.Context(), in)
			if err != nil {
				businessError(w, err)
			} else {
				writeJSON(w, 201, map[string]any{"account": a, "permission_check_job_id": j.ID})
			}
		default:
			writeError(w, 405, "method_not_allowed")
		}
	case strings.HasPrefix(path, "/accounts/"):
		parts := strings.Split(strings.TrimPrefix(path, "/accounts/"), "/")
		id := parts[0]
		if len(parts) == 2 && parts[1] == "permission-check" && r.Method == "POST" {
			j, err := p.engine.Enqueue(r.Context(), "permission_check", id)
			if err != nil {
				businessError(w, err)
			} else {
				writeJSON(w, 202, map[string]string{"job_id": j.ID})
			}
			return true
		}
		if len(parts) != 1 {
			writeError(w, 404, "not_found")
			return true
		}
		var in model.AccountUpdate
		switch r.Method {
		case "PATCH":
			if !readBody(w, r, &in) {
				return true
			}
		case "DELETE":
			off := false
			in.Enabled = &off
		default:
			writeError(w, 405, "method_not_allowed")
			return true
		}
		a, err := p.engine.UpdateAccount(r.Context(), id, in)
		if err != nil {
			businessError(w, err)
		} else if r.Method == "DELETE" {
			w.WriteHeader(204)
		} else {
			writeJSON(w, 200, a)
		}
	case path == "/instances/refresh" && r.Method == "POST":
		var in struct {
			AccountID string `json:"account_id"`
		}
		if !readBody(w, r, &in) {
			return true
		}
		j, err := p.engine.Enqueue(r.Context(), "refresh", in.AccountID)
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 202, map[string]string{"job_id": j.ID})
		}
	case strings.HasPrefix(path, "/jobs/") && r.Method == "GET":
		j, err := p.s.Job(r.Context(), strings.TrimPrefix(path, "/jobs/"))
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, j)
		}
	case path == "/instances" && r.Method == "GET":
		p.instances(w, r)
	case path == "/regions" && r.Method == "GET":
		p.regions(w, r)
	case (path == "/traffic/billing-subjects" || path == "/bandwidth-packages") && r.Method == "GET":
		kind := "subject"
		if path == "/bandwidth-packages" {
			kind = "package"
		}
		items, err := p.s.Snapshots(r.Context(), []string{kind}, r.URL.Query().Get("account_id"))
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"items": items})
		}
	default:
		return false
	}
	return true
}
func (p *productionPanel) instances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	kinds := []string{"ecs", "swas"}
	if kind != "" {
		if kind != "ecs" && kind != "swas" {
			fieldError(w, "kind", "invalid_format")
			return
		}
		kinds = []string{kind}
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			fieldError(w, "limit", "out_of_range")
			return
		}
		limit = n
	}
	cursor := ""
	if raw := q.Get("cursor"); raw != "" {
		v, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(v) > 512 || len(v) == 0 {
			fieldError(w, "cursor", "invalid_format")
			return
		}
		cursor = string(v)
	}
	items, err := p.s.Snapshots(r.Context(), kinds, q.Get("account_id"))
	if err != nil {
		businessError(w, err)
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["id"].(string) < items[j]["id"].(string) })
	filtered := []map[string]any{}
	var updated any
	latest := time.Time{}
	for _, i := range items {
		if value, ok := i["updated_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, value); err == nil && t.After(latest) {
				latest = t
				updated = value
			}
		}
		if i["id"].(string) > cursor {
			filtered = append(filtered, i)
		}
	}
	var next any
	if len(filtered) > limit {
		next = base64.RawURLEncoding.EncodeToString([]byte(filtered[limit-1]["id"].(string)))
		filtered = filtered[:limit]
	}
	if err = p.overlayAgents(r.Context(), filtered); err != nil {
		businessError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": filtered, "next_cursor": next, "updated_at": updated})
}
func (p *productionPanel) regions(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	if site == "" {
		site = "china"
	}
	if site != "china" && site != "international" {
		fieldError(w, "site", "invalid_format")
		return
	}
	accounts, err := p.s.Accounts(r.Context())
	if err != nil {
		businessError(w, err)
		return
	}
	byID := map[string]map[string]any{}
	for _, a := range accounts {
		if a.Site != site {
			continue
		}
		items, err := p.s.Snapshots(r.Context(), []string{"regions"}, a.ID)
		if err != nil {
			businessError(w, err)
			return
		}
		for _, v := range items {
			byID[v["id"].(string)] = v
		}
	}
	items := []map[string]any{}
	for _, v := range byID {
		items = append(items, v)
	}
	if len(items) == 0 {
		items = aliyun.InitialRegions()
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["id"].(string) < items[j]["id"].(string) })
	writeJSON(w, 200, map[string]any{"items": items})
}
