package store

import (
	"context"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type NotificationEvent struct {
	Created      bool
	Notification model.Notification
}

func (s *Store) NotificationChangeHead(ctx context.Context) (int64, error) {
	var seq int64
	err := s.DB.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM notification_change").Scan(&seq)
	return seq, err
}
func (s *Store) NotificationChanges(ctx context.Context, after int64) ([]NotificationEvent, int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT seq,notification_id,seq=(SELECT min(first.seq) FROM notification_change first WHERE first.notification_id=c.notification_id)
 FROM notification_change c WHERE seq>? ORDER BY seq LIMIT 100`, after)
	if err != nil {
		return nil, after, err
	}
	ids := []string{}
	created := map[string]bool{}
	for rows.Next() {
		var id string
		var first bool
		if err = rows.Scan(&after, &id, &first); err != nil {
			rows.Close()
			return nil, after, err
		}
		if _, ok := created[id]; !ok {
			ids = append(ids, id)
		}
		created[id] = created[id] || first
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, after, err
	}
	events := []NotificationEvent{}
	for _, id := range ids {
		n, err := s.Notification(ctx, id)
		if err != nil {
			return nil, after, err
		}
		events = append(events, NotificationEvent{Created: created[id], Notification: n})
	}
	return events, after, nil
}
