package devmode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestDemoTracesExposeReadOnlySyntheticSummaryDetailAndExport(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SeedDemo(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := New(s, "Asia/Shanghai")
	call := func(method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/network/traces/1/export", "", nil, ""); w.Code != 401 {
		t.Fatal("anonymous demo trace", w.Code)
	}
	w := call("POST", "/session", `{"password":"demo-password-only"}`, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	var session map[string]string
	if json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatal("session decode")
	}
	now := time.Now().UTC()
	path := "/network/traces?" + url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}, "limit": {"1"}}.Encode()
	w = call("GET", path, "", cookie, "")
	var page struct {
		Items []model.TraceRun `json:"items"`
		Next  *string          `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next == nil || page.Items[0].ID != "2" || page.Items[0].Outcome.Reached {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), `"hops"`) {
		t.Fatal("summary contains raw hops")
	}
	if w = call("GET", path+"&point_id=server&cursor="+url.QueryEscape(*page.Next), "", cookie, ""); w.Code != 422 {
		t.Fatal("demo cross-filter cursor", w.Code)
	}
	w = call("GET", path+"&cursor="+url.QueryEscape(*page.Next), "", cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != "1" || !page.Items[0].Outcome.Reached || page.Next != nil {
		t.Fatal(w.Code, w.Body)
	}
	for _, id := range []string{"1", "2"} {
		w = call("GET", "/network/traces/"+id, "", cookie, "")
		var d model.TraceDetail
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.Result == nil {
			t.Fatal(w.Code, w.Body)
		}
		if netprobe.ValidateTraceResult(d.Request, d.Result, nil, time.Now()) != nil {
			t.Fatal("invalid synthetic trace", d)
		}
	}
	w = call("GET", "/network/traces/1/export", "", cookie, "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Body.String(), "2 5 * * - - - probe_timeout") || !strings.Contains(w.Body.String(), "tcp_reset") {
		t.Fatal(w.Code, w.Body)
	}
	w = call("POST", "/network/traces", `{}`, cookie, session["csrf_token"])
	if w.Code != 501 {
		t.Fatal("demo submitted a real trace", w.Code)
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM network_probe_job").Scan(&count); err != nil || count != 0 {
		t.Fatal("demo reads created real tasks", count, err)
	}
}
