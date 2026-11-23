package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const passkeyTTL = 5 * time.Minute

type passkeyUser struct {
	handle []byte
	keys   []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u passkeyUser) WebAuthnName() string                       { return "administrator" }
func (u passkeyUser) WebAuthnDisplayName() string                { return "Harbormaster" }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.keys }
func passkeyUserFrom(s store.PasskeySnapshot) (passkeyUser, error) {
	u := passkeyUser{handle: s.UserHandle}
	for _, k := range s.Keys {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(k.CredentialJSON), &c); err != nil {
			return u, err
		}
		if !bytes.Equal(c.ID, k.CredentialID) {
			return u, store.ErrUnauthorized
		}
		u.keys = append(u.keys, c)
	}
	return u, nil
}
func (p *productionPanel) passkeyRP() (*webauthn.WebAuthn, error) {
	u, err := url.Parse(p.publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("passkey_unavailable")
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyTTL, TimeoutUVD: passkeyTTL}
	return webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "Harbormaster", RPOrigins: []string{u.Scheme + "://" + u.Host}, AttestationPreference: protocol.PreferNoAttestation, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(), UserVerification: protocol.VerificationRequired}, Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout}})
}
func randomPasskeyToken() (string, error) {
	var raw [32]byte
	_, err := rand.Read(raw[:])
	return hex.EncodeToString(raw[:]), err
}
func passkeyBinding(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }
func passkeyCookie(token string, expiry time.Time) *http.Cookie {
	return &http.Cookie{Name: "hm_passkey", Value: token, Path: "/api/v1/session/passkey", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expiry, MaxAge: int(passkeyTTL.Seconds())}
}

// Public endpoints still pass the panel transport, host, Origin and IP checks.
func (p *productionPanel) publicPasskeys(w http.ResponseWriter, r *http.Request, path, source string) bool {
	if path == "/session/methods" {
		if r.Method != "GET" {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		initialized, err := p.s.Initialized(r.Context())
		if err != nil {
			passkeyError(w, err)
			return true
		}
		state, err := p.s.Passkeys(r.Context())
		if err != nil {
			passkeyError(w, err)
			return true
		}
		writeJSON(w, 200, map[string]bool{"password_enabled": initialized && state.PasswordEnabled, "passkey_enabled": initialized && len(p.eligiblePasskeys(state)) > 0})
		return true
	}
	if path != "/session/passkey/options" && path != "/session/passkey/finish" {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	wait, err := p.s.TakeAuthAttempt(r.Context(), source, p.now())
	if err != nil {
		passkeyError(w, err)
		return true
	}
	if wait > 0 {
		rateLimited(w, wait)
		return true
	}
	if strings.HasSuffix(path, "/options") {
		p.beginPasskey(w, r, "login", "")
	} else {
		p.finishPasskey(w, r, "login", "")
	}
	return true
}
func (p *productionPanel) passkeys(w http.ResponseWriter, r *http.Request, path, token string) bool {
	if path != "/passkeys" && !strings.HasPrefix(path, "/passkeys/") {
		return false
	}
	if path == "/passkeys" && r.Method == "GET" {
		state, err := p.s.Passkeys(r.Context())
		if err != nil {
			passkeyError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"items": state.Keys})
		}
		return true
	}
	if path == "/passkeys/registration/options" || path == "/passkeys/registration/finish" {
		if r.Method != "POST" {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		if strings.HasSuffix(path, "/options") {
			p.beginPasskey(w, r, "registration", token)
		} else {
			p.finishPasskey(w, r, "registration", token)
		}
		return true
	}
	if r.Method == "DELETE" {
		id := strings.TrimPrefix(path, "/passkeys/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, 404, "not_found")
			return true
		}
		state, err := p.s.Passkeys(r.Context())
		if err != nil {
			passkeyError(w, err)
			return true
		}
		if err := p.s.DeletePasskey(r.Context(), id, passkeyBinding(token), p.eligiblePasskeys(state), p.now()); err != nil {
			passkeyError(w, err)
		} else {
			w.WriteHeader(204)
		}
		return true
	}
	writeError(w, 405, "method_not_allowed")
	return true
}
func (p *productionPanel) beginPasskey(w http.ResponseWriter, r *http.Request, kind, token string) {
	rp, err := p.passkeyRP()
	if err != nil {
		writeError(w, 503, "passkey_unavailable")
		return
	}
	name := ""
	if kind == "registration" {
		var in struct {
			Name string `json:"name"`
		}
		if !readBody(w, r, &in) {
			return
		}
		name = strings.TrimSpace(in.Name)
		if len(name) < 1 || len(name) > 80 || !utf8.ValidString(name) {
			fieldError(w, "name", "out_of_range")
			return
		}
	} else {
		var in struct{}
		if !readBody(w, r, &in) {
			return
		}
	}
	state, err := p.s.Passkeys(r.Context())
	if err != nil {
		passkeyError(w, err)
		return
	}
	user, err := passkeyUserFrom(state)
	if err != nil {
		passkeyError(w, err)
		return
	}
	var options any
	var session *webauthn.SessionData
	if kind == "registration" {
		if len(state.Keys) >= 20 {
			passkeyError(w, store.ErrPasskeyLimit)
			return
		}
		descriptors := webauthn.Credentials(user.keys).CredentialDescriptors()
		options, session, err = rp.BeginRegistration(user, webauthn.WithExclusions(descriptors))
	} else {
		options, session, err = rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	}
	if err != nil {
		passkeyError(w, err)
		return
	}
	id, err := randomPasskeyToken()
	if err != nil {
		passkeyError(w, err)
		return
	}
	if kind == "login" {
		token, err = randomPasskeyToken()
		if err != nil {
			passkeyError(w, err)
			return
		}
	}
	now := p.now()
	expiry := now.Add(passkeyTTL)
	session.Expires = expiry
	session.Origin = rp.Config.RPOrigins[0]
	payload, err := json.Marshal(session)
	if err != nil {
		passkeyError(w, err)
		return
	}
	c := store.PasskeyCeremony{ID: id, Kind: kind, Name: name, BindingHash: passkeyBinding(token), Epoch: state.Epoch, SessionJSON: string(payload), ExpiresAt: expiry}
	if err = p.s.SavePasskeyCeremony(r.Context(), c, now); err != nil {
		passkeyError(w, err)
		return
	}
	if kind == "login" {
		http.SetCookie(w, passkeyCookie(token, expiry))
	}
	writeJSON(w, 200, map[string]any{"ceremony_id": id, "expires_at": expiry.UTC().Format(time.RFC3339Nano), "options": options})
}
func (p *productionPanel) finishPasskey(w http.ResponseWriter, r *http.Request, kind, token string) {
	rp, err := p.passkeyRP()
	if err != nil {
		writeError(w, 503, "passkey_unavailable")
		return
	}
	var in struct {
		ID         string          `json:"ceremony_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if !readBody(w, r, &in) {
		return
	}
	if len(in.ID) != 64 {
		writeError(w, 401, "invalid_passkey")
		return
	}
	if kind == "login" {
		cookie, err := r.Cookie("hm_passkey")
		if err != nil || len(cookie.Value) != 64 {
			writeError(w, 401, "invalid_passkey")
			return
		}
		token = cookie.Value
	}
	c, err := p.s.TakePasskeyCeremony(r.Context(), in.ID, kind, passkeyBinding(token), p.now())
	if err != nil {
		passkeyError(w, err)
		return
	}
	var session webauthn.SessionData
	if err = json.Unmarshal([]byte(c.SessionJSON), &session); err != nil {
		passkeyError(w, err)
		return
	}
	// Bind to the current RP configuration even if public_url changed mid-flow.
	if session.RelyingPartyID != rp.Config.RPID || session.Origin != rp.Config.RPOrigins[0] {
		writeError(w, 401, "invalid_passkey")
		return
	}
	state, err := p.s.Passkeys(r.Context())
	if err != nil {
		passkeyError(w, err)
		return
	}
	user, err := passkeyUserFrom(state)
	if err != nil {
		passkeyError(w, err)
		return
	}
	if kind == "registration" {
		parsed, err := protocol.ParseCredentialCreationResponseBytes(in.Credential)
		if err != nil {
			writeError(w, 401, "invalid_passkey")
			return
		}
		key, err := rp.CreateCredential(user, session, parsed)
		if err != nil {
			writeError(w, 401, "invalid_passkey")
			return
		}
		raw, err := json.Marshal(key)
		if err != nil {
			passkeyError(w, err)
			return
		}
		id, err := randomPasskeyToken()
		if err != nil {
			passkeyError(w, err)
			return
		}
		record := store.Passkey{ID: id, Name: c.Name, CreatedAt: p.now().UTC().Format(time.RFC3339Nano), CredentialID: key.ID, CredentialJSON: string(raw)}
		if err = p.s.RegisterPasskey(r.Context(), c, record, p.now()); err != nil {
			passkeyError(w, err)
			return
		}
		writeJSON(w, 201, record)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(in.Credential)
	if err != nil {
		writeError(w, 401, "invalid_passkey")
		return
	}
	lookup := func(rawID, userHandle []byte) (webauthn.User, error) {
		if !bytes.Equal(userHandle, user.handle) {
			return nil, store.ErrUnauthorized
		}
		for _, k := range user.keys {
			if bytes.Equal(k.ID, rawID) {
				return user, nil
			}
		}
		return nil, store.ErrUnauthorized
	}
	key, err := rp.ValidateDiscoverableLogin(lookup, session, parsed)
	if err != nil || key.Authenticator.CloneWarning {
		writeError(w, 401, "invalid_passkey")
		return
	}
	var previous store.Passkey
	for _, k := range state.Keys {
		if bytes.Equal(k.CredentialID, key.ID) {
			previous = k
			break
		}
	}
	raw, err := json.Marshal(key)
	if err != nil {
		passkeyError(w, err)
		return
	}
	newToken, err := randomPasskeyToken()
	if err != nil {
		passkeyError(w, err)
		return
	}
	now := p.now()
	expiry := now.Add(p.ttl)
	if err = p.s.CompletePasskeyLogin(r.Context(), c, previous, string(raw), passkeyBinding(newToken), expiry, now); err != nil {
		passkeyError(w, err)
		return
	}
	http.SetCookie(w, sessionCookie(newToken, expiry, int(p.ttl.Seconds())))
	cookie := passkeyCookie("", time.Unix(1, 0))
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
	sessionResponse(w, 200, newToken, expiry)
}
func passkeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrPasskeyRequired):
		writeError(w, 409, "passkey_required")
	case errors.Is(err, store.ErrLastPasskey):
		writeError(w, 409, "last_passkey")
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, 401, "invalid_passkey")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, store.ErrPasskeyLimit):
		writeError(w, 409, "passkey_limit")
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "passkey_exists")
	default:
		writeError(w, 503, "internal_error")
	}
}
