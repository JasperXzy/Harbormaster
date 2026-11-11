package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/ui"
)

func TestHealthAndRouting(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := Handler(s)
	for _, c := range []struct {
		path   string
		status int
		kind   string
	}{{"/healthz", 200, "application/json"}, {"/readyz", 200, "application/json"}, {"/instances/unknown", 404, "application/json"}, {"/api/v1/missing", 404, "application/json"}, {"/agent/v1/missing", 404, "application/json"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != c.status || !strings.HasPrefix(w.Header().Get("Content-Type"), c.kind) {
			t.Errorf("%s: %d %s", c.path, w.Code, w.Header())
		}
	}
	embedded := NewHandler(s, false, "Asia/Shanghai", ui.Handler())
	page := httptest.NewRecorder()
	embedded.ServeHTTP(page, httptest.NewRequest("GET", "/instances/unknown", nil))
	if page.Code != 200 || !strings.Contains(page.Header().Get("Content-Type"), "text/html") {
		t.Fatal("optional embedded SPA unavailable")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/agent/v1/reports", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	s.Close()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("closed db ready")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("liveness depends on db")
	}
}
