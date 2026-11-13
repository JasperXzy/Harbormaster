package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/JasperXzy/harbormaster/internal/branding"
	"github.com/JasperXzy/harbormaster/internal/model"
)

var (
	ErrBrandingChanged = errors.New("branding_changed")
	ErrBrandingName    = errors.New("invalid_branding_name")
)

type brandingReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readBranding(ctx context.Context, q brandingReader) (model.Branding, error) {
	var state model.Branding
	var width, height, size sql.NullInt64
	var at sql.NullString
	err := q.QueryRowContext(ctx, "SELECT name,revision,logo_width,logo_height,length(logo_png),updated_at FROM panel_branding WHERE id=1").Scan(&state.Name, &state.Revision, &width, &height, &size, &at)
	if err != nil {
		return state, err
	}
	if size.Valid {
		state.Logo = &model.BrandingLogo{URL: "/api/v1/settings/branding/logo?revision=" + strconv.FormatInt(state.Revision, 10), MediaType: "image/png", Width: int(width.Int64), Height: int(height.Int64), Bytes: size.Int64}
	}
	if at.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, at.String)
		if err != nil {
			return state, err
		}
		state.UpdatedAt = &parsed
	}
	return state, nil
}

func (s *Store) Branding(ctx context.Context) (model.Branding, error) {
	return readBranding(ctx, s.DB)
}

func (s *Store) BrandingLogo(ctx context.Context) ([]byte, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, "SELECT logo_png FROM panel_branding WHERE id=1 AND logo_png IS NOT NULL").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return raw, err
}

func (s *Store) changeBranding(ctx context.Context, revision int64, now time.Time, apply func(*sql.Tx, model.Branding) ([]string, error)) (model.Branding, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Branding{}, err
	}
	defer tx.Rollback()
	current, err := readBranding(ctx, tx)
	if err != nil {
		return current, err
	}
	if current.Revision != revision {
		return current, ErrBrandingChanged
	}
	fields, err := apply(tx, current)
	if err != nil {
		return current, err
	}
	if len(fields) == 0 {
		return current, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE panel_branding SET revision=revision+1,updated_at=? WHERE id=1", timestamp(now)); err != nil {
		return current, err
	}
	revision++
	if err = appendAudit(ctx, tx, auditEvent{Action: "branding_updated", TargetType: "branding", TargetID: "panel", Details: model.AuditDetails{ChangedFields: fields, Revision: &revision}}, now); err != nil {
		return current, err
	}
	current, err = readBranding(ctx, tx)
	if err != nil {
		return current, err
	}
	return current, tx.Commit()
}

func (s *Store) UpdateBrandingName(ctx context.Context, revision int64, name string, now time.Time) (model.Branding, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 100 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return model.Branding{}, ErrBrandingName
	}
	return s.changeBranding(ctx, revision, now, func(tx *sql.Tx, current model.Branding) ([]string, error) {
		if current.Name == name {
			return nil, nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE panel_branding SET name=? WHERE id=1", name)
		return []string{"name"}, err
	})
}

func (s *Store) ReplaceBrandingLogo(ctx context.Context, revision int64, mediaType string, raw []byte, now time.Time) (model.Branding, error) {
	logo, err := branding.Normalize(mediaType, raw)
	if err != nil {
		return model.Branding{}, err
	}
	return s.changeBranding(ctx, revision, now, func(tx *sql.Tx, current model.Branding) ([]string, error) {
		if current.Logo != nil {
			var previous []byte
			if err := tx.QueryRowContext(ctx, "SELECT logo_png FROM panel_branding WHERE id=1").Scan(&previous); err != nil {
				return nil, err
			}
			if bytes.Equal(previous, logo.PNG) {
				return nil, nil
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE panel_branding SET logo_png=?,logo_width=?,logo_height=? WHERE id=1", logo.PNG, logo.Width, logo.Height)
		return []string{"logo"}, err
	})
}

func (s *Store) ClearBrandingLogo(ctx context.Context, revision int64, now time.Time) (model.Branding, error) {
	return s.changeBranding(ctx, revision, now, func(tx *sql.Tx, current model.Branding) ([]string, error) {
		if current.Logo == nil {
			return nil, nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE panel_branding SET logo_png=NULL,logo_width=NULL,logo_height=NULL WHERE id=1")
		return []string{"logo"}, err
	})
}
