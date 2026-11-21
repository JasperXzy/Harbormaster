package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Message copy follows docs/design/telegram-templates.md (frontend-owned).
// Panel history and Telegram show the same rendered text. Clauses that would
// claim unavailable data, such as worst-case estimation while an agent is
// offline, are omitted rather than invented. Missing values drop the clause.

// alertFacts carries the evidence a rule observed, for rendering only.
type alertFacts struct {
	failures             int
	since                *time.Time
	reason               string
	offline              time.Duration
	expiresAt            *time.Time
	renewal              string
	publicMbps, specKbps int64
	amount, currency     string
	limit                string
	value, threshold     float64
	resourceKind         string
	resourceName         string
	resourceReason       string
	scope                string
	usedBytes            int64
	hasUsed              bool
	quotaBytes           int64
	dailyAvgBytes        int64
	exceedsOn            string
}

func severitySymbol(kind, severity string) string {
	switch {
	case kind == "recovery":
		return "🟢"
	case kind == "test":
		return "⚪"
	case severity == "critical":
		return "🔴"
	case severity == "serious":
		return "🟠"
	default:
		return "🟡"
	}
}

func copyLocation(ctx context.Context, q queryRower) *time.Location {
	loc, err := businessLocation(ctx, q, "")
	if err != nil {
		return time.UTC
	}
	return loc
}

// Today shows only 17:42; other dates show 11-10 22:05.
func copyTime(t, now time.Time, loc *time.Location) string {
	local, today := t.In(loc), now.In(loc)
	if local.Year() == today.Year() && local.YearDay() == today.YearDay() {
		return local.Format("15:04")
	}
	return local.Format("01-02 15:04")
}

func copyDuration(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return strconv.Itoa(int(d/time.Second)) + " 秒"
	case d < 2*time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " 分钟"
	default:
		return strconv.Itoa(int(d/time.Hour)) + " 小时"
	}
}

func copyNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func copyScope(scope string) string {
	if scope == "mainland" {
		return "国内"
	}
	return "海外"
}

// Readable names fall back to stable IDs; a lookup failure never blocks an alert.
func copyName(ctx context.Context, q queryRower, query, id string) string {
	var name string
	if err := q.QueryRowContext(ctx, query, id).Scan(&name); err != nil || strings.TrimSpace(name) == "" {
		return id
	}
	return name
}
func accountCopyName(ctx context.Context, q queryRower, id string) string {
	return copyName(ctx, q, "SELECT name FROM account WHERE id=?", id)
}
func instanceCopyName(ctx context.Context, q queryRower, id string) string {
	return copyName(ctx, q, "SELECT name FROM instance WHERE id=?", id)
}
func planCopyName(ctx context.Context, q queryRower, id string) string {
	plan, err := readProbePlan(ctx, q, id)
	if err != nil || strings.TrimSpace(plan.Name) == "" {
		return id
	}
	return plan.Name
}

// Same wording as web/src/api/labels.ts; unknown codes omit the reason.
var accountFailureReason = map[string]string{
	"access_key_invalid": "AccessKey 不存在或已被禁用",
	"permission_denied":  "阿里云拒绝了读取请求，请检查只读权限",
	"aliyun_throttled":   "阿里云接口限流",
	"aliyun_unreachable": "连不上阿里云接口",
}

var renewalCopy = map[string]string{
	"auto_renewal":   "已开自动续费",
	"manual_renewal": "需要手动续费",
	"no_renewal":     "设置了到期不续",
}

var idleKindCopy = map[string]string{"eip": "公网 IP", "disk": "云盘", "ecs": "ECS 实例", "swas": "轻量服务器"}
var idleReasonCopy = map[string]string{
	"unbound_eip":               "没有绑定任何实例",
	"stopped_subscription":      "包年包月实例已停机，仍在计费",
	"stopped_keep_charging":     "停机后仍在计费",
	"stopped_not_charge_exempt": "停机后不享受停机不收费",
	"unattached_cloud_disk":     "没有挂载到实例",
	"disk_on_stopped_instances": "挂载的实例都已停机",
}

func alertCopy(ctx context.Context, q queryRower, o alertObservation, kind, severity string, now time.Time) (string, string) {
	symbol := severitySymbol(kind, severity)
	recovered := kind == "recovery"
	f := o.facts
	account := func() string { return accountCopyName(ctx, q, o.account) }
	instance := func() string { return instanceCopyName(ctx, q, o.instance) }
	plan := func() string { return planCopyName(ctx, q, o.plan) }
	switch o.rule {
	case "account_api_failure":
		if recovered {
			return symbol + " 账号 " + account() + " 恢复连接", ""
		}
		body := "连续 " + strconv.Itoa(f.failures) + " 次失败"
		if reason := accountFailureReason[f.reason]; reason != "" {
			body += "：" + reason
		}
		body += "。"
		if f.since != nil {
			body += "它的数据停在 " + copyTime(*f.since, now, copyLocation(ctx, q)) + "。"
		}
		return symbol + " 账号 " + account() + " 连不上阿里云", body
	case "cdt_official_unavailable":
		if recovered {
			return symbol + " " + account() + " 的官方流量数据恢复读取", ""
		}
		return symbol + " 取不到 " + account() + " 的官方流量数据", "连续 " + strconv.Itoa(f.failures) + " 次读取失败，面板暂时显示上一次读到的数值。"
	case "agent_offline":
		if recovered {
			return symbol + " " + instance() + " 的采集程序恢复上报", ""
		}
		return symbol + " " + instance() + " 的采集程序掉线了", "已经 " + copyDuration(f.offline) + "没有收到上报。\n请登录这台机器，检查 hm-agent 是否在运行。"
	case "instance_expiring":
		if recovered {
			return symbol + " " + instance() + " 已不再临近到期", ""
		}
		title := symbol + " " + instance() + " 快到期了"
		body := ""
		if f.expiresAt != nil {
			body = "将在 " + f.expiresAt.In(copyLocation(ctx, q)).Format("01-02 15:04") + " 到期。"
		}
		if r := renewalCopy[f.renewal]; r != "" {
			body += r + "。"
		}
		return title, body
	case "spec_bandwidth_insufficient":
		if recovered {
			return symbol + " " + instance() + " 的公网带宽不再超过规格上限", ""
		}
		return symbol + " " + instance() + " 的公网带宽超过了规格上限", fmt.Sprintf("公网带宽是 %d Mbps，但这台机器的规格最多只能跑约 %d Mbps，多出来的带宽用不上。", f.publicMbps, (f.specKbps+500)/1000)
	case "balance_low":
		if recovered {
			return symbol + " 账号 " + account() + " 的余额恢复到提醒线以上", ""
		}
		return symbol + " 账号 " + account() + " 的余额不足", "可用余额 " + f.amount + " " + f.currency + "，低于设置的 " + f.limit + " " + f.currency + "。"
	case "network_latency":
		if recovered {
			return symbol + " " + plan() + " 的网络延迟恢复正常", ""
		}
		return symbol + " " + plan() + " 的网络延迟升高", "平均延迟 " + copyNumber(f.value) + " ms，超过设置的 " + copyNumber(f.threshold) + " ms。"
	case "network_loss":
		if recovered {
			return symbol + " " + plan() + " 的探测恢复正常", ""
		}
		return symbol + " " + plan() + " 的探测失败比例偏高", "失败 " + copyNumber(f.value) + "%，超过设置的 " + copyNumber(f.threshold) + "%。"
	case "network_unreachable":
		if recovered {
			return symbol + " " + plan() + " 的目标恢复可达", ""
		}
		return symbol + " " + plan() + " 的目标连不上了", "连续几次探测全部失败。"
	case "cdt_forecast_exceed":
		sc := copyScope(f.scope)
		if recovered {
			return symbol + " " + account() + " 的" + sc + "额度预计月底前够用了", ""
		}
		when := f.exceedsOn
		if when == "" {
			when = "月底前"
		}
		title := symbol + " " + account() + " 的" + sc + "额度预计 " + when + " 用完"
		body := "最近 7 天平均每天 " + copyGB(f.dailyAvgBytes)
		if f.hasUsed {
			body += "，现在已用 " + copyGB(f.usedBytes) + " / " + copyGB(f.quotaBytes)
		}
		body += "。"
		return title, body
	case "idle_resource_detected":
		what := idleKindCopy[f.resourceKind]
		if what == "" {
			what = "资源"
		}
		name := f.resourceName
		if name == "" {
			name = o.resource
		}
		if recovered {
			return symbol + " " + account() + " 的" + what + " " + name + " 不再闲置", ""
		}
		body := ""
		if reason := idleReasonCopy[f.resourceReason]; reason != "" {
			body = reason + "。"
		}
		return symbol + " " + account() + " 有闲置的" + what + "：" + name, body + "如果确实不用了，可以考虑释放。"
	case "cpu_credit_low":
		if recovered {
			return symbol + " " + instance() + " 的 CPU 积分已恢复", ""
		}
		pctStr := strconv.FormatFloat(f.value, 'f', 1, 64) + "%"
		body := "当前积分占 24 小时累积上限的 " + pctStr
		if f.limit != "" {
			body += "（" + f.limit + "）"
		}
		body += "，低于阈值 " + copyNumber(f.threshold) + "%。"
		return symbol + " " + instance() + " 的 CPU 积分偏低", body
	case "swas_traffic_usage":
		if recovered {
			return symbol + " 轻量服务器 " + instance() + " 的套餐流量预计不再超额", ""
		}
		body := ""
		if f.hasUsed {
			body = "当前已用 " + copyGB(f.usedBytes) + " / " + copyGB(f.quotaBytes) + "，按当前速度预计早于重置日用尽。"
		}
		return symbol + " 轻量服务器 " + instance() + " 的套餐流量预计在重置前用尽", body
	}
	// Rules without a frontend template keep a neutral, truthful sentence.
	if recovered {
		return symbol + " 告警已恢复：" + o.rule, ""
	}
	return symbol + " 告警：" + o.rule, ""
}

func quotaProtectionCopy(ctx context.Context, q queryRower, r model.ProtectionRule, d model.QuotaDecision, rehearsal bool) (string, string) {
	title := severitySymbol("alert", "critical") + " " + accountCopyName(ctx, q, r.AccountID) + " 的" + copyScope(r.Scope) + "流量到了保护线：" + r.Name
	used := ""
	if d.Value != nil && d.Threshold != nil {
		used = "已用 " + copyGB(*d.Value) + "，保护线 " + copyGB(*d.Threshold) + "。"
	}
	switch {
	case r.Action != "stop_notify":
		return title, used
	case rehearsal:
		return title, used + "这次只是演练，没有关机。"
	default:
		return title, used + "正在按规则关机。"
	}
}

// Traffic uses GB (1024³ bytes) with one decimal, as in the templates.
func copyGB(b int64) string {
	return strconv.FormatFloat(float64(b)/(1<<30), 'f', 1, 64) + " GB"
}

func quotaPowerSeverity(status string) string {
	if status == "succeeded" {
		return "warning"
	}
	return "critical"
}

func quotaPowerCopy(ctx context.Context, q queryRower, instance, status string) string {
	name := instanceCopyName(ctx, q, instance)
	if status == "succeeded" {
		return severitySymbol("alert", "warning") + " 额度保护已把 " + name + " 关机"
	}
	return severitySymbol("alert", "critical") + " 额度保护没能把 " + name + " 关机，请尽快手动处理"
}
