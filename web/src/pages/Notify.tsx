// 通知：Telegram 渠道、告警规则、通知记录。远程控制、静默时段、日报等在后续里程碑加入。
import { useState } from "react";
import { api } from "../api/client";
import { useEvent } from "../api/events";
import { ALERT_RULE, DELIVERY, errorText } from "../api/labels";
import type { AlertRule, Notification } from "../api/types";
import { TelegramForm } from "../components/TelegramForm";
import { Async, Badge, Card, EmptyState, errMsg, Spinner, Switch, useToast, type Level } from "../components/ui";
import { stamp } from "../lib/format";
import { useResource } from "../lib/useResource";
import { NotificationItem } from "./Overview";

const TG_STATUS: Record<string, [Level, string]> = { ok: ["good", "工作正常"], failing: ["critical", "发送失败"], not_configured: ["neutral", "还没有设置"] };

export function Notify() {
  const tg = useResource(api.telegram, []);
  const rules = useResource(api.alertRules, []);
  return (
    <>
      <div className="grid g2">
        <Card title="Telegram" right={tg.data && <Badge level={TG_STATUS[tg.data.status][0]}>{TG_STATUS[tg.data.status][1]}</Badge>}>
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
        <Card title="什么情况下通知" sub="关掉的规则不再发通知">
          <Async res={rules} lines={5}>
            {(list) => (list.length ? <div>{list.map((r) => <RuleRow key={r.key} rule={r} onChange={(nr) => rules.mutate((l) => l.map((x) => (x.key === nr.key ? nr : x)))} />)}</div> : <EmptyState title="暂时没有可用的规则" />)}
          </Async>
        </Card>
      </div>
      <History />
    </>
  );
}

function RuleRow({ rule, onChange }: { rule: AlertRule; onChange: (r: AlertRule) => void }) {
  const toast = useToast();
  const meta = ALERT_RULE[rule.key];
  const p = rule.params;
  const initial = p.thresholds_percent ? p.thresholds_percent.join(", ") : p.consecutive_failures !== undefined ? String(p.consecutive_failures) : p.offline_after_seconds !== undefined ? String(p.offline_after_seconds) : null;
  const [text, setText] = useState(initial ?? "");
  const [busy, setBusy] = useState(false);
  const label = p.thresholds_percent ? ["用到", "% 时"] : p.consecutive_failures !== undefined ? ["连续失败", "次后"] : ["没有上报超过", "秒后"];

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
    if (!nums.length || nums.some((n) => !(n > 0)) || (p.thresholds_percent && nums.some((n) => n > 100))) return toast("请填写大于 0 的数字；百分比不能超过 100。", true);
    const params = p.thresholds_percent ? { thresholds_percent: [...nums].sort((a, b) => a - b) } : p.consecutive_failures !== undefined ? { consecutive_failures: nums[0] } : { offline_after_seconds: nums[0] };
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
            <input id={`rule-${rule.key}`} type="text" value={text} onChange={(e) => setText(e.target.value)} style={{ width: p.thresholds_percent ? 110 : 60, padding: "3px 8px" }} disabled={!rule.enabled} inputMode="numeric" />
            <span>{label[1]}通知</span>
            {text !== initial && <button className="btn sm" onClick={saveParam} disabled={busy}>保存</button>}
          </div>
        )}
      </div>
      {busy && <Spinner />}
      <Switch on={rule.enabled} label={meta.title} disabled={busy} onChange={(v) => patch({ enabled: v }, `“${meta.title}”已${v ? "开启" : "关闭"}`)} />
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
    <Card title="通知记录" sub="新的在前">
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
