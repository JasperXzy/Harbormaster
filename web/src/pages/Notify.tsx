// 通知：Telegram 渠道、远程控制白名单、静默时段与维护窗口、告警规则、通知记录。
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useEvent } from "../api/events";
import { ALERT_RULE, DELIVERY, errorText } from "../api/labels";
import type { AlertRule, Notification, QuietHours, TelegramChannel, TelegramControlSettings } from "../api/types";
import { TelegramForm } from "../components/TelegramForm";
import { Async, Badge, Card, EmptyState, errMsg, Spinner, Switch, useToast, type Level } from "../components/ui";
import { stamp } from "../lib/format";
import { useResource } from "../lib/useResource";
import { NotificationItem } from "./Overview";

const TG_STATUS: Record<TelegramChannel["status"], [Level, string]> = { ok: ["good", "工作正常"], failing: ["critical", "发送失败"], not_tested: ["warning", "已保存，还没测试"], not_configured: ["neutral", "还没有设置"] };

export function Notify() {
  const tg = useResource(api.telegram, []);
  const ctrl = useResource(api.telegramControl, []);
  const quiet = useResource(api.quietHours, []);
  const rules = useResource(() => api.alertRules(true), []);
  return (
    <>
      <div className="grid g2">
        <div className="stack">
          <Card title="Telegram 通知渠道" right={tg.data && <Badge level={TG_STATUS[tg.data.status][0]}>{TG_STATUS[tg.data.status][1]}</Badge>}>
            <Async res={tg} lines={4}>
              {(v) => (
                <>
                  {v.status === "failing" && v.last_error && <p className="field-error" style={{ margin: "0 0 12px" }}>最近一次发送失败：{errorText(v.last_error.code)}</p>}
                  <TelegramForm value={v} onSaved={(nv) => tg.mutate(() => nv)} />
                  {v.last_success_at && <p className="note">最近一次成功发送：{stamp(v.last_success_at)}</p>}
                </>
              )}
            </Async>
          </Card>

          <Card title="Telegram 远程控制" sub="允许白名单里的 Telegram 用户发指令查状态、开关机">
            <Async res={ctrl} lines={3}>
              {(v) => <TelegramControlForm value={v} onSaved={(nv) => ctrl.mutate(() => nv)} />}
            </Async>
          </Card>

          <Card title="静默时段与临时维护期" sub="非紧急通知在静默时段只记录不打扰；严重告警（如掉线、额度保护）不受限制">
            <Async res={quiet} lines={3}>
              {(v) => <QuietHoursForm value={v} onSaved={(nv) => quiet.mutate(() => nv)} />}
            </Async>
          </Card>
        </div>

        <Card title="什么情况下通知">
          <Async res={rules} lines={8}>
            {(list) => (list.length ? <div>{list.map((r) => <RuleRow key={r.key} rule={r} onChange={(nr) => rules.mutate((l) => l.map((x) => (x.key === nr.key ? nr : x)))} />)}</div> : <EmptyState title="暂时没有可用的规则" />)}
          </Async>
        </Card>
      </div>
      <History />
    </>
  );
}

function TelegramControlForm({ value, onSaved }: { value: TelegramControlSettings; onSaved: (v: TelegramControlSettings) => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [idsText, setIdsText] = useState(value.allowed_user_ids.join(", "));
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const ids = [...new Set(idsText.split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean))];
    if (ids.some((id) => !/^\d+$/.test(id))) {
      toast("Telegram 用户 ID 必须是纯数字（不是 @用户名）。", true);
      return;
    }
    if (enabled && !ids.length) {
      toast("开启远程控制时，必须至少填写一个允许操作的 Telegram 用户数字 ID。", true);
      return;
    }
    setBusy(true);
    try {
      const saved = await api.updateTelegramControl({ revision: value.revision, enabled, allowed_user_ids: ids });
      onSaved(saved);
      toast("Telegram 远程控制已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      <div className="inline">
        <Switch on={enabled} label="开启 Telegram 远程控制" onChange={setEnabled} />
        <span>开启远程控制监听（复用上方机器人的 Token 与代理配置）</span>
      </div>
      <div>
        <label className="lbl" htmlFor="tg-ctrl-ids">允许操作的用户数字 ID 白名单（逗号分隔）</label>
        <input id="tg-ctrl-ids" type="text" value={idsText} onChange={(e) => setIdsText(e.target.value)} placeholder="例如：123456789" />
        <div className="field-help">不在白名单内的消息会被静默忽略。可用指令：<span className="mono">/status</span>、<span className="mono">/quota</span>、<span className="mono">/start 实例名</span>、<span className="mono">/stop 实例名</span>、<span className="mono">/reboot 实例名</span>（写操作均需点击二次确认按钮）。</div>
      </div>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button className="btn sm primary" disabled={busy}>{busy && <Spinner />} 保存远程控制</button>
      </div>
    </form>
  );
}

function QuietHoursForm({ value, onSaved }: { value: QuietHours; onSaved: (v: QuietHours) => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [start, setStart] = useState(value.start);
  const [end, setEnd] = useState(value.end);
  const [maintMins, setMaintMins] = useState("0");
  const [busy, setBusy] = useState(false);

  const hasActiveMaint = Boolean(value.maintenance && Date.parse(value.maintenance.end_at) > Date.now());

  const save = async (clearMaint = false) => {
    if (!/^\d{2}:\d{2}$/.test(start) || !/^\d{2}:\d{2}$/.test(end)) {
      toast("时间格式请填 HH:MM，例如 23:30。", true);
      return;
    }
    const mins = Number(maintMins);
    const nowIso = new Date().toISOString();
    const maintenance = clearMaint
      ? null
      : mins > 0
        ? { start_at: nowIso, end_at: new Date(Date.now() + mins * 60_000).toISOString() }
        : hasActiveMaint
          ? value.maintenance
          : null;
    setBusy(true);
    try {
      const saved = await api.updateQuietHours({ revision: value.revision, enabled, start, end, maintenance });
      setMaintMins("0");
      onSaved(saved);
      toast(clearMaint ? "已结束临时维护期" : "静默时段已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <div className="inline">
        <Switch on={enabled} label="开启每日静默时段" onChange={setEnabled} />
        <span>每天</span>
        <input type="text" value={start} onChange={(e) => setStart(e.target.value)} style={{ width: 72, padding: "4px 8px" }} aria-label="静默开始时间" />
        <span>至</span>
        <input type="text" value={end} onChange={(e) => setEnd(e.target.value)} style={{ width: 72, padding: "4px 8px" }} aria-label="静默结束时间" />
        {value.active && <Badge level="neutral">当前处于静默中</Badge>}
      </div>

      {hasActiveMaint ? (
        <div className="inline">
          <Badge level="warning">临时维护中 · 持续到 {stamp(value.maintenance!.end_at)}</Badge>
          <button type="button" className="btn sm" disabled={busy} onClick={() => save(true)}>结束维护期</button>
        </div>
      ) : (
        <div className="inline">
          <label className="sub" htmlFor="qh-maint">开启临时维护静默：</label>
          <select id="qh-maint" value={maintMins} onChange={(e) => setMaintMins(e.target.value)}>
            <option value="0">不开启</option>
            <option value="30">30 分钟</option>
            <option value="60">1 小时</option>
            <option value="180">3 小时</option>
          </select>
        </div>
      )}

      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button type="button" className="btn sm primary" disabled={busy} onClick={() => save(false)}>{busy && <Spinner />} 保存静默设置</button>
      </div>
    </div>
  );
}

function RuleRow({ rule, onChange }: { rule: AlertRule; onChange: (r: AlertRule) => void }) {
  const toast = useToast();
  const meta = ALERT_RULE[rule.key] ?? { title: rule.key, desc: "" };
  const p = rule.params;
  const editableKind =
    p.thresholds_percent !== undefined
      ? "thresholds"
      : rule.key === "cdt_estimation_deviation"
        ? "deviation"
        : p.consecutive_failures !== undefined && p.probe_thresholds === undefined
          ? "failures"
          : p.offline_after_seconds !== undefined
            ? "offline"
            : p.before_days !== undefined
              ? "days"
              : null;

  const initial =
    editableKind === "thresholds"
      ? p.thresholds_percent!.join(", ")
      : editableKind === "deviation" || editableKind === "failures"
        ? String(p.consecutive_failures ?? 3)
        : editableKind === "offline"
          ? String(p.offline_after_seconds ?? 30)
          : editableKind === "days"
            ? String(p.before_days ?? 7)
            : null;

  const [text, setText] = useState(initial ?? "");
  const [busy, setBusy] = useState(false);
  const label =
    editableKind === "thresholds"
      ? ["用到", "% 时"]
      : editableKind === "deviation" || editableKind === "failures"
        ? ["连续", "次后"]
        : editableKind === "days"
          ? ["到期前", "天"]
          : ["没有上报超过", "秒后"];

  const patch = async (body: Parameters<typeof api.updateAlertRule>[1], okText: string) => {
    setBusy(true);
    try {
      onChange(await api.updateAlertRule(rule.key, body));
      toast(okText);
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };
  const saveParam = () => {
    const nums = text.split(/[,，\s]+/).filter(Boolean).map(Number);
    if (!nums.length || nums.some((n) => !(n > 0)) || (editableKind === "thresholds" && nums.some((n) => n > 100))) {
      return toast("请填写大于 0 的数字；百分比不能超过 100。", true);
    }
    const params =
      editableKind === "thresholds"
        ? { thresholds_percent: [...nums].sort((a, b) => a - b) }
        : editableKind === "deviation"
          ? { consecutive_failures: Math.round(nums[0]), recovery_samples: p.recovery_samples ?? 3 }
          : editableKind === "failures"
            ? { consecutive_failures: Math.round(nums[0]) }
            : editableKind === "days"
              ? { before_days: Math.round(nums[0]) }
              : { offline_after_seconds: Math.round(nums[0]) };
    patch({ params }, "已保存");
  };

  return (
    <div className="rowline" style={{ alignItems: "flex-start" }}>
      <div className="grow">
        <b>{meta.title}</b>
        {rule.urgent && <span className="tag" style={{ marginLeft: 6 }} title="紧急：不受静默时段影响">紧急</span>}
        <div className="sub">{meta.desc}</div>
        {initial !== null && (
          <div className="inline sub" style={{ marginTop: 6 }}>
            <label htmlFor={`rule-${rule.key}`}>{label[0]}</label>
            <input id={`rule-${rule.key}`} type="text" value={text} onChange={(e) => setText(e.target.value)} style={{ width: editableKind === "thresholds" ? 110 : 60, padding: "3px 8px" }} disabled={!rule.enabled} inputMode="numeric" />
            <span>{label[1]}通知</span>
            {text !== initial && <button className="btn sm" onClick={saveParam} disabled={busy}>保存</button>}
          </div>
        )}
      </div>
      {busy && <Spinner />}
      <Switch
        on={rule.enabled}
        label={meta.title}
        disabled={busy}
        onChange={(v) =>
          patch(
            rule.key === "cdt_estimation_deviation" && v
              ? { enabled: v, params: { consecutive_failures: p.consecutive_failures ?? 3, recovery_samples: p.recovery_samples ?? 3 } }
              : { enabled: v },
            `“${meta.title}”已${v ? "开启" : "关闭"}`,
          )
        }
      />
    </div>
  );
}

function History() {
  const [items, setItems] = useState<Notification[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [more, setMore] = useState(false);
  const first = useResource(async () => {
    const r = await api.notifications({ limit: 20 });
    setItems(r.items);
    setCursor(r.next_cursor);
    return true;
  }, []);
  useEvent("notification.created", (n) => setItems((l) => (l.some((x) => x.id === n.id) ? l : [n, ...l])));
  const loadMore = async () => {
    if (!cursor) return;
    setMore(true);
    try {
      const r = await api.notifications({ limit: 20, cursor });
      setItems((l) => [...l, ...r.items]);
      setCursor(r.next_cursor);
    } finally {
      setMore(false);
    }
  };
  return (
    <Card title="通知记录">
      <Async res={first} lines={5}>
        {() =>
          !items.length ? <EmptyState title="还没有通知">用量越过档位、采集程序掉线、账号连接失败时，会发通知并记录在这里。可以先点上面的“发送测试消息”试一下。</EmptyState> : (
            <>
              <div className="alist">
                {items.map((n) => {
                  const [dl, dt] = DELIVERY[n.delivery_status];
                  return (
                    <div key={n.id} style={{ borderBottom: "1px solid var(--border)" }}>
                      <NotificationItem n={n} />
                      {n.delivery_status !== "sent" && <div style={{ margin: "-4px 0 10px 2px" }}><Badge level={dl}>{dt}</Badge></div>}
                    </div>
                  );
                })}
              </div>
              {cursor && <div style={{ textAlign: "center", marginTop: 12 }}><button className="btn" onClick={loadMore} disabled={more}>{more && <Spinner />} 加载更早的</button></div>}
            </>
          )
        }
      </Async>
    </Card>
  );
}
