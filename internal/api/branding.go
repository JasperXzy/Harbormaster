package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/JasperXzy/harbormaster/internal/branding"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func brandingResponse(w http.ResponseWriter, state model.Branding, err error) {
	switch {
	case err == nil:
		writeJSON(w, 200, state)
	case errors.Is(err, store.ErrBrandingChanged):
		writeError(w, 409, "branding_changed")
	case errors.Is(err, store.ErrBrandingName):
		fieldError(w, "name", "invalid_format")
	case errors.Is(err, branding.ErrLogoType):
		writeError(w, 415, "unsupported_media_type")
	case errors.Is(err, branding.ErrLogoTooLarge):
		writeError(w, 413, "branding_logo_too_large")
	case errors.Is(err, branding.ErrLogoDimensions):
		fieldError(w, "logo", "invalid_dimensions")
	case errors.Is(err, branding.ErrLogoInvalid):
		fieldError(w, "logo", "invalid_image")
	default:
		businessError(w, err)
	}
}

func (p *productionPanel) branding(w http.ResponseWriter, r *http.Request, path string) bool {
	if path == "/settings/branding" {
		switch r.Method {
		case "GET":
			state, err := p.s.Branding(r.Context())
			brandingResponse(w, state, err)
		case "PUT":
			var in struct {
				Revision *int64  `json:"revision"`
				Name     *string `json:"name"`
			}
			if !readBody(w, r, &in) {
				return true
			}
			if in.Revision == nil || *in.Revision < 0 {
				fieldError(w, "revision", "invalid_format")
				return true
			}
			if in.Name == nil {
				fieldError(w, "name", "required")
				return true
			}
			state, err := p.s.UpdateBrandingName(r.Context(), *in.Revision, *in.Name, p.now())
			brandingResponse(w, state, err)
		default:
			writeError(w, 405, "method_not_allowed")
		}
		return true
	}
	if path != "/settings/branding/logo" {
		return false
	}
	if r.Method == "GET" {
		raw, err := p.s.BrandingLogo(r.Context())
		if err != nil {
			businessError(w, err)
		} else {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
			w.WriteHeader(200)
			_, _ = w.Write(raw)
		}
		return true
	}
	if r.Method != "PUT" && r.Method != "DELETE" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	values := r.URL.Query()["revision"]
	var revision int64
	var err error
	if len(values) == 1 {
		revision, err = strconv.ParseInt(values[0], 10, 64)
	}
	if len(values) != 1 || err != nil || revision < 0 || strconv.FormatInt(revision, 10) != values[0] {
		fieldError(w, "revision", "invalid_format")
		return true
	}
	if r.Method == "DELETE" {
		state, err := p.s.ClearBrandingLogo(r.Context(), revision, p.now())
		brandingResponse(w, state, err)
		return true
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "image/png" && media != "image/jpeg" {
		writeError(w, 415, "unsupported_media_type")
		return true
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, branding.MaxLogoBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "branding_logo_too_large")
		} else {
			fieldError(w, "logo", "invalid_image")
		}
		return true
	}
	state, err := p.s.ReplaceBrandingLogo(r.Context(), revision, media, raw, p.now())
	brandingResponse(w, state, err)
	return true
}
