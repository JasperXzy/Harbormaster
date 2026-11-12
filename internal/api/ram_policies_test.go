package api

import (
	"encoding/json"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/rampolicy"
)

func TestRAMPolicyReadsAreAuthenticatedDraftsOnly(t *testing.T) {
	p := testPanel(t)
	if w := panelRequest(p, "GET", "/ram-policies", nil, nil, ""); w.Code != 401 {
		t.Fatal("anonymous policy API", w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	w := panelRequest(p, "GET", "/ram-policies", nil, cookie, "")
	var page struct {
		Items []rampolicy.Draft `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 3 {
		t.Fatal(w.Code, w.Body)
	}
	for index, draft := range page.Items {
		if draft.Verification != "draft" || draft.CloudWritePermissions != (index > 0) || len(draft.Limitations) < 3 {
			t.Fatal("draft limitations lost", draft)
		}
	}
	if w := panelRequest(p, "POST", "/ram-policies", map[string]string{"id": "resource_management"}, cookie, csrf); w.Code != 405 {
		t.Fatal("API exposes policy mutation", w.Code)
	}
	var jobs, credentials int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("template read enqueued work", jobs, err)
	}
	if err := p.s.DB.QueryRow("SELECT count(*) FROM credential").Scan(&credentials); err != nil || credentials != 0 {
		t.Fatal("template read stored credentials", credentials, err)
	}
}
