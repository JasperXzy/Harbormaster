package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// The registration's verified attestation binds the credential to an RP ID.
// Count keys for this RP only: a key from a previous public_url domain cannot
// make disabling the current site's password login recoverable.
func (p *productionPanel) eligiblePasskeys(state store.PasskeySnapshot) map[string]string {
	eligible := map[string]string{}
	rp, err := p.passkeyRP()
	if err != nil {
		return eligible
	}
	want := sha256.Sum256([]byte(rp.Config.RPID))
	for _, record := range state.Keys {
		var key webauthn.Credential
		if json.Unmarshal([]byte(record.CredentialJSON), &key) != nil || !bytes.Equal(key.ID, record.CredentialID) {
			continue
		}
		var att protocol.AttestationObject
		if att.UnmarshalCBOR(key.Attestation.Object) != nil || len(att.RawAuthData) < 37 || !bytes.Equal(att.RawAuthData[:32], want[:]) {
			continue
		}
		eligible[record.ID] = record.CredentialJSON
	}
	return eligible
}
func (p *productionPanel) passwordPolicy(w http.ResponseWriter, r *http.Request, path, token string) bool {
	if path != "/session/password" {
		return false
	}
	if r.Method != "PATCH" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !readBody(w, r, &in) {
		return true
	}
	if in.Enabled == nil {
		fieldError(w, "enabled", "required")
		return true
	}
	state, err := p.s.Passkeys(r.Context())
	if err != nil {
		passkeyError(w, err)
		return true
	}
	if err = p.s.SetPasswordLogin(r.Context(), *in.Enabled, passkeyBinding(token), p.eligiblePasskeys(state), p.now()); err != nil {
		passkeyError(w, err)
		return true
	}
	writeJSON(w, 200, map[string]bool{"password_enabled": *in.Enabled})
	return true
}
