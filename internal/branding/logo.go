// Package branding normalizes bounded uploaded images for local panel use.
package branding

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
)

const MaxLogoBytes = 2 << 20
const MaxLogoDimension = 512

var (
	ErrLogoType       = errors.New("branding_logo_type_invalid")
	ErrLogoTooLarge   = errors.New("branding_logo_too_large")
	ErrLogoDimensions = errors.New("branding_logo_dimensions_invalid")
	ErrLogoInvalid    = errors.New("branding_logo_invalid")
)

type Logo struct {
	PNG           []byte
	Width, Height int
}

// Normalize checks dimensions before pixel decoding and writes a fresh 8-bit
// static PNG. Source metadata and bytes after the image are not persisted.
func Normalize(mediaType string, raw []byte) (Logo, error) {
	want := "png"
	if mediaType == "image/jpeg" {
		want = "jpeg"
	} else if mediaType != "image/png" {
		return Logo{}, ErrLogoType
	}
	if len(raw) > MaxLogoBytes {
		return Logo{}, ErrLogoTooLarge
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != want {
		return Logo{}, ErrLogoInvalid
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoDimension || cfg.Height > MaxLogoDimension {
		return Logo{}, ErrLogoDimensions
	}
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Logo{}, ErrLogoInvalid
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	draw.Draw(canvas, canvas.Bounds(), source, source.Bounds().Min, draw.Src)
	var out bytes.Buffer
	if png.Encode(&out, canvas) != nil {
		return Logo{}, ErrLogoInvalid
	}
	if out.Len() > MaxLogoBytes {
		return Logo{}, ErrLogoTooLarge
	}
	return Logo{PNG: out.Bytes(), Width: cfg.Width, Height: cfg.Height}, nil
}
