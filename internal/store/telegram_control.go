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

func readTelegramControlTx(ctx context.Context, q queryRower) (model.TelegramControlSettings, error) {
	out := model.DefaultTelegramControlSettings()
	var enabled int
	var raw string
	var at sql.NullString
	err := q.QueryRowContext(ctx, "SELECT revision,enabled,allowed_user_ids_json,updated_at FROM telegram_control WHERE id=1").Scan(&out.Revision, &enabled, &raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = enabled == 1
	if err = json.Unmarshal([]byte(raw), &out.AllowedUserIDs); err != nil {
		return out, err
	}
	if out.AllowedUserIDs == nil {
		out.AllowedUserIDs = []string{}
	}
	out.UpdatedAt = parseAgentTime(at)
	return out, nil
}

func (s *Store) TelegramControl(ctx context.Context) (model.TelegramControlSettings, error) {
	return readTelegramControlTx(ctx, s.DB)
}

func (s *Store) SaveTelegramControl(ctx context.Context, in model.TelegramControlSettings, now time.Time) (model.TelegramControlSettings, error) {
	if fields := in.NormalizeAndValidate(); len(fields) > 0 {
		return model.TelegramControlSettings{}, &SettingsValidationError{Fields: fields}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.TelegramControlSettings{}, err
	}
	defer tx.Rollback()
	cur, err := readTelegramControlTx(ctx, tx)
	if err != nil {
		return cur, err
	}
	if cur.Revision != in.Revision {
		return cur, ErrChannelChanged
	}
	at := now.UTC()
	in.Revision = cur.Revision + 1
	in.UpdatedAt = &at
	raw, err := json.Marshal(in.AllowedUserIDs)
	if err != nil {
		return cur, err
	}
	enabled := 0
	if in.Enabled {
		enabled = 1
	}
	if _, err = tx.ExecContext(ctx, "UPDATE telegram_control SET revision=?,enabled=?,allowed_user_ids_json=?,updated_at=? WHERE id=1", in.Revision, enabled, string(raw), timestamp(at)); err != nil {
		return cur, err
	}
	if !in.Enabled {
		_, _ = tx.ExecContext(ctx, "DELETE FROM telegram_action_token")
	}
	details := auditDiff(
		auditChange{"enabled", cur.Enabled, in.Enabled},
		auditChange{"allowed_user_ids", cur.AllowedUserIDs, in.AllowedUserIDs},
	)
	details.Revision = &in.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: "telegram_control_updated", TargetType: "telegram_channel", TargetID: "control", Details: details}, at); err != nil {
		return cur, err
	}
	return in, tx.Commit()
}

func (s *Store) TelegramBotOffset(ctx context.Context, channelRevision int64) (int64, error) {
	var rev, offset int64
	err := s.DB.QueryRowContext(ctx, "SELECT channel_revision,update_offset FROM telegram_bot_offset WHERE id=1").Scan(&rev, &offset)
	if errors.Is(err, sql.ErrNoRows) || rev != channelRevision {
		return 0, nil
	}
	return offset, err
}

func (s *Store) SaveTelegramBotOffset(ctx context.Context, channelRevision, offset int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO telegram_bot_offset(id,channel_revision,update_offset) VALUES(1,?,?)
 ON CONFLICT(id) DO UPDATE SET channel_revision=excluded.channel_revision,update_offset=excluded.update_offset`, channelRevision, offset)
	return err
}

type TelegramActionToken struct {
	Token      string
	UserID     string
	ChatID     string
	InstanceID string
	Action     string
	StopMode   string
	ExpiresAt  time.Time
}

func (s *Store) CreateTelegramActionToken(ctx context.Context, userID, chatID, instanceID, action, stopMode string, now time.Time) (string, error) {
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM telegram_action_token WHERE expires_at<=?", timestamp(now))
	tok := model.NewID("tga_")
	exp := now.UTC().Add(5 * time.Minute)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO telegram_action_token(token,user_id,chat_id,instance_id,action,stop_mode,expires_at) VALUES(?,?,?,?,?,?,?)",
		tok, userID, chatID, instanceID, action, stopMode, timestamp(exp))
	return tok, err
}

func (s *Store) ConsumeTelegramActionToken(ctx context.Context, tok, userID string, cancelOnly bool, now time.Time) (TelegramActionToken, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TelegramActionToken{}, err
	}
	defer tx.Rollback()
	var item TelegramActionToken
	var expStr string
	err = tx.QueryRowContext(ctx, "SELECT token,user_id,chat_id,instance_id,action,stop_mode,expires_at FROM telegram_action_token WHERE token=?", tok).Scan(
		&item.Token, &item.UserID, &item.ChatID, &item.InstanceID, &item.Action, &item.StopMode, &expStr)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	item.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expStr)
	if item.UserID != userID {
		return item, ErrUnauthorized
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM telegram_action_token WHERE token=?", tok); err != nil {
		return item, err
	}
	if err = tx.Commit(); err != nil {
		return item, err
	}
	if !cancelOnly && !now.Before(item.ExpiresAt) {
		return item, ErrNotFound
	}
	return item, nil
}

type ResolvedInstance struct {
	ID                   string
	Name                 string
	Kind                 string
	Status               string
	SupportsStopCharging bool
}

func (s *Store) ResolveInstanceByQuery(ctx context.Context, query string) ([]ResolvedInstance, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT i.id,i.name,i.kind,i.status,r.payload_json
 FROM instance i JOIN resource_snapshot r ON r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1
 JOIN account_settings a ON a.account_id=i.account_id AND a.enabled=1
 ORDER BY i.name,i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	exact := []ResolvedInstance{}
	partial := []ResolvedInstance{}
	lower := strings.ToLower(q)
	for rows.Next() {
		var it ResolvedInstance
		var raw string
		if err = rows.Scan(&it.ID, &it.Name, &it.Kind, &it.Status, &raw); err != nil {
			return nil, err
		}
		var payload struct {
			CloudID string `json:"cloud_instance_id"`
			Charge  string `json:"charge_type"`
		}
		_ = json.Unmarshal([]byte(raw), &payload)
		it.SupportsStopCharging = it.Kind == "ecs" && (payload.Charge == "pay_as_you_go" || payload.Charge == "spot")
		if strings.EqualFold(it.ID, q) || strings.EqualFold(it.Name, q) || strings.EqualFold(payload.CloudID, q) {
			exact = append(exact, it)
		} else if strings.Contains(strings.ToLower(it.Name), lower) {
			partial = append(partial, it)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(exact) > 0 {
		return exact, nil
	}
	return partial, nil
}

func (s *Store) WidgetSummary(ctx context.Context, powerEnabled bool, defaultTimezone string, now time.Time) (model.WidgetSummary, error) {
	out := model.WidgetSummary{UpdatedAt: now.UTC(), PowerEnabled: powerEnabled, Quotas: []model.WidgetQuotaSummary{}}
	accounts, err := s.Accounts(ctx)
	if err != nil {
		return out, err
	}
	out.Accounts.Total = len(accounts)
	enabled := []model.Account{}
	accNames := map[string]string{}
	for _, a := range accounts {
		accNames[a.ID] = a.Name
		if a.Enabled {
			out.Accounts.Enabled++
			enabled = append(enabled, a)
			if a.SyncStatus == "failing" {
				out.Accounts.Failing++
			}
		}
	}
	loc, err := s.BusinessLocation(ctx, defaultTimezone)
	if err != nil {
		loc = time.UTC
	}
	snaps, err := s.Snapshots(ctx, []string{"ecs", "swas"}, "")
	if err != nil {
		return out, err
	}
	agentViews, err := s.AgentViews(ctx, now)
	if err != nil {
		return out, err
	}
	enabledSet := map[string]bool{}
	for _, a := range enabled {
		enabledSet[a.ID] = true
	}
	for _, item := range snaps {
		accID, _ := item["account_id"].(string)
		if !enabledSet[accID] {
			continue
		}
		out.Instances.Total++
		status, _ := item["status"].(string)
		switch status {
		case "running":
			out.Instances.Running++
		case "stopped":
			out.Instances.Stopped++
		default:
			out.Instances.Other++
		}
		id, _ := item["id"].(string)
		av, hasAV := agentViews[id]
		agStatus := "not_installed"
		if hasAV {
			agStatus = av.Agent.Status
		}
		switch agStatus {
		case "online":
			out.Agents.Online++
		case "offline":
			out.Agents.Offline++
		default:
			out.Agents.NotInstalled++
		}
		countsToward, _ := item["counts_toward_cdt"].(bool)
		if status == "running" && countsToward && agStatus != "online" {
			out.Instances.Unprotected++
		}
	}
	quotas, err := s.Quotas(ctx, enabled, loc, now)
	if err != nil {
		return out, err
	}
	for _, q := range quotas {
		wq := model.WidgetQuotaSummary{
			AccountID:     q.AccountID,
			AccountName:   accNames[q.AccountID],
			Scope:         q.Scope,
			QuotaBytes:    q.QuotaBytes,
			OfficialBytes: q.OfficialBytes,
			UpdatedAt:     q.UpdatedAt,
		}
		var usable *int64
		if q.Estimation != nil && q.Estimation.Status == "ready" && q.Estimation.Estimated != nil && !q.Estimation.CalculatedAt.After(now) && now.Sub(q.Estimation.CalculatedAt) <= 60*time.Second {
			wq.EstimatedBytes = q.Estimation.Estimated
			v := *q.Estimation.Estimated
			if q.OfficialBytes != nil && *q.OfficialBytes > v {
				v = *q.OfficialBytes
			}
			usable = &v
			if q.Estimation.Mode != "aligned" {
				wq.Unverified = true
			}
		} else if q.OfficialBytes != nil {
			v := *q.OfficialBytes
			usable = &v
			wq.Unverified = q.OfficialQuality != "verified"
		}
		if usable != nil {
			rem := q.QuotaBytes - *usable
			wq.RemainingBytes = &rem
		}
		if q.OverageCost != nil && q.OverageCost.EstimatedCost != nil {
			c := q.OverageCost.Currency + " " + *q.OverageCost.EstimatedCost
			wq.OverageCost = &c
		}
		if q.Forecast != nil {
			fc := q.Forecast.CycleEndBytes
			wq.ForecastCycleEndBytes = &fc
		}
		out.Quotas = append(out.Quotas, wq)
	}
	return out, nil
}
