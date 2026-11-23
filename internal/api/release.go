package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

var releaseTokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (p *productionPanel) instanceRelease(w http.ResponseWriter, r *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
	if !strings.HasPrefix(path, "/instances/") || len(parts) != 2 || parts[0] == "" || parts[1] != "release" {
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	if !p.releaseEnabled {
		writeError(w, 503, "instance_release_disabled")
		return true
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !model.ValidPowerEventKey(keys[0]) {
		fieldError(w, "idempotency_key", "invalid_format")
		return true
	}
	var in struct {
		Name  string `json:"confirmation_name"`
		Token string `json:"preview_token"`
	}
	if !readBody(w, r, &in) {
		return true
	}
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 128 {
		fieldError(w, "confirmation_name", "invalid_format")
		return true
	}
	if !releaseTokenPattern.MatchString(in.Token) {
		fieldError(w, "preview_token", "invalid_format")
		return true
	}
	j, err := p.s.EnqueueReleaseJob(r.Context(), parts[0], in.Name, in.Token, keys[0], p.now().UTC())
	switch {
	case err == nil:
		writeJSON(w, 202, map[string]string{"job_id": j.ID})
	case errors.Is(err, store.ErrReleaseConfirmation):
		fieldError(w, "confirmation_name", "mismatch")
	case errors.Is(err, store.ErrReleaseParameters):
		fieldError(w, "preview_token", "invalid_format")
	case errors.Is(err, store.ErrReleasePreviewChanged):
		writeError(w, 409, "release_preview_changed")
	case errors.Is(err, store.ErrReleaseBusy):
		writeError(w, 409, "instance_release_busy")
	case errors.Is(err, store.ErrPowerEventConflict):
		writeError(w, 409, "idempotency_key_conflict")
	default:
		businessError(w, err)
	}
	return true
}
