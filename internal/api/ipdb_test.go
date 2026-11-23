package api

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
)

type ipdbTransport func(*http.Request) (*http.Response, error)

func (f ipdbTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestIPDatabaseStatusUsesLocalEvidenceAndSession(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	if w := panelRequest(p, "GET", "/network/ipdb", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "GET", "/network/ipdb", nil, cookie, ""); w.Code != 503 {
		t.Fatal("missing component claimed ready", w.Code)
	}
	var calls atomic.Int32
	m, err := ipinfo.New(ipinfo.Options{Directory: t.TempDir(), HTTP: &http.Client{Transport: ipdbTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, ipinfo.ErrUpdate })}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p.ipdb = m
	w := panelRequest(p, "GET", "/network/ipdb", nil, cookie, "")
	var s ipinfo.Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.ASN.Edition != "2026-11" || s.Country.SizeBytes == 0 || s.Mode != "country" || s.City != nil || s.GoHeapAllocBytes == 0 || len(s.Attribution) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	if calls.Load() != 0 {
		t.Fatal("GET triggered network work")
	}
	if w = panelRequest(p, "POST", "/network/ipdb", nil, cookie, csrf); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
