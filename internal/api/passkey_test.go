package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// A local P-256 authenticator exercises the actual WebAuthn parser and signature
// checks. It is not a mock of the verifier and never contacts cloud services.
type testPasskeyDevice struct {
	key        *ecdsa.PrivateKey
	id, handle []byte
}
type testPasskeyOptions struct {
	ID      string `json:"ceremony_id"`
	Options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
			UserVerification       string `json:"userVerification"`
			AuthenticatorSelection struct {
				ResidentKey      string `json:"residentKey"`
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	} `json:"options"`
}

func decodePasskeyOptions(t *testing.T, w *httptest.ResponseRecorder) testPasskeyOptions {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("options: %d %s", w.Code, w.Body)
	}
	var o testPasskeyOptions
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	return o
}
func b64Passkey(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func testPasskeyClientData(kind, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}
func testPasskeyAuthData(rpid string, flags byte, count uint32) []byte {
	h := sha256.Sum256([]byte(rpid))
	b := append([]byte{}, h[:]...)
	b = append(b, flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(b[33:], count)
	return b
}
func (d testPasskeyDevice) registration(t *testing.T, o testPasskeyOptions, origin string, flags byte) map[string]any {
	t.Helper()
	pub, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: d.key.X.FillBytes(make([]byte, 32)), -3: d.key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	data := testPasskeyAuthData("panel.example", flags, 0)
	data = append(data, make([]byte, 16)...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(d.id)))
	data = append(data, d.id...)
	data = append(data, pub...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": data, "attStmt": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": b64Passkey(d.id), "rawId": b64Passkey(d.id), "type": "public-key", "response": map[string]any{"clientDataJSON": b64Passkey(testPasskeyClientData("webauthn.create", o.Options.PublicKey.Challenge, origin)), "attestationObject": b64Passkey(att)}, "clientExtensionResults": map[string]any{}}
}
func (d testPasskeyDevice) assertion(t *testing.T, o testPasskeyOptions, origin, rpid string, flags byte, count uint32) map[string]any {
	t.Helper()
	data := testPasskeyAuthData(rpid, flags, count)
	client := testPasskeyClientData("webauthn.get", o.Options.PublicKey.Challenge, origin)
	h := sha256.Sum256(client)
	signed := append(append([]byte{}, data...), h[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": b64Passkey(d.id), "rawId": b64Passkey(d.id), "type": "public-key", "response": map[string]any{"clientDataJSON": b64Passkey(client), "authenticatorData": b64Passkey(data), "signature": b64Passkey(sig), "userHandle": b64Passkey(d.handle)}, "clientExtensionResults": map[string]any{}}
}
func registeredPasskey(t *testing.T) (*productionPanel, *http.Cookie, string, testPasskeyDevice, string) {
	t.Helper()
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	w := panelRequest(p, "POST", "/passkeys/registration/options", map[string]string{"name": "My test device"}, cookie, csrf)
	o := decodePasskeyOptions(t, w)
	if o.Options.PublicKey.AuthenticatorSelection.ResidentKey != "required" || o.Options.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		t.Fatal("passwordless requirements missing")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := base64.RawURLEncoding.DecodeString(o.Options.PublicKey.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	d := testPasskeyDevice{key: key, id: []byte("local-test-credential-not-a-secret"), handle: handle}
	w = panelRequest(p, "POST", "/passkeys/registration/finish", map[string]any{"ceremony_id": o.ID, "credential": d.registration(t, o, "https://panel.example", 0x45)}, cookie, csrf)
	if w.Code != 201 {
		t.Fatalf("registration: %d %s", w.Code, w.Body)
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	return p, cookie, csrf, d, saved.ID
}
func clearPasskeyRateLimit(t *testing.T, p *productionPanel) {
	t.Helper()
	if _, err := p.s.DB.Exec("DELETE FROM auth_attempt"); err != nil {
		t.Fatal(err)
	}
}
func loginPasskeyOptions(t *testing.T, p *productionPanel) (testPasskeyOptions, *http.Cookie) {
	t.Helper()
	w := panelRequest(p, "POST", "/session/passkey/options", struct{}{}, nil, "")
	o := decodePasskeyOptions(t, w)
	if o.Options.PublicKey.UserVerification != "required" {
		t.Fatal("UV not required")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe ceremony cookie")
	}
	return o, cookies[0]
}
func finishPasskeyRequest(p *productionPanel, o testPasskeyOptions, assertion map[string]any, cookie *http.Cookie) *httptest.ResponseRecorder {
	return panelRequest(p, "POST", "/session/passkey/finish", map[string]any{"ceremony_id": o.ID, "credential": assertion}, cookie, "")
}

func TestPasskeyLifecycleWithRealSignatures(t *testing.T) {
	p, cookie, csrf, d, id := registeredPasskey(t)
	w := panelRequest(p, "GET", "/passkeys", nil, cookie, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "publicKey") || !strings.Contains(w.Body.String(), id) {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/session/methods", nil, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"passkey_enabled":true`) {
		t.Fatal(w.Code, w.Body)
	}
	o, binding := loginPasskeyOptions(t, p)
	assertion := d.assertion(t, o, "https://panel.example", "panel.example", 5, 1)
	w = finishPasskeyRequest(p, o, assertion, nil)
	if w.Code != 401 {
		t.Fatal("missing binding", w.Code)
	}
	w = finishPasskeyRequest(p, o, assertion, binding)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var sessionCookieValue *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "hm_session" {
			sessionCookieValue = c
		}
	}
	if sessionCookieValue == nil || panelRequest(p, "GET", "/session", nil, sessionCookieValue, "").Code != 200 {
		t.Fatal("no usable session")
	}
	clearPasskeyRateLimit(t, p)
	if w = finishPasskeyRequest(p, o, assertion, binding); w.Code != 401 {
		t.Fatal("replay accepted", w.Code)
	}
	state, err := p.s.Passkeys(context.Background())
	if err != nil || state.Keys[0].LastUsedAt == nil {
		t.Fatal("last use missing", err)
	}
	o, binding = loginPasskeyOptions(t, p)
	assertion = d.assertion(t, o, "https://panel.example", "panel.example", 5, 2)
	if w = panelRequest(p, "DELETE", "/passkeys/"+id, nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w = finishPasskeyRequest(p, o, assertion, binding); w.Code != 401 {
		t.Fatal("deleted credential accepted", w.Code)
	}
}
func TestPasskeyRejectsInvalidProofs(t *testing.T) {
	p, _, _, d, _ := registeredPasskey(t)
	for _, tc := range []struct {
		name, origin, rpid string
		flags              byte
		corrupt            bool
	}{
		{"origin", "https://evil.example", "panel.example", 5, false},
		{"rp", "https://panel.example", "evil.example", 5, false},
		{"uv", "https://panel.example", "panel.example", 1, false},
		{"presence", "https://panel.example", "panel.example", 4, false},
		{"signature", "https://panel.example", "panel.example", 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearPasskeyRateLimit(t, p)
			o, binding := loginPasskeyOptions(t, p)
			a := d.assertion(t, o, tc.origin, tc.rpid, tc.flags, 1)
			if tc.corrupt {
				a["response"].(map[string]any)["signature"] = b64Passkey([]byte("invalid"))
			}
			w := finishPasskeyRequest(p, o, a, binding)
			if w.Code != 401 {
				t.Fatal(w.Code, w.Body)
			}
			w = finishPasskeyRequest(p, o, d.assertion(t, o, "https://panel.example", "panel.example", 5, 1), binding)
			if w.Code != 401 {
				t.Fatal("failed challenge reused", w.Code)
			}
		})
	}
}
func TestPasskeyCounterAndExpiry(t *testing.T) {
	p, _, _, d, _ := registeredPasskey(t)
	o, binding := loginPasskeyOptions(t, p)
	w := finishPasskeyRequest(p, o, d.assertion(t, o, "https://panel.example", "panel.example", 5, 2), binding)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	clearPasskeyRateLimit(t, p)
	o, binding = loginPasskeyOptions(t, p)
	w = finishPasskeyRequest(p, o, d.assertion(t, o, "https://panel.example", "panel.example", 5, 1), binding)
	if w.Code != 401 {
		t.Fatal("counter rollback accepted", w.Code)
	}
	clearPasskeyRateLimit(t, p)
	o, binding = loginPasskeyOptions(t, p)
	now := p.now()
	p.now = func() time.Time { return now.Add(passkeyTTL + time.Second) }
	w = finishPasskeyRequest(p, o, d.assertion(t, o, "https://panel.example", "panel.example", 5, 3), binding)
	if w.Code != 401 {
		t.Fatal("expired accepted", w.Code)
	}
}
func TestPasskeyRegistrationSessionAndCSRF(t *testing.T) {
	p, cookie, csrf, d, _ := registeredPasskey(t)
	if w := panelRequest(p, "POST", "/passkeys/registration/options", map[string]string{"name": "other"}, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	w := panelRequest(p, "POST", "/passkeys/registration/options", map[string]string{"name": "other"}, cookie, csrf)
	o := decodePasskeyOptions(t, w)
	if err := p.s.ResetPassword(context.Background(), "$argon2id$test-reset"); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "POST", "/passkeys/registration/finish", map[string]any{"ceremony_id": o.ID, "credential": d.registration(t, o, "https://panel.example", 0x45)}, cookie, csrf)
	if w.Code != 401 {
		t.Fatal("revoked session registered key", w.Code)
	}
	var n int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM passkey_ceremony").Scan(&n); err != nil || n != 0 {
		t.Fatal("reset kept pending challenges", err, n)
	}
}

func TestPasskeyRegistrationProofAndDuplicate(t *testing.T) {
	p, cookie, csrf, d, _ := registeredPasskey(t)
	for _, tc := range []struct {
		name, origin string
		flags        byte
		want         int
	}{
		{"origin", "https://evil.example", 0x45, 401},
		{"uv", "https://panel.example", 0x41, 401},
		{"duplicate", "https://panel.example", 0x45, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := panelRequest(p, "POST", "/passkeys/registration/options", map[string]string{"name": "another"}, cookie, csrf)
			o := decodePasskeyOptions(t, w)
			w = panelRequest(p, "POST", "/passkeys/registration/finish", map[string]any{"ceremony_id": o.ID, "credential": d.registration(t, o, tc.origin, tc.flags)}, cookie, csrf)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
}

func TestPasskeyMethodsAndTransport(t *testing.T) {
	p := testPanel(t)
	w := panelRequest(p, "GET", "/session/methods", nil, nil, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "true") {
		t.Fatal("uninitialized methods", w.Code, w.Body)
	}
	cookie, csrf := setupPanel(t, p)
	p.publicURL = ""
	w = panelRequest(p, "POST", "/passkeys/registration/options", map[string]string{"name": "test"}, cookie, csrf)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "passkey_unavailable") {
		t.Fatal(w.Code, w.Body)
	}
	p.publicURL = "https://panel.example"
	for _, origin := range []string{"", "https://evil.example"} {
		r := httptest.NewRequest("POST", "https://panel.example/api/v1/session/passkey/options", strings.NewReader("{}"))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "203.0.113.10:12345"
		w = httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("login bypassed Origin", origin, w.Code)
		}
	}
	clearPasskeyRateLimit(t, p)
	for range 5 {
		w = panelRequest(p, "POST", "/session/passkey/options", struct{}{}, nil, "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w = panelRequest(p, "POST", "/session/passkey/options", struct{}{}, nil, "")
	if w.Code != 429 {
		t.Fatal("missing persistent rate limit", w.Code)
	}
}
