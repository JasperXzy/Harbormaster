package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) businessLocation(ctx context.Context) (*time.Location, error) {
	return p.s.BusinessLocation(ctx, p.timezone)
}
func (p *productionPanel) quotaRows(ctx context.Context, id string) ([]model.QuotaStatus, error) {
	accounts, err := p.s.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	if id != "" {
		filtered := []model.Account{}
		for _, a := range accounts {
			if a.ID == id {
				filtered = append(filtered, a)
			}
		}
		if len(filtered) == 0 {
			return nil, store.ErrNotFound
		}
		accounts = filtered
	}
	loc, err := p.businessLocation(ctx)
	if err != nil {
		return nil, err
	}
	return p.s.Quotas(ctx, accounts, loc, p.now())
}
func (p *productionPanel) traffic(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.Method != "GET" {
		return false
	}
	switch {
	case path == "/traffic/quotas":
		rows, err := p.quotaRows(r.Context(), r.URL.Query().Get("account_id"))
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"items": rows})
		}
	case path == "/traffic/observations":
		id := r.URL.Query().Get("account_id")
		if id == "" {
			fieldError(w, "account_id", "required")
			return true
		}
		if _, err := p.s.Account(r.Context(), id); err != nil {
			businessError(w, err)
			return true
		}
		limit := 50
		if value := r.URL.Query().Get("limit"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 200 {
				fieldError(w, "limit", "out_of_range")
				return true
			}
			limit = n
		}
		var before int64
		if value := r.URL.Query().Get("cursor"); value != "" {
			raw, err := base64.RawURLEncoding.DecodeString(value)
			if err != nil || len(raw) > 20 {
				fieldError(w, "cursor", "invalid_format")
				return true
			}
			before, err = strconv.ParseInt(string(raw), 10, 64)
			if err != nil || before < 1 {
				fieldError(w, "cursor", "invalid_format")
				return true
			}
		}
		rows, more, err := p.s.Observations(r.Context(), id, before, limit)
		if err != nil {
			businessError(w, err)
			return true
		}
		var cursor any
		if more {
			cursor = base64.RawURLEncoding.EncodeToString([]byte(rows[len(rows)-1].ID))
		}
		writeJSON(w, 200, map[string]any{"items": rows, "next_cursor": cursor})
	case path == "/traffic/daily":
		id := r.URL.Query().Get("account_id")
		if id == "" {
			fieldError(w, "account_id", "required")
			return true
		}
		loc, err := p.businessLocation(r.Context())
		if err != nil {
			businessError(w, err)
			return true
		}
		a, err := p.s.Account(r.Context(), id)
		if err != nil {
			businessError(w, err)
			return true
		}
		data, err := p.s.DailyTraffic(r.Context(), a, loc, p.now())
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, data)
		}
	case strings.HasPrefix(path, "/bandwidth-packages/") && strings.HasSuffix(path, "/hourly"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/bandwidth-packages/"), "/hourly")
		items, err := p.s.Snapshots(r.Context(), []string{"package"}, "")
		if err != nil {
			businessError(w, err)
			return true
		}
		found := false
		for _, v := range items {
			if v["id"] == id {
				found = true
			}
		}
		if !found {
			writeError(w, 404, "not_found")
			return true
		}
		writeJSON(w, 200, map[string]any{"package_id": id, "period_seconds": 3600, "periods": []any{}, "updated_at": nil})
	default:
		return false
	}
	return true
}
