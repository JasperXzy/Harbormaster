package store

import (
	"context"
	"strings"
	"time"
)

type InstanceIntent struct {
	DesiredState string
	UpdatedAt    time.Time
}

// InstanceIntents returns recorded intent only. Legacy defaults and current
// cloud status are not evidence of the administrator's desired state.
func (s *Store) InstanceIntents(ctx context.Context, ids []string) (map[string]InstanceIntent, error) {
	out := map[string]InstanceIntent{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 200 {
		return nil, ErrPowerParameters
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for n, id := range ids {
		marks[n] = "?"
		args[n] = id
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT i.id,i.desired_state,d.updated_at FROM instance i JOIN instance_intent d ON d.instance_id=i.id WHERE i.id IN ("+strings.Join(marks, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at string
		var value InstanceIntent
		if err = rows.Scan(&id, &value.DesiredState, &at); err != nil {
			return nil, err
		}
		value.UpdatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		out[id] = value
	}
	return out, rows.Err()
}
