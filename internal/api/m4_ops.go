package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) m4Ops(w http.ResponseWriter, r *http.Request, path string) bool {
	if strings.HasPrefix(path, "/instances/") && strings.HasSuffix(path, "/change-eip") {
		parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			return false
		}
		if r.Method != http.MethodPost {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		if !p.eipManageEnabled {
			writeError(w, 503, "eip_manage_disabled")
			return true
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !model.ValidPowerEventKey(keys[0]) {
			fieldError(w, "idempotency_key", "invalid_format")
			return true
		}
		var in model.EIPRotateInput
		if !readBody(w, r, &in) {
			return true
		}
		if in.BandwidthMbps == 0 {
			in.BandwidthMbps = 100
		}
		if in.InternetChargeType == "" {
			in.InternetChargeType = "PayByTraffic"
		}
		if fields := in.Validate(); len(fields) > 0 {
			writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": fields}})
			return true
		}
		j, err := p.s.EnqueueEIPRotateJob(r.Context(), parts[0], in, keys[0], p.now().UTC())
		switch {
		case err == nil:
			writeJSON(w, 202, map[string]string{"job_id": j.ID})
		case errors.Is(err, store.ErrUnmanagedEIPProtected):
			writeError(w, 409, "unmanaged_eip_protected")
		case errors.Is(err, store.ErrEIPRotateUnsupported):
			writeError(w, 409, "eip_rotate_unsupported")
		case errors.Is(err, store.ErrEIPBusy):
			writeError(w, 409, "instance_eip_busy")
		case errors.Is(err, store.ErrPowerEventConflict):
			writeError(w, 409, "idempotency_key_conflict")
		default:
			businessError(w, err)
		}
		return true
	}

	if path == "/cloud/ecs-catalog" {
		if r.Method != http.MethodGet {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		if p.engine == nil || p.engine.Box == nil || p.m4Cloud == nil {
			writeError(w, 503, "internal_error")
			return true
		}
		acctID := strings.TrimSpace(r.URL.Query().Get("account_id"))
		regionID := strings.TrimSpace(r.URL.Query().Get("region_id"))
		if acctID == "" {
			fieldError(w, "account_id", "required")
			return true
		}
		if !model.RegionPattern.MatchString(regionID) {
			fieldError(w, "region_id", "invalid_format")
			return true
		}
		a, err := p.s.Account(r.Context(), acctID)
		if err != nil {
			businessError(w, err)
			return true
		}
		if !a.Enabled {
			writeError(w, 409, "account_disabled")
			return true
		}
		if !slices.Contains(a.RegionIDs, regionID) {
			fieldError(w, "region_id", "region_not_enabled")
			return true
		}
		cipher, err := p.s.AccountCipher(r.Context(), acctID)
		if err != nil {
			writeError(w, 503, "internal_error")
			return true
		}
		raw, err := p.engine.Box.Decrypt(cipher, model.CredentialOwner(acctID))
		if err != nil {
			writeError(w, 503, "internal_error")
			return true
		}
		defer clear(raw)
		var cred model.Credential
		if json.Unmarshal(raw, &cred) != nil {
			writeError(w, 503, "internal_error")
			return true
		}
		defer func() { cred.AccessKeySecret = "" }()
		cat, err := p.m4Cloud.ECSCatalog(r.Context(), aliyun.Credentials{Credential: cred, Site: a.Site, Regions: a.RegionIDs}, acctID, regionID)
		if err != nil {
			writeError(w, 502, aliyun.Code(err))
			return true
		}
		writeJSON(w, 200, cat)
		return true
	}

	if path == "/instances/create" {
		if r.Method != http.MethodPost {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		if !p.instanceCreateEnabled {
			writeError(w, 503, "instance_create_disabled")
			return true
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !model.ValidPowerEventKey(keys[0]) {
			fieldError(w, "idempotency_key", "invalid_format")
			return true
		}
		var in model.ECSCreateInput
		if !readBody(w, r, &in) {
			return true
		}
		if fields := in.Validate(); len(fields) > 0 {
			writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": fields}})
			return true
		}
		j, err := p.s.EnqueueECSCreateJob(r.Context(), in, keys[0], p.now().UTC())
		switch {
		case err == nil:
			writeJSON(w, 202, map[string]string{"job_id": j.ID})
		case err.Error() == "region_not_enabled":
			fieldError(w, "region_id", "region_not_enabled")
		case errors.Is(err, store.ErrPowerEventConflict):
			writeError(w, 409, "idempotency_key_conflict")
		default:
			businessError(w, err)
		}
		return true
	}

	return false
}
