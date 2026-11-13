package branding

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestLogoNormalizationStripsTrailingDataAndPreservesDimensions(t *testing.T) {
	input := image.NewNRGBA64(image.Rect(0, 0, 8, 5))
	input.SetNRGBA64(2, 1, color.NRGBA64{R: 0xffff, G: 0x1234, A: 0x8000})
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			var source bytes.Buffer
			var err error
			if format == "png" {
				err = png.Encode(&source, input)
			} else {
				err = jpeg.Encode(&source, input, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			marker := []byte("discarded-logo-source-metadata-fixture")
			raw := append(source.Bytes(), marker...)
			logo, err := Normalize("image/"+format, raw)
			if err != nil || logo.Width != 8 || logo.Height != 5 || bytes.Contains(logo.PNG, marker) {
				t.Fatal("source data retained or image changed", err)
			}
			cfg, kind, err := image.DecodeConfig(bytes.NewReader(logo.PNG))
			if err != nil || kind != "png" || cfg.Width != 8 || cfg.Height != 5 || logo.PNG[24] != 8 {
				t.Fatal("invalid normalized PNG", err)
			}
			if _, err = png.Decode(bytes.NewReader(logo.PNG)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLogoRejectsInvalidContentAndBoundsBeforeSaving(t *testing.T) {
	var small, wide bytes.Buffer
	if err := png.Encode(&small, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&wide, image.NewNRGBA(image.Rect(0, 0, MaxLogoDimension+1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, media string
		raw         []byte
		want        error
	}{
		{"empty", "image/png", nil, ErrLogoInvalid},
		{"SVG", "image/svg+xml", []byte(`<svg onload="invalid"/>`), ErrLogoType},
		{"URL", "image/png", []byte("https://invalid.example/logo.png"), ErrLogoInvalid},
		{"mismatch", "image/jpeg", small.Bytes(), ErrLogoInvalid},
		{"truncated", "image/png", small.Bytes()[:len(small.Bytes())-12], ErrLogoInvalid},
		{"wide", "image/png", wide.Bytes(), ErrLogoDimensions},
		{"too_large", "image/png", make([]byte, MaxLogoBytes+1), ErrLogoTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Normalize(tc.media, tc.raw); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
	var boundary bytes.Buffer
	if err := png.Encode(&boundary, image.NewNRGBA(image.Rect(0, 0, MaxLogoDimension, MaxLogoDimension))); err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize("image/png", boundary.Bytes()); err != nil {
		t.Fatal("valid boundary rejected", err)
	}
}
