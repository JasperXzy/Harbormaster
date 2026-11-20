package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrAuditFilter = errors.New("invalid_audit_filter")

// Non-sensitive configuration values passed to auditDiff record before/after.
// Credentials, tokens, hashes and ciphertexts must never be passed here.
type auditChange struct {
	name          string
	before, after any
}

func auditChanges(changes ...auditChange) []string {
	fields := []string{}
	for _, change := range changes {
		if !reflect.DeepEqual(change.before, change.after) {
			fields = append(fields, change.name)
		}
	}
	return fields
}

func auditDiff(changes ...auditChange) model.AuditDetails {
	fields := []string{}
	var before, after map[string]any
	for _, change := range changes {
		if !reflect.DeepEqual(change.before, change.after) {
			if before == nil {
				before = map[string]any{}
				after = map[string]any{}
			}
			fields = append(fields, change.name)
			before[change.name] = change.before
			after[change.name] = change.after
		}
	}
	return model.AuditDetails{ChangedFields: fields, Before: before, After: after}
}

var auditActionSummary = map[string]string{
	"account_created":                    "创建阿里云账号",
	"account_updated":                    "修改阿里云账号配置",
	"account_disabled":                   "停用阿里云账号",
	"alert_rule_updated":                 "修改告警规则",
	"quiet_hours_updated":                "修改静默与维护时段",
	"telegram_channel_updated":           "修改 Telegram 通知渠道",
	"telegram_control_updated":           "修改 Telegram 远程控制",
	"branding_updated":                   "修改面板名称与图标",
	"settings_updated":                   "修改系统与保留策略",
	"estimation_settings_updated":        "修改实时估算设置",
	"traffic_pricing_updated":            "修改流量阶梯单价",
	"instance_metadata_updated":          "修改实例备注、分组与标签",
	"instance_intent_confirmed":          "确认实例期望状态",
	"quota_rule_saved":                   "保存额度保护规则",
	"quota_rule_deleted":                 "删除额度保护规则",
	"quota_simulation_saved":             "注入模拟用量",
	"quota_simulation_cleared":           "清除模拟用量",
	"quota_exemption_saved":              "设置额度保护临时豁免",
	"quota_exemption_cleared":            "清除额度保护临时豁免",
	"power_schedule_saved":               "保存定时开关机规则",
	"power_schedule_deleted":             "删除定时开关机规则",
	"keepalive_rule_saved":               "保存实例保活规则",
	"keepalive_rule_deleted":             "删除实例保活规则",
	"monthly_reset_saved":                "修改月初自动开机规则",
	"api_key_created":                    "创建 API Key",
	"api_key_revoked":                    "撤销 API Key",
	"agent_registration_issued":          "生成采集程序安装码",
	"agent_registered":                   "采集程序完成注册",
	"agent_revoked":                      "撤销采集程序凭证",
	"instance_power_requested":           "提交实例电源操作",
	"instance_power_started":             "开始处理实例电源任务",
	"instance_power_dispatch_started":    "向阿里云发送电源指令",
	"instance_power_dispatch_recorded":   "记录阿里云电源指令响应",
	"instance_power_finished":            "实例电源任务结束",
	"instance_release_requested":         "提交实例释放任务",
	"instance_release_dispatch_started":  "向阿里云发送实例释放指令",
	"instance_release_dispatch_recorded": "记录阿里云实例释放响应",
	"instance_release_finished":          "实例释放任务结束",
	"probe_target_created":               "创建测速目标",
	"probe_target_updated":               "修改测速目标",
	"probe_target_deleted":               "删除测速目标",
	"probe_plan_created":                 "创建测速组合",
	"probe_plan_updated":                 "修改测速组合",
	"probe_plan_deleted":                 "删除测速组合",
	"trace_schedule_updated":             "修改每日线路快照设置",
}

func auditSummaryText(e model.AuditEntry) string {
	if s, ok := auditActionSummary[e.Action]; ok {
		return s
	}
	return "系统审计：" + e.Action
}

type auditEvent struct {
	Action, TargetType, TargetID, AccountID, JobID string
	Details                                        model.AuditDetails
	Actor                                          *model.AuditActor
}

func appendAudit(ctx context.Context, tx *sql.Tx, event auditEvent, now time.Time) error {
	actor := model.AuditActorForContext(ctx)
	if event.Actor != nil {
		actor = *event.Actor
	}
	if !model.ValidAuditActor(actor.Kind) || event.Action == "" || event.TargetType == "" || event.TargetID == "" {
		return errors.New("invalid_audit_event")
	}
	if event.Details.ChangedFields == nil {
		event.Details.ChangedFields = []string{}
	}
	raw, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_log(created_at,actor_kind,actor_id,action,target_type,target_id,account_id,job_id,details_json) VALUES(?,?,?,?,?,?,nullif(?,''),nullif(?,''),?)", timestamp(now), actor.Kind, actor.ID, event.Action, event.TargetType, event.TargetID, event.AccountID, event.JobID, string(raw))
	return err
}

const auditColumns = "seq,created_at,actor_kind,actor_id,action,target_type,target_id,account_id,job_id,details_json"

func readAudit(row rowScanner) (model.AuditEntry, error) {
	var e model.AuditEntry
	var seq int64
	var at, raw string
	err := row.Scan(&seq, &at, &e.Actor.Kind, &e.Actor.ID, &e.Action, &e.TargetType, &e.TargetID, &e.AccountID, &e.JobID, &raw)
	if err != nil {
		return e, err
	}
	e.ID = strconv.FormatInt(seq, 10)
	if e.CreatedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
		return e, err
	}
	if err = json.Unmarshal([]byte(raw), &e.Details); err != nil {
		return e, err
	}
	if e.Details.ChangedFields == nil {
		e.Details.ChangedFields = []string{}
	}
	e.Summary = auditSummaryText(e)
	return e, nil
}
func (s *Store) AuditLog(ctx context.Context, f model.AuditFilter) ([]model.AuditEntry, error) {
	if !f.Validate() {
		return nil, ErrAuditFilter
	}
	where := []string{}
	args := []any{}
	if f.Before > 0 {
		where = append(where, "seq<?")
		args = append(args, f.Before)
	}
	for _, pair := range []struct{ column, value string }{{"actor_kind", f.Actor}, {"action", f.Action}, {"target_type", f.TargetType}, {"target_id", f.TargetID}, {"account_id", f.AccountID}} {
		if pair.value != "" {
			where = append(where, pair.column+"=?")
			args = append(args, pair.value)
		}
	}
	if f.From != nil {
		where = append(where, "created_at>=? AND created_at<?")
		args = append(args, timestamp(*f.From), timestamp(*f.To))
	}
	query := "SELECT " + auditColumns + " FROM audit_log"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY seq DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		e, err := readAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
