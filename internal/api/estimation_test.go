package api

import (
	"encoding/json"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
)

func TestEstimationSettingsSessionCSRFRevisionAndValidation(t *testing.T) {
	p := testPanel(t)
	if w := panelRequest(p, "GET", "/traffic/estimation-settings", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	in := map[string]any{"revision": 0, "lag_window_seconds": 60, "bandwidth_mode_instance_ids": []string{}}
	if w := panelRequest(p, "PUT", "/traffic/estimation-settings", in, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, bad := range []map[string]any{{"lag_window_seconds": 60, "bandwidth_mode_instance_ids": []string{}}, {"revision": 0, "lag_window_seconds": 59, "bandwidth_mode_instance_ids": []string{}}, {"revision": 0, "lag_window_seconds": 60}, {"revision": 0, "lag_window_seconds": 60, "bandwidth_mode_instance_ids": []string{}, "verified": true}} {
		if w := panelRequest(p, "PUT", "/traffic/estimation-settings", bad, cookie, csrf); w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := panelRequest(p, "PUT", "/traffic/estimation-settings", in, cookie, csrf)
	var v model.EstimationSettings
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Revision != 1 || v.LagSeconds != 60 {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "PUT", "/traffic/estimation-settings", in, cookie, csrf); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
}
