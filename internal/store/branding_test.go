package store

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func brandingImage(t *testing.T, width int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, width, 3))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBrandingPersistsNamesImagesAndOnlyEffectiveAuditChanges(t *testing.T) {
	ctx := model.WithJobTrigger(context.Background(), "panel")
	dir := t.TempDir()
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 11, 13, 3, 4, 5, 0, time.UTC)
	state, err := s.Branding(ctx)
	if err != nil || state.Name != model.DefaultBrandingName || state.Logo != nil || state.UpdatedAt != nil || state.Revision != 0 {
		t.Fatal(state, err)
	}
	if _, err = s.BrandingLogo(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing image not 404", err)
	}
	state, err = s.UpdateBrandingName(ctx, 0, "  港口  ", now)
	if err != nil || state.Name != "港口" || state.Revision != 1 || state.UpdatedAt == nil || !state.UpdatedAt.Equal(now) {
		t.Fatal(state, err)
	}
	source := append(brandingImage(t, 7), []byte("discarded-branding-trailer")...)
	state, err = s.ReplaceBrandingLogo(ctx, 1, "image/png", source, now.Add(time.Minute))
	if err != nil || state.Revision != 2 || state.Logo == nil || state.Logo.Width != 7 || state.Logo.Height != 3 || state.Logo.MediaType != "image/png" || !strings.HasSuffix(state.Logo.URL, "revision=2") {
		t.Fatal(state, err)
	}
	imageBefore, err := s.BrandingLogo(ctx)
	if err != nil || bytes.Contains(imageBefore, []byte("discarded-branding-trailer")) || state.Logo.Bytes != int64(len(imageBefore)) {
		t.Fatal("stored original content", err)
	}
	state, err = s.UpdateBrandingName(ctx, 2, "Port fixture", now.Add(2*time.Minute))
	if err != nil || state.Revision != 3 || state.Logo == nil {
		t.Fatal(state, err)
	}
	unchangedAt := *state.UpdatedAt
	state, err = s.UpdateBrandingName(ctx, 3, " Port fixture ", now.Add(3*time.Minute))
	if err != nil || state.Revision != 3 || !state.UpdatedAt.Equal(unchangedAt) {
		t.Fatal("name no-op mutated state", state, err)
	}
	state, err = s.ReplaceBrandingLogo(ctx, 3, "image/png", imageBefore, now.Add(4*time.Minute))
	if err != nil || state.Revision != 3 || !state.UpdatedAt.Equal(unchangedAt) {
		t.Fatal("logo no-op mutated state", state, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.Branding(ctx)
	retained, imageErr := s.BrandingLogo(ctx)
	if err != nil || imageErr != nil || state.Name != "Port fixture" || state.Revision != 3 || !bytes.Equal(retained, imageBefore) {
		t.Fatal("configuration lost on reopen", state, err, imageErr)
	}
	for _, name := range []string{"", " \n ", "bad\nname", strings.Repeat("界", 101), string([]byte{0xff})} {
		if _, err = s.UpdateBrandingName(ctx, 3, name, now); !errors.Is(err, ErrBrandingName) {
			t.Fatal("invalid name accepted", err)
		}
	}
	state, err = s.ClearBrandingLogo(ctx, 3, now.Add(5*time.Minute))
	if err != nil || state.Logo != nil || state.Name != "Port fixture" || state.Revision != 4 {
		t.Fatal(state, err)
	}
	state, err = s.ClearBrandingLogo(ctx, 4, now.Add(6*time.Minute))
	if err != nil || state.Revision != 4 {
		t.Fatal("clear no-op mutated state", state, err)
	}
	if _, err = s.BrandingLogo(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted bytes still readable", err)
	}
	entries, err := s.AuditLog(ctx, model.AuditFilter{Action: "branding_updated", Limit: 20})
	if err != nil || len(entries) != 4 {
		t.Fatal("wrong audit count", len(entries), err)
	}
	for _, entry := range entries {
		if entry.Actor.Kind != "admin" || entry.TargetType != "branding" || entry.TargetID != "panel" || entry.Details.CredentialsChanged || len(entry.Details.ChangedFields) != 1 {
			t.Fatal("unexpected audit metadata", entry)
		}
	}
}

func TestBrandingRejectsStaleCrossFieldEdits(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	image := brandingImage(t, 5)
	now := time.Now().UTC()
	var results [2]error
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		_, results[0] = s.UpdateBrandingName(ctx, 0, "concurrent fixture", now)
	}()
	go func() {
		defer workers.Done()
		<-start
		_, results[1] = s.ReplaceBrandingLogo(ctx, 0, "image/png", image, now)
	}()
	close(start)
	workers.Wait()
	if !(results[0] == nil && errors.Is(results[1], ErrBrandingChanged) || results[1] == nil && errors.Is(results[0], ErrBrandingChanged)) {
		t.Fatal("concurrent stale edit was not fenced", results)
	}
	state, err := s.Branding(ctx)
	if err != nil || state.Revision != 1 || (state.Name == "concurrent fixture") == (state.Logo != nil) {
		t.Fatal("partial or overwritten state", state, err)
	}
	if results[0] == nil {
		state, err = s.ReplaceBrandingLogo(ctx, 1, "image/png", image, now)
	} else {
		state, err = s.UpdateBrandingName(ctx, 1, "concurrent fixture", now)
	}
	if err != nil || state.Revision != 2 || state.Name != "concurrent fixture" || state.Logo == nil {
		t.Fatal("refresh/retry lost the other field", state, err)
	}
}

func TestBrandingAuditFailureRollsBackNameImageAndRevision(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	state, err := s.ReplaceBrandingLogo(ctx, 0, "image/png", brandingImage(t, 4), now)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.BrandingLogo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER branding_audit_failure BEFORE INSERT ON audit_log WHEN new.action='branding_updated' BEGIN SELECT RAISE(ABORT,'simulated'); END"); err != nil {
		t.Fatal(err)
	}
	operations := []func() error{
		func() error { _, err := s.UpdateBrandingName(ctx, state.Revision, "must roll back", now); return err },
		func() error {
			_, err := s.ReplaceBrandingLogo(ctx, state.Revision, "image/png", brandingImage(t, 8), now)
			return err
		},
		func() error { _, err := s.ClearBrandingLogo(ctx, state.Revision, now); return err },
	}
	for _, operation := range operations {
		if err := operation(); err == nil {
			t.Fatal("audit failure ignored")
		}
		after, err := s.Branding(ctx)
		raw, imageErr := s.BrandingLogo(ctx)
		if err != nil || imageErr != nil || after.Name != state.Name || after.Revision != state.Revision || !bytes.Equal(raw, before) {
			t.Fatal("partial branding write escaped rollback", after, err, imageErr)
		}
	}
}
