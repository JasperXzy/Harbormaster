package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func readInstanceMetadataTx(ctx context.Context, q queryRower, instanceID string) (model.InstanceMetadata, error) {
	m := model.InstanceMetadata{InstanceID: instanceID, Tags: []string{}}
	var group sql.NullString
	var rawTags, at string
	err := q.QueryRowContext(ctx, "SELECT note,group_name,tags_json,updated_at FROM instance_metadata WHERE instance_id=?", instanceID).Scan(&m.Note, &group, &rawTags, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if group.Valid {
		g := group.String
		m.Group = &g
	}
	if err = json.Unmarshal([]byte(rawTags), &m.Tags); err != nil {
		return m, err
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return m, err
	}
	m.UpdatedAt = &t
	return m, nil
}

func (s *Store) InstanceMetadataMap(ctx context.Context, ids []string) (map[string]model.InstanceMetadata, error) {
	out := map[string]model.InstanceMetadata{}
	if len(ids) == 0 {
		return out, nil
	}
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		batch := ids[start:end]
		marks := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			marks[i] = "?"
			args[i] = id
		}
		rows, err := s.DB.QueryContext(ctx, "SELECT instance_id,note,group_name,tags_json,updated_at FROM instance_metadata WHERE instance_id IN ("+strings.Join(marks, ",")+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var m model.InstanceMetadata
			var group sql.NullString
			var rawTags, at string
			if err = rows.Scan(&m.InstanceID, &m.Note, &group, &rawTags, &at); err != nil {
				rows.Close()
				return nil, err
			}
			if group.Valid {
				g := group.String
				m.Group = &g
			}
			if err = json.Unmarshal([]byte(rawTags), &m.Tags); err != nil {
				rows.Close()
				return nil, err
			}
			if m.Tags == nil {
				m.Tags = []string{}
			}
			t, err := time.Parse(time.RFC3339Nano, at)
			if err != nil {
				rows.Close()
				return nil, err
			}
			m.UpdatedAt = &t
			out[m.InstanceID] = m
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) UpdateInstanceMetadata(ctx context.Context, instanceID string, in model.InstanceMetadataUpdate, now time.Time) (model.InstanceMetadata, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.InstanceMetadata{}, err
	}
	defer tx.Rollback()
	var accountID string
	err = tx.QueryRowContext(ctx, "SELECT account_id FROM instance WHERE id=?", instanceID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.InstanceMetadata{}, ErrNotFound
	}
	if err != nil {
		return model.InstanceMetadata{}, err
	}
	prev, err := readInstanceMetadataTx(ctx, tx, instanceID)
	if err != nil {
		return model.InstanceMetadata{}, err
	}
	next, fields := model.NormalizeInstanceMetadata(prev, in)
	if len(fields) > 0 {
		return model.InstanceMetadata{}, &SettingsValidationError{Fields: fields}
	}
	details := auditDiff(
		auditChange{"note", prev.Note, next.Note},
		auditChange{"group", prev.Group, next.Group},
		auditChange{"tags", prev.Tags, next.Tags},
	)
	if len(details.ChangedFields) == 0 {
		return prev, tx.Commit()
	}
	rawTags, err := json.Marshal(next.Tags)
	if err != nil {
		return model.InstanceMetadata{}, err
	}
	var groupVal any
	if next.Group != nil {
		groupVal = *next.Group
	}
	at := now.UTC()
	next.UpdatedAt = &at
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_metadata(instance_id,note,group_name,tags_json,updated_at) VALUES(?,?,?,?,?)
 ON CONFLICT(instance_id) DO UPDATE SET note=excluded.note,group_name=excluded.group_name,tags_json=excluded.tags_json,updated_at=excluded.updated_at`,
		instanceID, next.Note, groupVal, string(rawTags), timestamp(at)); err != nil {
		return model.InstanceMetadata{}, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_metadata_updated", TargetType: "instance", TargetID: instanceID, AccountID: accountID, Details: details}, at); err != nil {
		return model.InstanceMetadata{}, err
	}
	return next, tx.Commit()
}
