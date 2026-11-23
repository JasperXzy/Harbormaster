package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

type BotTransport interface {
	GetUpdates(ctx context.Context, d telegram.Destination, offset int64, timeoutSeconds int) ([]telegram.Update, error)
	ReplyToChat(ctx context.Context, d telegram.Destination, chatID string, text string, buttons [][]telegram.InlineButton) (telegram.Receipt, error)
	AnswerCallback(ctx context.Context, d telegram.Destination, callbackID, text string) error
}

type BotRunner struct {
	Channels         Channels
	Bot              BotTransport
	PowerEnabled     bool
	EIPManageEnabled bool
	Timezone         string
	Log              *slog.Logger
	Now              func() time.Time
}

func (b BotRunner) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b BotRunner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		delay := b.Step(ctx, 8)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (b BotRunner) Step(ctx context.Context, pollSeconds int) time.Duration {
	if b.Channels.Store == nil || b.Bot == nil {
		return 5 * time.Second
	}
	s := b.Channels.Store
	ctrl, err := s.TelegramControl(ctx)
	if err != nil || !ctrl.Enabled || len(ctrl.AllowedUserIDs) == 0 {
		return 5 * time.Second
	}
	dest, state, err := b.Channels.Load(ctx)
	if err != nil {
		return 5 * time.Second
	}
	offset, err := s.TelegramBotOffset(ctx, state.Revision)
	if err != nil {
		return 5 * time.Second
	}
	updates, err := b.Bot.GetUpdates(ctx, dest, offset, pollSeconds)
	if err != nil {
		return 5 * time.Second
	}
	for _, u := range updates {
		nextOffset := u.UpdateID + 1
		if nextOffset > offset {
			offset = nextOffset
			_ = s.SaveTelegramBotOffset(ctx, state.Revision, offset)
		}
		b.handleUpdate(ctx, dest, ctrl, u)
	}
	return 500 * time.Millisecond
}

func (b BotRunner) handleUpdate(ctx context.Context, dest telegram.Destination, ctrl model.TelegramControlSettings, u telegram.Update) {
	ctx = model.WithJobTrigger(ctx, "telegram")
	now := b.now()
	if u.CallbackQuery != nil {
		cb := u.CallbackQuery
		uid := telegram.FormatUserID(cb.From.ID)
		if !slices.Contains(ctrl.AllowedUserIDs, uid) {
			_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "未授权的用户")
			return
		}
		chatID := dest.ChatID
		if cb.Message != nil && cb.Message.Chat.ID != 0 {
			chatID = strconv.FormatInt(cb.Message.Chat.ID, 10)
		}
		b.handleCallback(ctx, dest, chatID, uid, cb, now)
		return
	}
	if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
		return
	}
	msg := u.Message
	uid := telegram.FormatUserID(msg.From.ID)
	if !slices.Contains(ctrl.AllowedUserIDs, uid) {
		// Silently ignore unauthorized users so bot never acts as an oracle.
		return
	}
	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	text := strings.TrimSpace(msg.Text)
	if !strings.HasPrefix(text, "/") {
		return
	}
	fields := strings.Fields(text)
	cmd := fields[0]
	if at := strings.IndexByte(cmd, '@'); at > 0 {
		cmd = cmd[:at]
	}
	args := fields[1:]
	b.handleCommand(ctx, dest, chatID, uid, strings.ToLower(cmd), args, now)
}

func formatGBBot(v int64) string {
	return strconv.FormatFloat(float64(v)/(1<<30), 'f', 1, 64) + " GB"
}

func (b BotRunner) handleCommand(ctx context.Context, dest telegram.Destination, chatID, uid, cmd string, args []string, now time.Time) {
	s := b.Channels.Store
	switch cmd {
	case "/help":
		_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "⚪ Harbormaster 远程控制命令\n/status — 查看实例与采集状态\n/quota — 查看各账号流量与额度\n/start <实例名> — 开机（需二次确认）\n/stop <实例名> — 关机（需二次确认）\n/reboot <实例名> — 重启（需二次确认）\n/changeip <实例名> — 更换弹性公网 IP（需二次确认）", nil)
	case "/status":
		sum, err := s.WidgetSummary(ctx, b.PowerEnabled, b.Timezone, now)
		if err != nil {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟠 读取系统状态失败，请稍后重试。", nil)
			return
		}
		powerNote := "已开启"
		if !sum.PowerEnabled {
			powerNote = "未开启（只读模式）"
		}
		reply := fmt.Sprintf("🟢 Harbormaster 状态摘要\n账号：%d 个启用（异常 %d）\n实例：共 %d 台（运行中 %d · 已停止 %d）\n采集程序：在线 %d · 掉线 %d · 未安装 %d\n电源操作开关：%s",
			sum.Accounts.Enabled, sum.Accounts.Failing, sum.Instances.Total, sum.Instances.Running, sum.Instances.Stopped, sum.Agents.Online, sum.Agents.Offline, sum.Agents.NotInstalled, powerNote)
		_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, reply, nil)
	case "/quota":
		sum, err := s.WidgetSummary(ctx, b.PowerEnabled, b.Timezone, now)
		if err != nil || len(sum.Quotas) == 0 {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "⚪ 当前没有启用的账号额度数据。", nil)
			return
		}
		lines := []string{"⚪ 各账号免费流量额度"}
		for _, q := range sum.Quotas {
			sc := "海外"
			if q.Scope == "mainland" {
				sc = "国内"
			}
			used := "待确认"
			if q.EstimatedBytes != nil {
				used = formatGBBot(*q.EstimatedBytes)
				if q.Unverified {
					used += "（估算）"
				}
			} else if q.OfficialBytes != nil {
				used = formatGBBot(*q.OfficialBytes) + "（官方）"
			}
			line := fmt.Sprintf("• %s %s：%s / %s", q.AccountName, sc, used, formatGBBot(q.QuotaBytes))
			if q.ForecastCycleEndBytes != nil {
				line += " · 月底预计 " + formatGBBot(*q.ForecastCycleEndBytes)
			}
			lines = append(lines, line)
		}
		_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, strings.Join(lines, "\n"), nil)
	case "/changeip":
		if len(args) == 0 {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "用法：/changeip <实例名称或ID>", nil)
			return
		}
		if !b.EIPManageEnabled {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟡 当前服务未开启弹性公网 IP 管理开关（HM_EIP_MANAGE_ENABLED=false），无法远程更换公网 IP。", nil)
			return
		}
		matches, err := s.ResolveInstanceByQuery(ctx, args[0])
		if err != nil || len(matches) == 0 {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟡 找不到匹配的实例："+args[0], nil)
			return
		}
		if len(matches) > 1 {
			names := []string{}
			for _, m := range matches {
				names = append(names, m.Name+" ("+m.ID+")")
			}
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟡 匹配到多台实例，请输入完整名称或 ID：\n"+strings.Join(names, "\n"), nil)
			return
		}
		target := matches[0]
		if target.Kind != "ecs" {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟡 仅阿里云 ECS 实例支持更换弹性公网 IP，轻量应用服务器不支持。", nil)
			return
		}
		tok, err := s.CreateTelegramActionToken(ctx, uid, chatID, target.ID, "changeip", "", now)
		if err != nil {
			return
		}
		_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, fmt.Sprintf("🟠 确认要为 %s 申请新弹性公网 IP 并替换当前受管 EIP 吗？（5 分钟内有效，非面板创建的原有 EIP 将被自动拒绝保护）", target.Name), [][]telegram.InlineButton{
			{{Text: "🔄 确认更换 IP", CallbackData: "act:" + tok}, {Text: "取消", CallbackData: "cancel:" + tok}},
		})
	case "/start", "/stop", "/reboot":
		if cmd == "/start" && len(args) == 0 {
			b.handleCommand(ctx, dest, chatID, uid, "/help", nil, now)
			return
		}
		if len(args) == 0 {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, fmt.Sprintf("用法：%s <实例名称或ID>", cmd), nil)
			return
		}
		if !b.PowerEnabled {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟡 当前服务未开启真实电源操作开关（HM_INSTANCE_POWER_ENABLED=false），无法远程开关机。", nil)
			return
		}
		matches, err := s.ResolveInstanceByQuery(ctx, args[0])
		if err != nil || len(matches) == 0 {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟡 找不到匹配的实例："+args[0], nil)
			return
		}
		if len(matches) > 1 {
			names := []string{}
			for _, m := range matches {
				names = append(names, m.Name+" ("+m.ID+")")
			}
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟡 匹配到多台实例，请输入完整名称或 ID：\n"+strings.Join(names, "\n"), nil)
			return
		}
		target := matches[0]
		action := strings.TrimPrefix(cmd, "/")
		if action == "stop" {
			if target.Kind == "ecs" {
				tokKeep, err := s.CreateTelegramActionToken(ctx, uid, chatID, target.ID, "stop", "keep_charging", now)
				if err != nil {
					return
				}
				row := []telegram.InlineButton{{Text: "🛑 普通停机", CallbackData: "act:" + tokKeep}}
				note := fmt.Sprintf("确认要将 %s 关机吗？（5 分钟内有效）", target.Name)
				if target.SupportsStopCharging {
					tokSave, sErr := s.CreateTelegramActionToken(ctx, uid, chatID, target.ID, "stop", "stop_charging", now)
					if sErr == nil {
						row = append(row, telegram.InlineButton{Text: "💰 节省停机", CallbackData: "act:" + tokSave})
						note += "\n注意：节省停机不收计算费，但非弹性公网 IP 会被回收，再次开机可能遇到库存不足。"
					}
				}
				_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟠 "+note, [][]telegram.InlineButton{
					row,
					{{Text: "取消", CallbackData: "cancel:" + tokKeep}},
				})
				return
			}
			tok, err := s.CreateTelegramActionToken(ctx, uid, chatID, target.ID, "stop", "", now)
			if err != nil {
				return
			}
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, fmt.Sprintf("🟠 确认要将轻量服务器 %s 关机吗？（5 分钟内有效）", target.Name), [][]telegram.InlineButton{
				{{Text: "🛑 确认关机", CallbackData: "act:" + tok}, {Text: "取消", CallbackData: "cancel:" + tok}},
			})
			return
		}
		tok, err := s.CreateTelegramActionToken(ctx, uid, chatID, target.ID, action, "", now)
		if err != nil {
			return
		}
		verb := "开机"
		if action == "reboot" {
			verb = "重启"
		}
		_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, fmt.Sprintf("🟠 确认要将 %s %s吗？（5 分钟内有效）", target.Name, verb), [][]telegram.InlineButton{
			{{Text: "✅ 确认" + verb, CallbackData: "act:" + tok}, {Text: "取消", CallbackData: "cancel:" + tok}},
		})
	}
}

func (b BotRunner) handleCallback(ctx context.Context, dest telegram.Destination, chatID, uid string, cb *telegram.BotCallbackQuery, now time.Time) {
	s := b.Channels.Store
	data := cb.Data
	switch {
	case strings.HasPrefix(data, "cancel:"):
		tok := strings.TrimPrefix(data, "cancel:")
		_, _ = s.ConsumeTelegramActionToken(ctx, tok, uid, true, now)
		_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "已取消操作")
		_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "⚪ 已取消电源操作。", nil)
	case strings.HasPrefix(data, "act:"):
		tok := strings.TrimPrefix(data, "act:")
		item, err := s.ConsumeTelegramActionToken(ctx, tok, uid, false, now)
		if err != nil {
			_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "确认码已失效或已使用")
			return
		}
		sum := sha256.Sum256([]byte("tg-act|" + item.Token))
		if item.Action == "changeip" {
			if !b.EIPManageEnabled {
				_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "EIP 管理开关未启用")
				return
			}
			j, eErr := s.EnqueueEIPRotateJob(ctx, item.InstanceID, model.EIPRotateInput{BandwidthMbps: 100, InternetChargeType: "PayByTraffic"}, hex.EncodeToString(sum[:]), now)
			if eErr != nil {
				_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "提交失败")
				msg := eErr.Error()
				if errors.Is(eErr, store.ErrUnmanagedEIPProtected) {
					msg = "当前绑定的 EIP 不是由本面板创建的，出于安全保护拒绝解绑与释放"
				} else if errors.Is(eErr, store.ErrEIPRotateUnsupported) {
					msg = "该实例带有固定公网 IP 或不支持弹性公网 IP 轮换"
				}
				_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🔴 更换公网 IP 提交失败："+msg, nil)
				return
			}
			_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "已提交更换")
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, fmt.Sprintf("🟢 已提交更换弹性公网 IP 任务（%s），后台正在执行。", j.ID), nil)
			return
		}
		if !b.PowerEnabled {
			_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "电源开关未启用")
			return
		}
		j, err := s.EnqueuePowerJob(ctx, item.InstanceID, item.Action, item.StopMode, hex.EncodeToString(sum[:]), now)
		if err != nil {
			_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "入队失败")
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🔴 电源任务提交失败："+err.Error(), nil)
			return
		}
		_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "已提交执行")
		_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, fmt.Sprintf("🟢 已提交电源操作（任务 %s），后台正在执行并确认状态。", j.ID), nil)
	case strings.HasPrefix(data, "intent:keep:") || strings.HasPrefix(data, "intent:restore:"):
		decision := "keep_stopped"
		instID := strings.TrimPrefix(data, "intent:keep:")
		if strings.HasPrefix(data, "intent:restore:") {
			decision = "restore_running"
			instID = strings.TrimPrefix(data, "intent:restore:")
		}
		desired, jobID, err := s.ConfirmInstanceIntent(ctx, instID, decision, b.PowerEnabled, now)
		if err != nil {
			_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "处理失败")
			return
		}
		_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "已确认")
		if desired == "stopped" {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟢 已确认保持停止，不会触发自动保活。", nil)
		} else if jobID != nil {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, fmt.Sprintf("🟢 已确认恢复运行，已提交开机任务（%s）。", *jobID), nil)
		} else {
			_, _ = b.Bot.ReplyToChat(ctx, dest, chatID, "🟢 已将期望状态设为运行。", nil)
		}
	default:
		_ = b.Bot.AnswerCallback(ctx, dest, cb.ID, "")
	}
}
