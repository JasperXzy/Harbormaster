package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestPasswordPolicyRequiresCurrentRPPasskeyAndCSRF(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	w := panelRequest(p, "PATCH", "/session/password", map[string]bool{"enabled": false}, cookie, csrf)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "passkey_required") {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "PATCH", "/session/password", map[string]bool{"enabled": false}, cookie, "")
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	for _, in := range []any{map[string]any{}, map[string]any{"enabled": nil}, map[string]any{"enabled": "false"}} {
		w = panelRequest(p, "PATCH", "/session/password", in, cookie, csrf)
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	p, cookie, csrf, _, _ = registeredPasskey(t)
	p.publicURL = "https://different.example"
	w = panelRequest(p, "PATCH", "/session/password", map[string]bool{"enabled": false}, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("different RP key counted", w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/session/methods", nil, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"passkey_enabled":false`) {
		t.Fatal(w.Code, w.Body)
	}
}
func TestPasswordDisablePasskeyLoginAndCLIResetRecovery(t *testing.T) {
	p, cookie, csrf, device, id := registeredPasskey(t)
	w := panelRequest(p, "PATCH", "/session/password", map[string]bool{"enabled": false}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/session/methods", nil, nil, "")
	var methods map[string]bool
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &methods) != nil || methods["password_enabled"] || !methods["passkey_enabled"] {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "POST", "/session", map[string]string{"password": testPassword}, nil, "")
	if w.Code != 401 {
		t.Fatal("disabled password authenticated", w.Code, w.Body)
	}
	w = panelRequest(p, "DELETE", "/passkeys/"+id, nil, cookie, csrf)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "last_passkey") {
		t.Fatal("deleted last key", w.Code, w.Body)
	}
	o, binding := loginPasskeyOptions(t, p)
	w = finishPasskeyRequest(p, o, device.assertion(t, o, "https://panel.example", "panel.example", 5, 1), binding)
	if w.Code != 200 {
		t.Fatal("passwordless unavailable", w.Code, w.Body)
	}
	clearPasskeyRateLimit(t, p)
	_, _ = loginPasskeyOptions(t, p)
	state, err := p.s.Passkeys(context.Background())
	if err != nil || state.PasswordEnabled {
		t.Fatal(state, err)
	}
	hash, err := security.HashPassword([]byte("recovered-password-test-only"))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.s.ResetPassword(context.Background(), hash); err != nil {
		t.Fatal(err)
	}
	after, err := p.s.Passkeys(context.Background())
	if err != nil || !after.PasswordEnabled || len(after.Keys) != 1 || after.Keys[0].ID != id || after.Epoch <= state.Epoch {
		t.Fatal("recovery changed keys or kept policy", after, err)
	}
	for _, table := range []string{"session", "passkey_ceremony"} {
		var n int
		if err = p.s.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	w = panelRequest(p, "GET", "/session", nil, cookie, "")
	if w.Code != 401 {
		t.Fatal("reset kept old session", w.Code)
	}
	w = panelRequest(p, "POST", "/session", map[string]string{"password": "recovered-password-test-only"}, nil, "")
	if w.Code != 200 {
		t.Fatal("reset password could not log in", w.Code, w.Body)
	}
	clearPasskeyRateLimit(t, p)
	o, binding = loginPasskeyOptions(t, p)
	w = finishPasskeyRequest(p, o, device.assertion(t, o, "https://panel.example", "panel.example", 5, 2), binding)
	if w.Code != 200 {
		t.Fatal("retained passkey unusable", w.Code, w.Body)
	}
}
func TestPasswordReenableUsesExistingPasswordAndSessionRemainsValid(t *testing.T) {
	p, cookie, csrf, _, id := registeredPasskey(t)
	for _, enabled := range []bool{false, true} {
		w := panelRequest(p, "PATCH", "/session/password", map[string]bool{"enabled": enabled}, cookie, csrf)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		if panelRequest(p, "GET", "/session", nil, cookie, "").Code != 200 {
			t.Fatal("policy invalidated current session")
		}
	}
	if w := panelRequest(p, "DELETE", "/passkeys/"+id, nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "POST", "/session", map[string]string{"password": testPassword}, nil, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}
