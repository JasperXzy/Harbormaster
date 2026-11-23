// 策略与通知工作区：统一承载自动停开机策略、跨账号主备容灾、紧凑摘要卡片式多渠道通知中心（Telegram / SMTP / Webhook / 简报 / 远程控制 / 静默窗）、分类告警触发规则及历史记录。
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useEvent } from "../api/events";
import { waitForJob } from "../api/jobs";
import { ALERT_RULE, DECISION_OUTCOME, DECISION_STATE, DELIVERY, errorText, SCOPE, STOP_MODE } from "../api/labels";
import type * as T from "../api/types";
import { BellIcon, GlobeIcon, PlusIcon, ShieldIcon, ZapIcon } from "../components/icons";
import { TelegramForm } from "../components/TelegramForm";
import { Async, Badge, Banner, Card, Confirm, Drawer, EmptyState, ErrorState, errMsg, Modal, SegToggle, Skeleton, Spinner, SubTabs, Switch, useToast, type Level } from "../components/ui";
import { useShared } from "../lib/data";
import { GB, gb, stamp } from "../lib/format";
import { Link, useQueryTab } from "../lib/router";
import { useResource } from "../lib/useResource";
import { NotificationItem } from "./Overview";

const WEEKDAYS: [number, string][] = [[1, "周一"], [2, "周二"], [3, "周三"], [4, "周四"], [5, "周五"], [6, "周六"], [0, "周日"]];
const CH_STATUS: Record<T.TelegramChannel["status"], [Level, string]> = {
  ok: ["good", "工作正常"],
  failing: ["critical", "投递失败"],
  not_tested: ["warning", "待测试"],
  not_configured: ["neutral", "未配置"],
};

export function Automation() {
  const [tab, setTab] = useQueryTab("policies", ["policies", "channels", "rules", "history"] as const);
  const { accounts, instances, reloadAll } = useShared();

  if (accounts.error) return <div className="card"><ErrorState error={accounts.error} onRetry={accounts.reload} /></div>;
  if (!accounts.data || !instances.data) return <div className="card"><Skeleton lines={6} /></div>;

  return (
    <>
      <SubTabs
        value={tab}
        onChange={setTab}
        tabs={[
          { key: "policies", label: "自动控制策略" },
          { key: "channels", label: "通知渠道与简报" },
          { key: "rules", label: "告警触发规则" },
          { key: "history", label: "通知与判定记录" },
        ]}
      />

      {tab === "policies" && <PoliciesSubWorkspace accounts={accounts.data} instances={instances.data.items} reloadAll={reloadAll} />}
      {tab === "channels" && <ChannelsSubWorkspace />}
      {tab === "rules" && <AlertRulesSubWorkspace />}
      {tab === "history" && <HistorySubWorkspace accounts={accounts.data} instances={instances.data.items} />}
    </>
  );
}

/* ============================================================================
 * Sub-Workspace 1: 自动控制策略 (Quota Protection, Schedules, Failover, Keepalive)
 * ========================================================================== */
function PoliciesSubWorkspace({ accounts, instances, reloadAll }: { accounts: T.Account[]; instances: T.Instance[]; reloadAll: () => void }) {
  const toast = useToast();
  const status = useResource(api.protectionStatus, []);
  const rules = useResource(api.protectionRules, []);
  const sims = useResource(api.quotaSimulations, [], { refreshMs: 15_000 });
  const schedules = useResource(api.powerSchedules, []);
  const keepalive = useResource(api.keepaliveSettings, []);
  const monthly = useResource(api.monthlyResetSettings, []);
  const failovers = useResource(api.failoverRules, []);

  const [ruleDrawer, setRuleDrawer] = useState<{ rule?: T.QuotaProtectionRule } | null>(null);
  const [delRule, setDelRule] = useState<T.QuotaProtectionRule>();
  const [schDrawer, setSchDrawer] = useState<{ sch?: T.PowerSchedule } | null>(null);
  const [delSch, setDelSch] = useState<T.PowerSchedule>();
  const [foDrawer, setFoDrawer] = useState<{ rule?: T.FailoverRule } | null>(null);
  const [delFo, setDelFo] = useState<T.FailoverRule>();
  const [simModal, setSimModal] = useState(false);
  const [answering, setAnswering] = useState<string>();

  if (!accounts.length) {
    return (
      <div className="card">
        <EmptyState title="尚未添加阿里云账号" action={<Link className="btn primary" to="/settings">添加云账号</Link>}>
          添加云账号并同步实例后，即可配置额度保护自动停机、定时开关机、跨账号主备容灾与意外停止保活。
        </EmptyState>
      </div>
    );
  }

  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;
  const instName = (id: string) => instances.find((i) => i.id === id)?.name ?? id;
  const pendingConfirms = instances.filter((i) => i.pending_intent_confirmation);

  const answerIntent = async (inst: T.Instance, decision: "keep_stopped" | "resume") => {
    setAnswering(inst.id);
    try {
      const r = await api.confirmInstanceIntent(inst.id, decision);
      if (r.job_id) {
        toast(`已发送开机请求，正在恢复 ${inst.name}…`);
        await waitForJob(r.job_id);
      } else {
        toast(`已将 ${inst.name} 标记为保持停止`);
      }
      reloadAll();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setAnswering(undefined);
    }
  };

  const clearSim = async (s: T.QuotaSimulation) => {
    try {
      await api.clearQuotaSimulation({ account_id: s.account_id, scope: s.scope, source_instance_id: s.source_instance_id || undefined, revision: s.revision });
      sims.reload();
      toast("已清除模拟用量");
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  const toggleRule = async (r: T.QuotaProtectionRule, enabled: boolean) => {
    try {
      const saved = await api.updateProtectionRule(r.id, {
        revision: r.revision,
        name: r.name,
        enabled,
        account_id: r.account_id,
        scope: r.scope,
        source_instance_id: r.source_instance_id,
        threshold_kind: r.threshold_kind,
        threshold: r.threshold,
        action: r.action,
        target_instance_ids: r.target_instance_ids,
        stop_mode: r.stop_mode,
        reserve_seconds: r.reserve_seconds,
        dry_run: r.dry_run,
        allow_unverified_execution: r.allow_unverified_execution ?? false,
        confirmed_self_instance_id: r.confirmed_self_instance_id,
      });
      rules.mutate((l) => l.map((x) => (x.id === saved.id ? saved : x)));
      toast(enabled ? `已启用「${r.name}」` : `已停用「${r.name}」`);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  const toggleSchedule = async (s: T.PowerSchedule, enabled: boolean) => {
    try {
      const saved = await api.updatePowerSchedule(s.id, {
        revision: s.revision,
        name: s.name,
        enabled,
        account_id: s.account_id,
        action: s.action,
        stop_mode: s.stop_mode,
        time_of_day: s.time_of_day,
        days_of_week: s.days_of_week,
        catch_up_minutes: s.catch_up_minutes,
        target_instance_ids: s.target_instance_ids,
        dry_run: s.dry_run,
        confirmed_self_instance_id: s.confirmed_self_instance_id,
      });
      schedules.mutate((l) => l.map((x) => (x.id === saved.id ? saved : x)));
      toast(enabled ? `已启用「${s.name}」` : `已停用「${s.name}」`);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  const st = status.data;
  const selfLabel = !st
    ? "检测中…"
    : !st.self_identity_known
      ? "未确认面板自身实例（自动关机受限）"
      : st.self_instance_id
        ? `自动排除面板自身 (${instName(st.self_instance_id)})`
        : "独立部署（不在受管实例上）";

  return (
    <>
      {pendingConfirms.map((i) => (
        <Banner
          key={i.id}
          level="warning"
          action={
            <div className="inline">
              <button className="btn sm" disabled={answering === i.id} onClick={() => answerIntent(i, "keep_stopped")}>保持停止</button>
              <button className="btn sm primary" disabled={answering === i.id} onClick={() => answerIntent(i, "resume")}>
                {answering === i.id && <Spinner />} 恢复运行
              </button>
            </div>
          }
        >
          <b>{i.name} 在面板外部被关机。</b> 确认前已暂停对其执行意外停止保活。
        </Banner>
      ))}

      {(sims.data ?? []).map((s) => (
        <div className="card compact sim-card" key={`${s.account_id}-${s.scope}-${s.source_instance_id}`}>
          <div className="inline">
            <Badge level="warning">模拟演练中</Badge>
            <b>{accName(s.account_id)} · {s.scope === "swas" ? `轻量 ${instName(s.source_instance_id)}` : SCOPE[s.scope]}</b>
            <span>模拟已用 <b>{gb(s.usage_bytes)}</b> · 持续至 {stamp(s.expires_at)}</span>
            <span className="sub">（仅触发通知与判定日志，不执行真实停机）</span>
            <div className="spacer" />
            <button className="btn sm" onClick={() => clearSim(s)}>结束演练</button>
          </div>
        </div>
      ))}

      <div className="toolbar">
        {st ? (
          <Badge level={st.power_enabled ? "good" : "warning"}>
            {st.power_enabled ? "真实开关机已开启" : "仅试运行模式"}
          </Badge>
        ) : (
          <Skeleton lines={1} />
        )}
        <span className="chip">{selfLabel}</span>
        {st && <span className="chip">熔断护栏：{Math.round(st.automatic_window_seconds / 60)} 分钟内最多自动操作 {st.automatic_limit} 台</span>}
        <div className="spacer" />
        <button className="btn sm" onClick={() => setSimModal(true)}>模拟用量演练</button>
      </div>

      <Card
        title="额度保护自动关机"
        sub="CDT 免费流量池或轻量套餐包接近阈值时，提前停机阻断超额账单"
        right={
          <button className="btn sm primary" onClick={() => setRuleDrawer({})}>
            <PlusIcon size={13} /> 新建保护规则
          </button>
        }
      >
        <Async res={rules}>
          {(list) =>
            !list.length ? (
              <EmptyState title="尚未创建额度保护规则" action={<button className="btn sm primary" onClick={() => setRuleDrawer({})}>创建首条保护规则</button>}>
                建议先开启「试运行」配合「模拟用量演练」验证通知链路，确认无误后再启用真实关机。
              </EmptyState>
            ) : (
              <div>
                {list.map((r) => (
                  <div className="rowline" key={r.id}>
                    <div className="grow">
                      <div className="inline" style={{ gap: 6 }}>
                        <b>{r.name}</b>
                        {r.dry_run && <Badge level="warning">试运行</Badge>}
                        {r.allow_unverified_execution && !r.dry_run && <span className="tag">实时估算触发</span>}
                        <span className="tag">
                          {r.action === "stop_notify" ? (r.stop_mode === "stop_charging" ? "节省停机 + 通知" : "普通停机 + 通知") : "仅告警通知"}
                        </span>
                      </div>
                      <div className="sub" style={{ marginTop: 3 }}>
                        {accName(r.account_id)} · {r.scope === "swas" ? `轻量套餐 (${instName(r.source_instance_id)})` : `${SCOPE[r.scope]}免费额度`} · 阈值{" "}
                        <b>{r.threshold_kind === "percent" ? `${r.threshold}%` : gb(r.threshold)}</b>（提前 {r.reserve_seconds}s 余量）
                        {r.action === "stop_notify" && ` → 关机目标：${r.target_instance_ids.map(instName).join("、")}`}
                      </div>
                    </div>
                    <div className="inline">
                      <button className="btn sm" onClick={() => setRuleDrawer({ rule: r })}>编辑</button>
                      <button className="btn sm danger" onClick={() => setDelRule(r)}>删除</button>
                      <Switch on={r.enabled} label={`启用 ${r.name}`} onChange={(v) => toggleRule(r, v)} />
                    </div>
                  </div>
                ))}
              </div>
            )
          }
        </Async>
      </Card>

      <div className="grid g2">
        <Card
          title="定时开关机计划"
          sub="按面板时区定期执行启停（夜间节省停机 / 白天自动拉起）"
          right={
            <button className="btn sm primary" onClick={() => setSchDrawer({})}>
              <PlusIcon size={13} /> 新建计划
            </button>
          }
        >
          <Async res={schedules}>
            {(list) =>
              !list.length ? (
                <EmptyState title="暂无定时开关机计划">按量 ECS 配置夜间节省停机可显著降低计算与带宽固定成本。</EmptyState>
              ) : (
                <div>
                  {list.map((s) => (
                    <div className="rowline" key={s.id}>
                      <div className="grow">
                        <div className="inline" style={{ gap: 6 }}>
                          <b>{s.name}</b>
                          <span className="mono tag">{s.time_of_day}</span>
                          <span className="tag">{s.action === "start" ? "开机" : s.stop_mode === "stop_charging" ? "节省停机" : "普通停机"}</span>
                          {s.dry_run && <Badge level="warning">试运行</Badge>}
                        </div>
                        <div className="sub" style={{ marginTop: 3 }}>
                          {s.days_of_week.length === 7 ? "每天" : WEEKDAYS.filter(([d]) => s.days_of_week.includes(d)).map(([, n]) => n).join("、")} · {s.target_instance_ids.map(instName).join("、")}
                        </div>
                      </div>
                      <div className="inline">
                        <button className="btn sm" onClick={() => setSchDrawer({ sch: s })}>编辑</button>
                        <button className="btn sm danger" onClick={() => setDelSch(s)}>删除</button>
                        <Switch on={s.enabled} label={`启用 ${s.name}`} onChange={(v) => toggleSchedule(s, v)} />
                      </div>
                    </div>
                  ))}
                </div>
              )
            }
          </Async>
        </Card>

        <Card
          title="主备容灾切换（支持跨账号）"
          sub="主实例触发流量保护关机时，立即启动备实例无缝接管"
          right={
            <button className="btn sm primary" disabled={instances.length < 2} onClick={() => setFoDrawer({})}>
              <PlusIcon size={13} /> 新建容灾
            </button>
          }
        >
          <Async res={failovers}>
            {(list) =>
              !list.length ? (
                <EmptyState title="暂无跨账号主备规则">
                  {instances.length < 2 ? "至少需要 2 台受管实例才可配置主备接管。" : "主节点免费流量耗尽停机后，可自动拉起另一账号下的备用机器。"}
                </EmptyState>
              ) : (
                <div>
                  {list.map((f) => {
                    const pInst = instances.find((x) => x.id === f.primary_instance_id);
                    const sInst = instances.find((x) => x.id === f.standby_instance_id);
                    return (
                      <div className="rowline" key={f.id}>
                        <div className="grow">
                          <div className="inline" style={{ gap: 6 }}>
                            <b>{f.name}</b>
                            {f.dry_run && <Badge level="warning">试运行</Badge>}
                          </div>
                          <div className="sub" style={{ marginTop: 3 }}>
                            {pInst ? `${pInst.name} (${accName(pInst.account_id)})` : f.primary_instance_id} →{" "}
                            <b>{sInst ? `${sInst.name} (${accName(sInst.account_id)})` : f.standby_instance_id}</b>
                          </div>
                        </div>
                        <div className="inline">
                          <button className="btn sm" onClick={() => setFoDrawer({ rule: f })}>编辑</button>
                          <button className="btn sm danger" onClick={() => setDelFo(f)}>删除</button>
                          <Switch
                            on={f.enabled}
                            label={`启用 ${f.name}`}
                            onChange={async (enabled) => {
                              try {
                                const saved = await api.updateFailoverRule(f.id, {
                                  revision: f.revision,
                                  name: f.name,
                                  enabled,
                                  dry_run: f.dry_run,
                                  primary_instance_id: f.primary_instance_id,
                                  standby_instance_id: f.standby_instance_id,
                                });
                                failovers.mutate((l) => l.map((x) => (x.id === saved.id ? saved : x)));
                                toast(enabled ? `已启用「${f.name}」` : `已停用「${f.name}」`);
                              } catch (e) {
                                toast(errMsg(e), true);
                              }
                            }}
                          />
                        </div>
                      </div>
                    );
                  })}
                </div>
              )
            }
          </Async>
        </Card>
      </div>

      <div className="grid g2">
        <KeepaliveCard res={keepalive} instances={instances} accName={accName} />
        <MonthlyResetCard res={monthly} instances={instances} accName={accName} />
      </div>

      {ruleDrawer && (
        <RuleDrawer
          initial={ruleDrawer.rule}
          accounts={accounts}
          instances={instances}
          selfId={st?.self_instance_id ?? null}
          onClose={() => setRuleDrawer(null)}
          onSaved={() => {
            setRuleDrawer(null);
            rules.reload();
          }}
        />
      )}
      {delRule && (
        <Confirm
          title={`删除保护规则「${delRule.name}」？`}
          okText="确认删除"
          danger
          body="删除后不再按此规则监控流量或触发自动停机。"
          onClose={() => setDelRule(undefined)}
          onOk={async () => {
            await api.deleteProtectionRule(delRule.id, delRule.revision);
            rules.reload();
            toast("规则已删除");
          }}
        />
      )}
      {schDrawer && (
        <ScheduleDrawer
          initial={schDrawer.sch}
          accounts={accounts}
          instances={instances}
          selfId={st?.self_instance_id ?? null}
          onClose={() => setSchDrawer(null)}
          onSaved={() => {
            setSchDrawer(null);
            schedules.reload();
          }}
        />
      )}
      {delSch && (
        <Confirm
          title={`删除定时计划「${delSch.name}」？`}
          okText="确认删除"
          danger
          body="删除后将停止该时间点的自动启停调度。"
          onClose={() => setDelSch(undefined)}
          onOk={async () => {
            await api.deletePowerSchedule(delSch.id, delSch.revision);
            schedules.reload();
            toast("定时计划已删除");
          }}
        />
      )}
      {foDrawer && (
        <FailoverDrawer
          initial={foDrawer.rule}
          accounts={accounts}
          instances={instances}
          onClose={() => setFoDrawer(null)}
          onSaved={() => {
            setFoDrawer(null);
            failovers.reload();
          }}
        />
      )}
      {delFo && (
        <Confirm
          title={`删除主备规则「${delFo.name}」？`}
          okText="确认删除"
          danger
          body="删除后主实例额度停机时不再自动拉起备机。"
          onClose={() => setDelFo(undefined)}
          onOk={async () => {
            await api.deleteFailoverRule(delFo.id, delFo.revision);
            failovers.reload();
            toast("主备规则已删除");
          }}
        />
      )}
      {simModal && (
        <SimulationModal
          accounts={accounts}
          instances={instances}
          existing={sims.data ?? []}
          onClose={() => setSimModal(false)}
          onSaved={() => {
            setSimModal(false);
            sims.reload();
          }}
        />
      )}
    </>
  );
}

/* ============================================================================
 * Sub-Workspace 2: 通知渠道与简报 (Compact Summary Cards + Slide-over Drawers!)
 * ========================================================================== */
type ChannelKey = "telegram" | "email" | "webhook" | "daily" | "tg_ctrl" | "quiet";

function ChannelsSubWorkspace() {
  const toast = useToast();
  const tg = useResource(api.telegram, []);
  const email = useResource(api.emailChannel, []);
  const webhook = useResource(api.webhookChannel, []);
  const daily = useResource(api.dailyReport, []);
  const ctrl = useResource(api.telegramControl, []);
  const quiet = useResource(api.quietHours, []);
  const [openKey, setOpenKey] = useState<ChannelKey | null>(null);
  const [busyAction, setBusyAction] = useState<string | null>(null);

  const quickTestEmail = async () => {
    setBusyAction("email");
    try {
      const { job_id } = await api.testEmailChannel();
      const job = await waitForJob(job_id);
      email.reload();
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("测试邮件已发送");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusyAction(null);
    }
  };

  const quickTestWebhook = async () => {
    setBusyAction("webhook");
    try {
      const { job_id } = await api.testWebhookChannel();
      const job = await waitForJob(job_id);
      webhook.reload();
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("Webhook 测试推送已送达");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusyAction(null);
    }
  };

  const quickTestTg = async () => {
    setBusyAction("tg");
    try {
      const { job_id } = await api.testTelegram();
      const job = await waitForJob(job_id);
      tg.reload();
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("Telegram 测试消息已发出");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusyAction(null);
    }
  };

  const quickSendDaily = async () => {
    setBusyAction("daily");
    try {
      await api.sendDailyReportNow();
      toast("已生成今日流量简报并加入投递队列");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusyAction(null);
    }
  };

  const tgData = tg.data;
  const emData = email.data;
  const whData = webhook.data;
  const dyData = daily.data;
  const ctData = ctrl.data;
  const qhData = quiet.data;
  const hasActiveMaint = Boolean(qhData?.maintenance && Date.parse(qhData.maintenance.end_at) > Date.now());

  return (
    <>
      <div className="grid g3">
        {/* 1. Telegram */}
        <div className="channel-card">
          <div className="channel-card-top">
            <div className="channel-icon"><BellIcon size={17} /></div>
            <div className="channel-meta">
              <b>Telegram 通知机器人</b>
              <span className="sub">即时推送告警与额度保护事件</span>
            </div>
            {tgData && <Badge level={CH_STATUS[tgData.status][0]}>{CH_STATUS[tgData.status][1]}</Badge>}
          </div>
          <div className="sub">
            {tgData?.chat_id ? (
              <>
                目标会话 <span className="mono">{tgData.chat_id}</span>
                {tgData.proxy_mode !== "none" ? ` · 代理 ${tgData.proxy_mode}` : ""}
                {tgData.last_success_at && <div>最近送达：{stamp(tgData.last_success_at)}</div>}
                {tgData.status === "failing" && tgData.last_error && (
                  <div className="field-error">失败原因：{errorText(tgData.last_error.code)}</div>
                )}
              </>
            ) : (
              "尚未配置 Bot Token 与 Chat ID"
            )}
          </div>
          <div className="channel-foot">
            {tgData?.bot_token_set ? (
              <button className="btn sm ghost" disabled={busyAction !== null} onClick={quickTestTg}>
                {busyAction === "tg" && <Spinner />} 发送测试
              </button>
            ) : (
              <span />
            )}
            <button className="btn sm" onClick={() => setOpenKey("telegram")}>配置</button>
          </div>
        </div>

        {/* 2. Email SMTP */}
        <div className="channel-card">
          <div className="channel-card-top">
            <div className="channel-icon"><GlobeIcon size={17} /></div>
            <div className="channel-meta">
              <b>邮件 (SMTP) 渠道</b>
              <span className="sub">STARTTLS / SSL 加密邮件告警</span>
            </div>
            {emData && <Badge level={emData.enabled ? CH_STATUS[emData.status][0] : "neutral"}>{emData.enabled ? CH_STATUS[emData.status][1] : "未启用"}</Badge>}
          </div>
          <div className="sub">
            {emData?.host ? (
              <>
                <span className="mono">{emData.host}:{emData.port}</span> · {emData.to_addresses.length} 位收件人
                {emData.last_success_at && <div>最近送达：{stamp(emData.last_success_at)}</div>}
                {emData.status === "failing" && emData.last_error && (
                  <div className="field-error">失败原因：{errorText(emData.last_error.code)}</div>
                )}
              </>
            ) : (
              "尚未配置 SMTP 发件服务器"
            )}
          </div>
          <div className="channel-foot">
            {emData && emData.status !== "not_configured" ? (
              <button className="btn sm ghost" disabled={busyAction !== null} onClick={quickTestEmail}>
                {busyAction === "email" && <Spinner />} 发送测试
              </button>
            ) : (
              <span />
            )}
            <button className="btn sm" onClick={() => setOpenKey("email")}>配置</button>
          </div>
        </div>

        {/* 3. Webhook */}
        <div className="channel-card">
          <div className="channel-card-top">
            <div className="channel-icon"><ZapIcon size={17} /></div>
            <div className="channel-meta">
              <b>Webhook 推送</b>
              <span className="sub">JSON POST + HMAC-SHA256 签名</span>
            </div>
            {whData && <Badge level={whData.enabled ? CH_STATUS[whData.status][0] : "neutral"}>{whData.enabled ? CH_STATUS[whData.status][1] : "未启用"}</Badge>}
          </div>
          <div className="sub">
            {whData?.url ? (
              <>
                <span className="mono" style={{ wordBreak: "break-all" }}>{whData.url}</span>
                {whData.last_success_at && <div>最近推送：{stamp(whData.last_success_at)}</div>}
                {whData.status === "failing" && whData.last_error && (
                  <div className="field-error">失败原因：{errorText(whData.last_error.code)}</div>
                )}
              </>
            ) : (
              "尚未配置飞书 / 钉钉 / 自建 Webhook URL"
            )}
          </div>
          <div className="channel-foot">
            {whData && whData.status !== "not_configured" ? (
              <button className="btn sm ghost" disabled={busyAction !== null} onClick={quickTestWebhook}>
                {busyAction === "webhook" && <Spinner />} 发送测试
              </button>
            ) : (
              <span />
            )}
            <button className="btn sm" onClick={() => setOpenKey("webhook")}>配置</button>
          </div>
        </div>

        {/* 4. Daily Report */}
        <div className="channel-card">
          <div className="channel-card-top">
            <div className="channel-icon"><BellIcon size={17} /></div>
            <div className="channel-meta">
              <b>每日流量简报</b>
              <span className="sub">定时汇总多账号流量池与剩余额度</span>
            </div>
            {dyData && <Badge level={dyData.enabled ? "good" : "neutral"}>{dyData.enabled ? `每天 ${dyData.time_of_day}` : "未开启"}</Badge>}
          </div>
          <div className="sub">
            {dyData?.enabled ? `每天 ${dyData.time_of_day} 自动向所有已启用渠道投递汇总报告` : "开启后每天定时推送各账号流量消耗简报"}
            {dyData?.last_sent_date && <div>最近自动发送：{dyData.last_sent_date}</div>}
          </div>
          <div className="channel-foot">
            <button className="btn sm ghost" disabled={busyAction !== null} onClick={quickSendDaily}>
              {busyAction === "daily" && <Spinner />} 立即发送一份
            </button>
            <button className="btn sm" onClick={() => setOpenKey("daily")}>配置</button>
          </div>
        </div>

        {/* 5. Telegram Remote Control */}
        <div className="channel-card">
          <div className="channel-card-top">
            <div className="channel-icon"><ShieldIcon size={17} /></div>
            <div className="channel-meta">
              <b>Telegram 远程控制</b>
              <span className="sub">白名单交互式查状态、开关机与换 IP</span>
            </div>
            {ctData && <Badge level={ctData.enabled ? "good" : "neutral"}>{ctData.enabled ? "监听中" : "未开启"}</Badge>}
          </div>
          <div className="sub">
            {ctData?.enabled
              ? `授权白名单：${ctData.allowed_user_ids.length} 位用户 · 支持 /status /quota /start /stop /changeip`
              : "仅允许数字 ID 白名单内的管理员执行受控运维指令"}
          </div>
          <div className="channel-foot">
            <span />
            <button className="btn sm" onClick={() => setOpenKey("tg_ctrl")}>配置</button>
          </div>
        </div>

        {/* 6. Quiet Hours & Maintenance Window */}
        <div className="channel-card">
          <div className="channel-card-top">
            <div className="channel-icon"><ShieldIcon size={17} /></div>
            <div className="channel-meta">
              <b>免打扰静默与维护窗</b>
              <span className="sub">夜间抑制普通提醒，紧急告警不受限</span>
            </div>
            {qhData && (
              <Badge level={hasActiveMaint ? "warning" : qhData.active ? "good" : qhData.enabled ? "neutral" : "neutral"}>
                {hasActiveMaint ? "维护静默中" : qhData.active ? "当前静默中" : qhData.enabled ? `${qhData.start}–${qhData.end}` : "未开启"}
              </Badge>
            )}
          </div>
          <div className="sub">
            {hasActiveMaint
              ? `临时维护静默持续至 ${stamp(qhData!.maintenance!.end_at)}`
              : qhData?.enabled
                ? `每日 ${qhData.start} 至 ${qhData.end} 非紧急通知仅存档不推送`
                : "可配置夜间免打扰时段或开启 30m–3h 临时维护期"}
          </div>
          <div className="channel-foot">
            <span />
            <button className="btn sm" onClick={() => setOpenKey("quiet")}>配置</button>
          </div>
        </div>
      </div>

      {openKey === "telegram" && tgData && (
        <Drawer title="配置 Telegram 通知机器人" sub="Bot Token 采用服务端主密钥加密存储" onClose={() => setOpenKey(null)}>
          {tgData.status === "failing" && tgData.last_error && (
            <Banner level="critical">最近一次发送失败：{errorText(tgData.last_error.code)}</Banner>
          )}
          <TelegramForm value={tgData} onSaved={(nv) => tg.mutate(() => nv)} />
        </Drawer>
      )}

      {openKey === "email" && emData && (
        <Drawer title="配置邮件 (SMTP) 通知渠道" sub="支持 STARTTLS (587) 与隐式 TLS (465)" onClose={() => setOpenKey(null)}>
          <EmailChannelForm value={emData} onSaved={(nv) => email.mutate(() => nv)} onReload={email.reload} />
        </Drawer>
      )}

      {openKey === "webhook" && whData && (
        <Drawer title="配置 Webhook 通知渠道" sub="JSON POST 推送，支持 HMAC-SHA256 签名与自定义鉴权头" onClose={() => setOpenKey(null)}>
          <WebhookChannelForm value={whData} onSaved={(nv) => webhook.mutate(() => nv)} onReload={webhook.reload} />
        </Drawer>
      )}

      {openKey === "daily" && dyData && (
        <Drawer title="配置每日流量简报" sub="按面板业务时区每天定时向所有已启用渠道投递汇总" onClose={() => setOpenKey(null)}>
          <DailyReportForm value={dyData} onSaved={(nv) => daily.mutate(() => nv)} />
        </Drawer>
      )}

      {openKey === "tg_ctrl" && ctData && (
        <Drawer title="配置 Telegram 远程控制" sub="复用 Telegram 通知机器人的连接与代理配置" onClose={() => setOpenKey(null)}>
          <TelegramControlForm value={ctData} onSaved={(nv) => ctrl.mutate(() => nv)} />
        </Drawer>
      )}

      {openKey === "quiet" && qhData && (
        <Drawer title="静默时段与临时维护期" sub="严重告警（额度保护、掉线、抢占回收）始终穿透静默即时送达" onClose={() => setOpenKey(null)}>
          <QuietHoursForm value={qhData} onSaved={(nv) => quiet.mutate(() => nv)} />
        </Drawer>
      )}
    </>
  );
}

/* ============================================================================
 * Sub-Workspace 3: 告警触发规则 (Categorized Cards!)
 * ========================================================================== */
const RULE_GROUPS: { title: string; sub: string; keys: T.AlertRuleKey[] }[] = [
  {
    title: "流量配额与套餐监控",
    sub: "CDT 免费流量档位、月底超额预测、轻量流量包及估算核对",
    keys: ["cdt_usage", "cdt_forecast_exceed", "swas_traffic_usage", "quota_protection", "cdt_estimation_deviation", "cdt_official_unavailable"],
  },
  {
    title: "账户余额、续费与闲置资源",
    sub: "现金余额不足、包年包月到期提醒与闲置计费资源告警",
    keys: ["balance_low", "instance_expiring", "idle_resource_detected"],
  },
  {
    title: "网络延迟、丢包与线路变更",
    sub: "周期探测超限、节点不可达与每日 AS 路由路径变化",
    keys: ["network_latency", "network_loss", "network_unreachable", "network_route_change"],
  },
  {
    title: "实例健康与云端维护事件",
    sub: "hm-agent 掉线、API 异常、IP 变动、突发积分不足与阿里云系统事件",
    keys: ["agent_offline", "account_api_failure", "instance_public_ip_changed", "spec_bandwidth_insufficient", "cpu_credit_low", "system_event"],
  },
];

function AlertRulesSubWorkspace() {
  const rulesRes = useResource(() => api.alertRules(true), []);
  const plansRes = useResource(api.networkPlans, []);

  return (
    <Async res={rulesRes} lines={8}>
      {(list) => {
        if (!list.length) return <div className="card"><EmptyState title="暂无可用告警规则" /></div>;
        const knownKeys = new Set(RULE_GROUPS.flatMap((g) => g.keys));
        const extra = list.filter((r) => !knownKeys.has(r.key));
        return (
          <div className="grid g2">
            {RULE_GROUPS.map((grp, idx) => {
              const items = [
                ...grp.keys.map((k) => list.find((r) => r.key === k)).filter((r): r is T.AlertRule => !!r),
                ...(idx === RULE_GROUPS.length - 1 ? extra : []),
              ];
              if (!items.length) return null;
              return (
                <Card key={grp.title} title={grp.title} sub={grp.sub}>
                  <div>
                    {items.map((r) => (
                      <RuleRow
                        key={r.key}
                        rule={r}
                        plans={plansRes.data ?? []}
                        onChange={(nr) => rulesRes.mutate((l) => l.map((x) => (x.key === nr.key ? nr : x)))}
                      />
                    ))}
                  </div>
                </Card>
              );
            })}
          </div>
        );
      }}
    </Async>
  );
}

function RuleRow({ rule, plans, onChange }: { rule: T.AlertRule; plans: T.ProbePlan[]; onChange: (r: T.AlertRule) => void }) {
  const toast = useToast();
  const meta = ALERT_RULE[rule.key] ?? { title: rule.key, desc: "" };
  const p = rule.params;
  const editableKind =
    p.thresholds_percent !== undefined
      ? "thresholds"
      : p.threshold_percent !== undefined || rule.key === "cpu_credit_low"
        ? "single_percent"
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
      : editableKind === "single_percent"
        ? String(p.threshold_percent ?? 20)
        : editableKind === "deviation" || editableKind === "failures"
          ? String(p.consecutive_failures ?? 3)
          : editableKind === "offline"
            ? String(p.offline_after_seconds ?? 30)
            : editableKind === "days"
              ? String(p.before_days ?? 7)
              : null;

  const [text, setText] = useState(initial ?? "");
  const [probePlanId, setProbePlanId] = useState(plans[0]?.id ?? "");
  const [probeVal, setProbeVal] = useState(rule.key === "network_latency" ? "200" : "10");
  const [busy, setBusy] = useState(false);

  const label =
    editableKind === "thresholds"
      ? ["达到", "% 时"]
      : editableKind === "single_percent"
        ? ["低于基准", "% 时"]
        : editableKind === "deviation" || editableKind === "failures"
          ? ["连续", "次后"]
          : editableKind === "days"
            ? ["到期前", "天"]
            : ["失联超过", "秒后"];

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
    if (!nums.length || nums.some((n) => !(n > 0)) || ((editableKind === "thresholds" || editableKind === "single_percent") && nums.some((n) => n > 100))) {
      return toast("请填写大于 0 的有效数值（百分比不超过 100）", true);
    }
    const params =
      editableKind === "thresholds"
        ? { thresholds_percent: [...nums].sort((a, b) => a - b) }
        : editableKind === "single_percent"
          ? { threshold_percent: Math.round(nums[0]) }
          : editableKind === "deviation"
            ? { consecutive_failures: Math.round(nums[0]), recovery_samples: p.recovery_samples ?? 3 }
            : editableKind === "failures"
              ? { consecutive_failures: Math.round(nums[0]) }
              : editableKind === "days"
                ? { before_days: Math.round(nums[0]) }
                : { offline_after_seconds: Math.round(nums[0]) };
    patch({ params }, "阈值已保存");
  };

  const addProbeThreshold = () => {
    const planId = probePlanId || plans[0]?.id;
    const v = Number(probeVal);
    if (!planId || !(v > 0)) return toast("请选择测速组合并输入大于 0 的阈值", true);
    const others = (p.probe_thresholds ?? []).filter((x) => x.plan_id !== planId);
    const entry =
      rule.key === "network_latency"
        ? { plan_id: planId, enabled: true, latency_ms: Math.round(v) }
        : { plan_id: planId, enabled: true, loss_percent: Math.min(100, Math.round(v)) };
    const nextParams =
      rule.key === "network_latency"
        ? { probe_thresholds: [...others, entry], duration_seconds: p.duration_seconds ?? 600, recovery_samples: p.recovery_samples ?? 3 }
        : { probe_thresholds: [...others, entry], recovery_samples: p.recovery_samples ?? 3 };
    patch({ params: nextParams }, "已保存组合告警阈值");
  };

  return (
    <div className="rowline" style={{ alignItems: "flex-start" }}>
      <div className="grow">
        <div className="inline" style={{ gap: 6 }}>
          <b>{meta.title}</b>
          {rule.urgent && <span className="tag" title="紧急：不受免打扰时段限制">紧急</span>}
        </div>
        <div className="sub">{meta.desc}</div>

        {rule.key === "balance_low" && (
          <div style={{ marginTop: 4 }}>
            <Link className="linkbtn sub" to="/traffic?tab=billing">前往账户账单页按账号设定提醒金额 →</Link>
          </div>
        )}

        {initial !== null && (
          <div className="inline sub" style={{ marginTop: 6 }}>
            <label htmlFor={`rule-${rule.key}`}>{label[0]}</label>
            <input
              id={`rule-${rule.key}`}
              type="text"
              value={text}
              onChange={(e) => setText(e.target.value)}
              style={{ width: editableKind === "thresholds" ? 110 : 64, padding: "3px 8px" }}
              disabled={!rule.enabled}
              inputMode="numeric"
            />
            <span>{label[1]}告警</span>
            {text !== initial && (
              <button className="btn sm primary" onClick={saveParam} disabled={busy}>保存</button>
            )}
          </div>
        )}

        {(rule.key === "network_latency" || rule.key === "network_loss") && plans.length > 0 && (
          <div className="stack" style={{ marginTop: 6, gap: 4 }}>
            {(p.probe_thresholds ?? []).length > 0 && (
              <div className="sub" style={{ fontSize: "var(--fs-xs)" }}>
                已设：
                {(p.probe_thresholds ?? [])
                  .map((pt) => {
                    const nm = plans.find((x) => x.id === pt.plan_id)?.name ?? pt.plan_id;
                    return rule.key === "network_latency" ? `${nm} > ${pt.latency_ms}ms` : `${nm} > ${pt.loss_percent ?? 5}%`;
                  })
                  .join(" · ")}
              </div>
            )}
            <div className="inline sub">
              <select value={probePlanId || plans[0].id} onChange={(e) => setProbePlanId(e.target.value)} style={{ padding: "3px 8px" }} disabled={!rule.enabled}>
                {plans.map((pl) => (
                  <option key={pl.id} value={pl.id}>{pl.name}</option>
                ))}
              </select>
              <span>&gt;</span>
              <input type="number" min={1} value={probeVal} onChange={(e) => setProbeVal(e.target.value)} style={{ width: 68, padding: "3px 8px" }} disabled={!rule.enabled} />
              <span>{rule.key === "network_latency" ? "ms" : "%"}</span>
              <button type="button" className="btn sm" disabled={busy || !rule.enabled} onClick={addProbeThreshold}>设定</button>
            </div>
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
            `「${meta.title}」已${v ? "开启" : "关闭"}`,
          )
        }
      />
    </div>
  );
}

/* ============================================================================
 * Sub-Workspace 4: 通知与判定记录 (Unified History)
 * ========================================================================== */
function HistorySubWorkspace({ accounts, instances }: { accounts: T.Account[]; instances: T.Instance[] }) {
  const [kind, setKind] = useState<"notifications" | "decisions">("notifications");
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;
  const instName = (id: string) => instances.find((i) => i.id === id)?.name ?? id;

  return (
    <>
      <div className="toolbar">
        <SegToggle
          label="记录类别"
          value={kind}
          onChange={setKind}
          options={[
            ["notifications", "通知投递历史"],
            ["decisions", "额度保护判定记录"],
          ]}
        />
        <div className="spacer" />
        <Link className="btn sm ghost" to="/settings?tab=activity">后台任务与操作审计 →</Link>
      </div>

      {kind === "notifications" ? <NotificationsHistoryCard /> : <DecisionsHistoryCard accName={accName} instName={instName} />}
    </>
  );
}

function NotificationsHistoryCard() {
  const [items, setItems] = useState<T.Notification[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [more, setMore] = useState(false);
  const first = useResource(async () => {
    const r = await api.notifications({ limit: 25 });
    setItems(r.items);
    setCursor(r.next_cursor);
    return true;
  }, []);
  useEvent("notification.created", (n) => setItems((l) => (l.some((x) => x.id === n.id) ? l : [n, ...l])));

  const loadMore = async () => {
    if (!cursor) return;
    setMore(true);
    try {
      const r = await api.notifications({ limit: 25, cursor });
      setItems((l) => [...l, ...r.items]);
      setCursor(r.next_cursor);
    } finally {
      setMore(false);
    }
  };

  return (
    <Card title="通知投递记录" sub="记录所有告警事件及各渠道投递状态">
      <Async res={first} lines={5}>
        {() =>
          !items.length ? (
            <EmptyState title="暂无通知记录">告警触发或发送测试消息后，投递详情将记录在此。</EmptyState>
          ) : (
            <>
              <div className="alist">
                {items.map((n) => {
                  const [dl, dt] = DELIVERY[n.delivery_status];
                  return (
                    <div key={n.id} style={{ borderBottom: "1px solid var(--border)" }}>
                      <NotificationItem n={n} />
                      {n.delivery_status !== "sent" && (
                        <div style={{ margin: "-4px 0 10px 2px" }}>
                          <Badge level={dl}>{dt}</Badge>
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
              {cursor && (
                <div style={{ textAlign: "center", marginTop: 14 }}>
                  <button className="btn sm" onClick={loadMore} disabled={more}>{more && <Spinner />} 加载更早通知</button>
                </div>
              )}
            </>
          )
        }
      </Async>
    </Card>
  );
}

function DecisionsHistoryCard({ accName, instName }: { accName: (id: string) => string; instName: (id: string) => string }) {
  const rules = useResource(api.protectionRules, []);
  const [ruleId, setRuleId] = useState("");
  const res = useResource(() => api.quotaDecisions({ rule_id: ruleId || undefined, limit: 40 }), [ruleId]);
  const ruleName = (id: string) => rules.data?.find((r) => r.id === id)?.name ?? id;

  return (
    <Card
      title="额度保护判定记录"
      sub="每次规则状态跃迁、试运行命中或执行停机的详细评估依据"
      right={
        (rules.data ?? []).length > 0 ? (
          <select value={ruleId} onChange={(e) => setRuleId(e.target.value)} aria-label="按规则筛选">
            <option value="">全部保护规则</option>
            {(rules.data ?? []).map((r) => (
              <option key={r.id} value={r.id}>{r.name}</option>
            ))}
          </select>
        ) : undefined
      }
    >
      <Async res={res} lines={5}>
        {(p) =>
          !p.items.length ? (
            <EmptyState title="暂无保护判定记录" />
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>评估时间</th>
                    <th>命中规则</th>
                    <th>判定状态</th>
                    <th className="num">评估用量 / 阈值</th>
                    <th>执行结果</th>
                  </tr>
                </thead>
                <tbody>
                  {p.items.map((d) => {
                    const [lv, lb] = DECISION_STATE[d.state];
                    return (
                      <tr key={d.id}>
                        <td>{stamp(d.evaluated_at)}</td>
                        <td>
                          <b>{ruleName(d.rule_id)}</b>
                          <div className="sub">{accName(d.account_id)} · {d.scope === "swas" ? "轻量套餐" : (SCOPE[d.scope as T.QuotaScope] ?? d.scope)}</div>
                        </td>
                        <td>
                          <Badge level={lv}>{lb}</Badge>
                          {d.simulation && <span className="tag" style={{ marginLeft: 4 }}>模拟</span>}
                        </td>
                        <td className="num">
                          {d.evaluated_bytes === null ? "—" : gb(d.evaluated_bytes)} / {d.threshold_bytes === null ? "—" : gb(d.threshold_bytes)}
                        </td>
                        <td className="wrap">
                          {d.targets.length
                            ? d.targets.map((t) => `${instName(t.instance_id)}：${DECISION_OUTCOME[t.outcome] ?? t.outcome}`).join("；")
                            : <span className="sub">{d.reasons.join("、") || "—"}</span>}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
    </Card>
  );
}

/* ============================================================================
 * Channel Configuration Forms (Inside Slide-over Drawers)
 * ========================================================================== */
function EmailChannelForm({ value, onSaved, onReload }: { value: T.EmailChannel; onSaved: (v: T.EmailChannel) => void; onReload: () => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [host, setHost] = useState(value.host || "");
  const [port, setPort] = useState(String(value.port || 587));
  const [tlsMode, setTlsMode] = useState<"starttls" | "implicit_tls" | "none">(value.tls_mode || "starttls");
  const [username, setUsername] = useState(value.username || "");
  const [password, setPassword] = useState("");
  const [fromAddr, setFromAddr] = useState(value.from_address || "");
  const [toAddrs, setToAddrs] = useState((value.to_addresses ?? []).join(", "));
  const [prefix, setPrefix] = useState(value.subject_prefix || "[Harbormaster]");
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const list = toAddrs.split(/[,，\s]+/).map((x) => x.trim()).filter(Boolean);
    if (!host.trim() || !fromAddr.trim() || !list.length) {
      return toast("请填写 SMTP 服务器、发件人邮箱和至少一个收件人邮箱", true);
    }
    setBusy(true);
    try {
      const payload: T.EmailChannelUpdate = {
        enabled,
        host: host.trim(),
        port: Number(port) || 587,
        tls_mode: tlsMode,
        username: username.trim(),
        from_address: fromAddr.trim(),
        to_addresses: list,
        subject_prefix: prefix.trim(),
      };
      if (password !== "") payload.password = password;
      const saved = await api.updateEmailChannel(payload);
      setPassword("");
      onSaved(saved);
      toast("邮件通知渠道已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    try {
      const { job_id } = await api.testEmailChannel();
      const job = await waitForJob(job_id);
      onReload();
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("测试邮件已发送");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setTesting(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      {value.status === "failing" && value.last_error && <Banner level="critical">最近发送失败：{errorText(value.last_error.code)}</Banner>}
      <div className="inline">
        <Switch on={enabled} label="启用邮件通知" onChange={setEnabled} />
        <span>启用 SMTP 邮件告警投递</span>
      </div>
      <div className="grid g3">
        <div>
          <label className="lbl">SMTP 主机</label>
          <input type="text" value={host} onChange={(e) => setHost(e.target.value)} placeholder="smtp.example.com" />
        </div>
        <div>
          <label className="lbl">端口</label>
          <input type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} />
        </div>
        <div>
          <label className="lbl">加密模式</label>
          <select value={tlsMode} onChange={(e) => setTlsMode(e.target.value as "starttls" | "implicit_tls" | "none")}>
            <option value="starttls">STARTTLS (587)</option>
            <option value="implicit_tls">SSL / TLS (465)</option>
            <option value="none">不加密</option>
          </select>
        </div>
      </div>
      <div className="grid g2">
        <div>
          <label className="lbl">登录账号（选填）</label>
          <input type="text" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        </div>
        <div>
          <label className="lbl">密码 / 授权码</label>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={value.password_set ? "已加密保存，留空不变" : "请输入密码"} autoComplete="new-password" />
        </div>
      </div>
      <div className="grid g2">
        <div>
          <label className="lbl">发件人邮箱</label>
          <input type="email" value={fromAddr} onChange={(e) => setFromAddr(e.target.value)} placeholder="alert@example.com" />
        </div>
        <div>
          <label className="lbl">标题前缀</label>
          <input type="text" value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="[Harbormaster]" />
        </div>
      </div>
      <div>
        <label className="lbl">收件人邮箱（逗号分隔）</label>
        <input type="text" value={toAddrs} onChange={(e) => setToAddrs(e.target.value)} placeholder="you@example.com" />
      </div>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        {value.status !== "not_configured" && (
          <button type="button" className="btn" disabled={testing || busy} onClick={runTest}>
            {testing && <Spinner />} 发送测试邮件
          </button>
        )}
        <button className="btn primary" disabled={busy || testing}>{busy && <Spinner />} 保存设置</button>
      </div>
    </form>
  );
}

function WebhookChannelForm({ value, onSaved, onReload }: { value: T.WebhookChannel; onSaved: (v: T.WebhookChannel) => void; onReload: () => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [url, setUrl] = useState(value.url || "");
  const [secret, setSecret] = useState("");
  const [headerName, setHeaderName] = useState(value.header_name || "");
  const [headerVal, setHeaderVal] = useState("");
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!url.trim()) return toast("请填写 Webhook URL", true);
    setBusy(true);
    try {
      const payload: T.WebhookChannelUpdate = {
        enabled,
        url: url.trim(),
        header_name: headerName.trim(),
      };
      if (secret !== "") payload.secret = secret;
      if (headerVal !== "") payload.header_value = headerVal;
      const saved = await api.updateWebhookChannel(payload);
      setSecret("");
      setHeaderVal("");
      onSaved(saved);
      toast("Webhook 渠道已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    try {
      const { job_id } = await api.testWebhookChannel();
      const job = await waitForJob(job_id);
      onReload();
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("Webhook 测试推送已送达");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setTesting(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      {value.status === "failing" && value.last_error && <Banner level="critical">最近推送失败：{errorText(value.last_error.code)}</Banner>}
      <div className="inline">
        <Switch on={enabled} label="启用 Webhook 推送" onChange={setEnabled} />
        <span>启用 Webhook JSON 推送</span>
      </div>
      <div>
        <label className="lbl">Webhook 接收 URL</label>
        <input type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://hooks.example.com/harbormaster" />
      </div>
      <div>
        <label className="lbl">HMAC-SHA256 签名密钥（选填）</label>
        <input type="password" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={value.secret_set ? "已加密保存，留空不变" : "生成 X-Harbormaster-Signature-256"} autoComplete="new-password" />
      </div>
      <div className="grid g2">
        <div>
          <label className="lbl">自定义请求头名（选填）</label>
          <input type="text" value={headerName} onChange={(e) => setHeaderName(e.target.value)} placeholder="如 X-Api-Token" />
        </div>
        <div>
          <label className="lbl">自定义请求头值</label>
          <input type="password" value={headerVal} onChange={(e) => setHeaderVal(e.target.value)} placeholder={value.header_value_set ? "已加密保存，留空不变" : "选填"} autoComplete="new-password" />
        </div>
      </div>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        {value.status !== "not_configured" && (
          <button type="button" className="btn" disabled={testing || busy} onClick={runTest}>
            {testing && <Spinner />} 发送测试
          </button>
        )}
        <button className="btn primary" disabled={busy || testing}>{busy && <Spinner />} 保存设置</button>
      </div>
    </form>
  );
}

function DailyReportForm({ value, onSaved }: { value: T.DailyReportSettings; onSaved: (v: T.DailyReportSettings) => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [timeOfDay, setTimeOfDay] = useState(value.time_of_day || "09:00");
  const [busy, setBusy] = useState(false);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const saved = await api.updateDailyReport({ revision: value.revision, enabled, time_of_day: timeOfDay });
      onSaved(saved);
      toast("每日简报设置已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={save}>
      <div className="inline">
        <Switch on={enabled} label="启用每日流量简报" onChange={setEnabled} />
        <span>启用每日定时简报</span>
      </div>
      <div>
        <label className="lbl">每日投递时刻</label>
        <input type="time" value={timeOfDay} onChange={(e) => setTimeOfDay(e.target.value)} style={{ width: 140 }} />
      </div>
      {value.last_sent_date && <div className="sub">最近自动发送日期：{value.last_sent_date}</div>}
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存设置</button>
      </div>
    </form>
  );
}

function TelegramControlForm({ value, onSaved }: { value: T.TelegramControlSettings; onSaved: (v: T.TelegramControlSettings) => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [idsText, setIdsText] = useState(value.allowed_user_ids.join(", "));
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const ids = [...new Set(idsText.split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean))];
    if (ids.some((id) => !/^\d+$/.test(id))) return toast("Telegram 用户 ID 须为纯数字", true);
    if (enabled && !ids.length) return toast("开启远程控制时至少需填写一个白名单数字 ID", true);
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
        <span>开启交互式远程指令监听</span>
      </div>
      <div>
        <label className="lbl">允许操作的 Telegram 用户数字 ID（逗号分隔）</label>
        <input type="text" value={idsText} onChange={(e) => setIdsText(e.target.value)} placeholder="如：123456789" />
        <div className="field-help">
          支持指令：<span className="mono">/status</span>、<span className="mono">/quota</span>、<span className="mono">/start 实例名</span>、<span className="mono">/stop 实例名</span>、<span className="mono">/reboot 实例名</span>、<span className="mono">/changeip 实例名</span>。所有写操作均需二次按钮确认。
        </div>
      </div>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存设置</button>
      </div>
    </form>
  );
}

function QuietHoursForm({ value, onSaved }: { value: T.QuietHours; onSaved: (v: T.QuietHours) => void }) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(value.enabled);
  const [start, setStart] = useState(value.start);
  const [end, setEnd] = useState(value.end);
  const [maintMins, setMaintMins] = useState("0");
  const [busy, setBusy] = useState(false);

  const hasActiveMaint = Boolean(value.maintenance && Date.parse(value.maintenance.end_at) > Date.now());

  const save = async (clearMaint = false) => {
    if (!/^\d{2}:\d{2}$/.test(start) || !/^\d{2}:\d{2}$/.test(end)) {
      return toast("时间请按 HH:MM 格式填写", true);
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
      toast(clearMaint ? "已结束临时维护期" : "静默设置已保存");
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
        <span>开启每日固定免打扰时段</span>
      </div>
      <div className="grid g2">
        <div>
          <label className="lbl">静默开始 (HH:MM)</label>
          <input type="text" value={start} onChange={(e) => setStart(e.target.value)} />
        </div>
        <div>
          <label className="lbl">静默结束 (HH:MM)</label>
          <input type="text" value={end} onChange={(e) => setEnd(e.target.value)} />
        </div>
      </div>

      {hasActiveMaint ? (
        <div className="inline" style={{ padding: "10px 12px", borderRadius: "var(--r-md)", background: "var(--tint-warning)" }}>
          <Badge level="warning">临时维护中 · 至 {stamp(value.maintenance!.end_at)}</Badge>
          <div className="spacer" />
          <button type="button" className="btn sm" disabled={busy} onClick={() => save(true)}>立即结束维护</button>
        </div>
      ) : (
        <div>
          <label className="lbl">开启一次性临时维护窗口</label>
          <select value={maintMins} onChange={(e) => setMaintMins(e.target.value)}>
            <option value="0">不开启</option>
            <option value="30">未来 30 分钟</option>
            <option value="60">未来 1 小时</option>
            <option value="180">未来 3 小时</option>
          </select>
        </div>
      )}

      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button type="button" className="btn primary" disabled={busy} onClick={() => save(false)}>{busy && <Spinner />} 保存设置</button>
      </div>
    </div>
  );
}

/* ============================================================================
 * Policy Cards & Slide-over Policy Drawers
 * ========================================================================== */
function KeepaliveCard({ res, instances, accName }: { res: ReturnType<typeof useResource<T.KeepaliveSettings>>; instances: T.Instance[]; accName: (id: string) => string }) {
  const toast = useToast();
  const [editing, setEditing] = useState<T.KeepaliveSettings | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <Card
      title="意外停止保活"
      sub="期望运行态实例意外停止时自动拉起（不干扰额度停机与定时关机）"
      right={res.data && !editing && <button className="btn sm" onClick={() => setEditing(structuredClone(res.data!))}>配置</button>}
    >
      <Async res={res}>
        {(k) => {
          const cur = editing ?? k;
          const save = async () => {
            setBusy(true);
            try {
              const saved = await api.updateKeepaliveSettings(cur);
              res.mutate(() => saved);
              setEditing(null);
              toast("意外停止保活已保存");
            } catch (e) {
              toast(errMsg(e), true);
            } finally {
              setBusy(false);
            }
          };
          if (!editing) {
            return (
              <div className="stack" style={{ gap: 8 }}>
                <div className="inline">
                  <Badge level={k.enabled ? "good" : "neutral"}>{k.enabled ? "已开启" : "未开启"}</Badge>
                  {k.dry_run && <Badge level="warning">试运行</Badge>}
                  {k.spot_auto_recover && <span className="tag">含抢占式恢复</span>}
                </div>
                <div className="sub">
                  守护实例：{k.target_instance_ids.length ? k.target_instance_ids.map((id) => instances.find((i) => i.id === id)?.name ?? id).join("、") : "未选择"}
                </div>
              </div>
            );
          }
          return (
            <div className="stack">
              <div className="inline">
                <Switch on={cur.enabled} label="开启意外停止保活" onChange={(v) => setEditing({ ...cur, enabled: v })} />
                <span>开启保活</span>
                <label className={`check ${cur.spot_auto_recover ? "on" : ""}`}>
                  <input type="checkbox" checked={cur.spot_auto_recover} onChange={(e) => setEditing({ ...cur, spot_auto_recover: e.target.checked })} />
                  含 Spot 实例
                </label>
                <label className={`check ${cur.dry_run ? "on" : ""}`}>
                  <input type="checkbox" checked={cur.dry_run} onChange={(e) => setEditing({ ...cur, dry_run: e.target.checked })} />
                  试运行
                </label>
              </div>
              <div className="checks">
                {instances.map((i) => {
                  const checked = cur.target_instance_ids.includes(i.id);
                  return (
                    <label key={i.id} className={`check ${checked ? "on" : ""}`}>
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() =>
                          setEditing({
                            ...cur,
                            target_instance_ids: checked ? cur.target_instance_ids.filter((x) => x !== i.id) : [...cur.target_instance_ids, i.id],
                          })
                        }
                      />
                      {i.name} ({accName(i.account_id)})
                    </label>
                  );
                })}
              </div>
              <div className="inline" style={{ justifyContent: "flex-end" }}>
                <button className="btn sm" onClick={() => setEditing(null)}>取消</button>
                <button className="btn sm primary" disabled={busy} onClick={save}>{busy && <Spinner />} 保存</button>
              </div>
            </div>
          );
        }}
      </Async>
    </Card>
  );
}

function MonthlyResetCard({ res, instances, accName }: { res: ReturnType<typeof useResource<T.MonthlyResetSettings>>; instances: T.Instance[]; accName: (id: string) => string }) {
  const toast = useToast();
  const [editing, setEditing] = useState<T.MonthlyResetSettings | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <Card
      title="月初额度重置自动开机"
      sub="每月 1 号免费额度重置后，自动唤醒上月因流量保护停机的实例"
      right={res.data && !editing && <button className="btn sm" onClick={() => setEditing(structuredClone(res.data!))}>配置</button>}
    >
      <Async res={res}>
        {(m) => {
          const cur = editing ?? m;
          const save = async () => {
            setBusy(true);
            try {
              const saved = await api.updateMonthlyResetSettings(cur);
              res.mutate(() => saved);
              setEditing(null);
              toast("月初自动开机已保存");
            } catch (e) {
              toast(errMsg(e), true);
            } finally {
              setBusy(false);
            }
          };
          if (!editing) {
            return (
              <div className="stack" style={{ gap: 8 }}>
                <div className="inline">
                  <Badge level={m.enabled ? "good" : "neutral"}>{m.enabled ? "已开启" : "未开启"}</Badge>
                  {m.dry_run && <Badge level="warning">试运行</Badge>}
                  {m.last_cycle_start && <span className="sub">最近执行：{stamp(m.last_cycle_start)}</span>}
                </div>
                <div className="sub">
                  唤醒范围：{m.target_instance_ids.length ? m.target_instance_ids.map((id) => instances.find((i) => i.id === id)?.name ?? id).join("、") : "未选择"}
                </div>
              </div>
            );
          }
          return (
            <div className="stack">
              <div className="inline">
                <Switch on={cur.enabled} label="开启月初自动开机" onChange={(v) => setEditing({ ...cur, enabled: v })} />
                <span>开启月初自动唤醒</span>
                <label className={`check ${cur.dry_run ? "on" : ""}`}>
                  <input type="checkbox" checked={cur.dry_run} onChange={(e) => setEditing({ ...cur, dry_run: e.target.checked })} />
                  试运行
                </label>
              </div>
              <div className="checks">
                {instances.map((i) => {
                  const checked = cur.target_instance_ids.includes(i.id);
                  return (
                    <label key={i.id} className={`check ${checked ? "on" : ""}`}>
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() =>
                          setEditing({
                            ...cur,
                            target_instance_ids: checked ? cur.target_instance_ids.filter((x) => x !== i.id) : [...cur.target_instance_ids, i.id],
                          })
                        }
                      />
                      {i.name} ({accName(i.account_id)})
                    </label>
                  );
                })}
              </div>
              <div className="inline" style={{ justifyContent: "flex-end" }}>
                <button className="btn sm" onClick={() => setEditing(null)}>取消</button>
                <button className="btn sm primary" disabled={busy} onClick={save}>{busy && <Spinner />} 保存</button>
              </div>
            </div>
          );
        }}
      </Async>
    </Card>
  );
}

function RuleDrawer({
  initial,
  accounts,
  instances,
  selfId,
  onClose,
  onSaved,
}: {
  initial?: T.QuotaProtectionRule;
  accounts: T.Account[];
  instances: T.Instance[];
  selfId: string | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(initial?.name ?? "海外免费额度 98% 保护");
  const [accountId, setAccountId] = useState(initial?.account_id ?? accounts[0]?.id ?? "");
  const [scope, setScope] = useState<"overseas" | "mainland" | "swas">(initial?.scope ?? "overseas");
  const [sourceId, setSourceId] = useState(initial?.source_instance_id ?? "");
  const [kind, setKind] = useState<"percent" | "bytes">(initial?.threshold_kind ?? "percent");
  const [thresholdVal, setThresholdVal] = useState(
    initial ? (initial.threshold_kind === "bytes" ? String(Math.round((initial.threshold / GB) * 10) / 10) : String(initial.threshold)) : "98",
  );
  const [reserve, setReserve] = useState(String(initial?.reserve_seconds ?? 60));
  const [action, setAction] = useState<"notify" | "stop_notify">(initial?.action ?? "stop_notify");
  const [targets, setTargets] = useState<string[]>(initial?.target_instance_ids ?? []);
  const [stopMode, setStopMode] = useState<"" | "keep_charging" | "stop_charging">(initial?.stop_mode ?? "keep_charging");
  const [dryRun, setDryRun] = useState(initial?.dry_run ?? true);
  const [allowUnverified, setAllowUnverified] = useState(initial?.allow_unverified_execution ?? false);
  const [confirmSelf, setConfirmSelf] = useState(Boolean(initial?.confirmed_self_instance_id));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const acctInstances = instances.filter((i) => i.account_id === accountId);
  const swasInstances = acctInstances.filter((i) => i.kind === "swas");
  const includesSelf = !!selfId && targets.includes(selfId);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return setError("请填写规则名称。");
    const numVal = Number(thresholdVal);
    if (!(numVal > 0) || (kind === "percent" && (numVal > 100 || !Number.isInteger(numVal)))) {
      return setError(kind === "percent" ? "百分比请填 1–100 的整数。" : "阈值 GB 必须大于 0。");
    }
    if (scope === "swas" && !sourceId) return setError("请选择要监控套餐流量的轻量服务器。");
    if (action === "stop_notify" && !targets.length) return setError("请至少选择一台要关机的目标实例。");
    if (includesSelf && !confirmSelf) return setError("目标包含运行面板自身的实例，请勾选底部确认框或取消选择该实例。");

    const body: T.QuotaProtectionRuleInput = {
      revision: initial?.revision ?? 0,
      name: name.trim(),
      enabled: initial?.enabled ?? true,
      account_id: accountId,
      scope,
      source_instance_id: scope === "swas" ? sourceId : "",
      threshold_kind: kind,
      threshold: kind === "percent" ? Math.round(numVal) : Math.round(numVal * GB),
      action,
      target_instance_ids: action === "stop_notify" ? targets : [],
      stop_mode: action === "stop_notify" && scope !== "swas" ? (stopMode || "keep_charging") : "",
      reserve_seconds: Math.max(0, Math.round(Number(reserve) || 0)),
      dry_run: dryRun,
      allow_unverified_execution: allowUnverified,
      confirmed_self_instance_id: includesSelf && selfId ? selfId : "",
    };
    setBusy(true);
    setError(undefined);
    try {
      if (initial) await api.updateProtectionRule(initial.id, body);
      else await api.createProtectionRule(body);
      toast(initial ? "保护规则已更新" : "保护规则已创建");
      onSaved();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer wide title={initial ? "编辑额度保护规则" : "新建额度保护规则"} sub="设置流量阈值与自动停机保护策略" onClose={onClose}>
      <form className="stack" onSubmit={submit} noValidate>
        <div>
          <label className="lbl" htmlFor="pr-name">规则名称</label>
          <input id="pr-name" type="text" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="grid g2">
          <div>
            <label className="lbl" htmlFor="pr-acc">所属云账号</label>
            <select
              id="pr-acc"
              value={accountId}
              onChange={(e) => {
                setAccountId(e.target.value);
                setTargets([]);
                setSourceId("");
              }}
            >
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="lbl" htmlFor="pr-scope">监控流量池</label>
            <select id="pr-scope" value={scope} onChange={(e) => setScope(e.target.value as typeof scope)}>
              <option value="overseas">非中国内地免费额度</option>
              <option value="mainland">中国内地免费额度</option>
              <option value="swas">轻量服务器套餐流量包</option>
            </select>
          </div>
        </div>
        {scope === "swas" && (
          <div>
            <label className="lbl" htmlFor="pr-swas">选择轻量服务器</label>
            <select
              id="pr-swas"
              value={sourceId}
              onChange={(e) => {
                setSourceId(e.target.value);
                if (e.target.value) setTargets([e.target.value]);
              }}
            >
              <option value="">请选择…</option>
              {swasInstances.map((i) => (
                <option key={i.id} value={i.id}>{i.name}</option>
              ))}
            </select>
          </div>
        )}
        <div className="grid g2">
          <div>
            <label className="lbl">触发阈值</label>
            <div className="inline">
              <SegToggle
                label="阈值类型"
                value={kind}
                onChange={(v) => {
                  setKind(v);
                  setThresholdVal(v === "percent" ? "98" : "195");
                }}
                options={[
                  ["percent", "按百分比"],
                  ["bytes", "按 GB"],
                ]}
              />
              <input
                type="number"
                value={thresholdVal}
                onChange={(e) => setThresholdVal(e.target.value)}
                style={{ width: 96 }}
                min={1}
                max={kind === "percent" ? 100 : undefined}
                step={kind === "percent" ? 1 : 0.5}
              />
              <span>{kind === "percent" ? "%" : "GB"}</span>
            </div>
          </div>
          <div>
            <label className="lbl" htmlFor="pr-res">提前预留秒数（抵消关机延迟）</label>
            <input id="pr-res" type="number" value={reserve} onChange={(e) => setReserve(e.target.value)} min={0} max={3600} />
          </div>
        </div>
        <div>
          <label className="lbl">触发动作</label>
          <SegToggle
            label="动作"
            value={action}
            onChange={setAction}
            options={[
              ["stop_notify", "自动关机并通知"],
              ["notify", "仅发通知不关机"],
            ]}
          />
        </div>
        {action === "stop_notify" && (
          <>
            <div>
              <span className="lbl">要关机的目标实例</span>
              <div className="checks">
                {acctInstances.map((i) => {
                  const on = targets.includes(i.id);
                  return (
                    <label key={i.id} className={`check ${on ? "on" : ""}`}>
                      <input type="checkbox" checked={on} onChange={() => setTargets(on ? targets.filter((x) => x !== i.id) : [...targets, i.id])} />
                      {i.name}{i.is_server_self ? "（面板自身）" : ""}
                    </label>
                  );
                })}
              </div>
            </div>
            {scope !== "swas" && (
              <div>
                <label className="lbl">ECS 停机模式</label>
                <SegToggle
                  label="关机方式"
                  value={(stopMode || "keep_charging") as "keep_charging" | "stop_charging"}
                  onChange={setStopMode}
                  options={[
                    ["keep_charging", STOP_MODE.keep_charging[0]],
                    ["stop_charging", STOP_MODE.stop_charging[0]],
                  ]}
                />
                <div className="field-help">{STOP_MODE[(stopMode || "keep_charging") as "keep_charging" | "stop_charging"][1]}</div>
              </div>
            )}
          </>
        )}
        <label className="rowline" style={{ cursor: "pointer" }}>
          <input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} style={{ width: "auto" }} />
          <span className="grow">
            <b>试运行模式</b>
            <span className="sub" style={{ display: "block" }}>开启后仅记录判定结果并发送告警，不执行真实停机。</span>
          </span>
        </label>
        {scope !== "swas" && (
          <label className="rowline" style={{ cursor: "pointer" }}>
            <input type="checkbox" checked={allowUnverified} onChange={(e) => setAllowUnverified(e.target.checked)} style={{ width: "auto" }} />
            <span className="grow">
              <b>允许依据实时采集估算立即执行停机</b>
              <span className="sub" style={{ display: "block" }}>由于阿里云 CDT 官方出账通常延迟约 1 小时，勾选后当 hm-agent 实时增量越线时立即关机阻断超额。</span>
            </span>
          </label>
        )}
        {includesSelf && (
          <label className="rowline sim-card" style={{ cursor: "pointer", padding: "10px 12px", borderRadius: "var(--r-md)" }}>
            <input type="checkbox" checked={confirmSelf} onChange={(e) => setConfirmSelf(e.target.checked)} style={{ width: "auto" }} />
            <span className="grow"><b>确认允许对运行面板自身的实例执行自动停机。</b></span>
          </label>
        )}
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="inline" style={{ justifyContent: "flex-end", paddingTop: 8 }}>
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存规则</button>
        </div>
      </form>
    </Drawer>
  );
}

function ScheduleDrawer({
  initial,
  accounts,
  instances,
  selfId,
  onClose,
  onSaved,
}: {
  initial?: T.PowerSchedule;
  accounts: T.Account[];
  instances: T.Instance[];
  selfId: string | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(initial?.name ?? "夜间自动停机");
  const [accountId, setAccountId] = useState(initial?.account_id ?? accounts[0]?.id ?? "");
  const [action, setAction] = useState<"start" | "stop">(initial?.action ?? "stop");
  const [stopMode, setStopMode] = useState<"" | "keep_charging" | "stop_charging">(initial?.stop_mode || "keep_charging");
  const [timeOfDay, setTimeOfDay] = useState(initial?.time_of_day ?? "01:00");
  const [days, setDays] = useState<number[]>(initial?.days_of_week ?? [1, 2, 3, 4, 5, 6, 0]);
  const [catchUp, setCatchUp] = useState(String(initial?.catch_up_minutes ?? 15));
  const [targets, setTargets] = useState<string[]>(initial?.target_instance_ids ?? []);
  const [dryRun, setDryRun] = useState(initial?.dry_run ?? false);
  const [confirmSelf, setConfirmSelf] = useState(Boolean(initial?.confirmed_self_instance_id));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const acctInstances = instances.filter((i) => i.account_id === accountId);
  const includesSelf = action === "stop" && !!selfId && targets.includes(selfId);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return setError("请填写计划名称。");
    if (!/^\d{2}:\d{2}$/.test(timeOfDay)) return setError("时间请按 HH:MM 格式填写。");
    if (!days.length) return setError("请至少选择一天。");
    if (!targets.length) return setError("请至少选择一台目标实例。");
    if (includesSelf && !confirmSelf) return setError("目标包含面板所在实例，请勾选确认框。");

    const body: T.PowerScheduleInput = {
      revision: initial?.revision ?? 0,
      name: name.trim(),
      enabled: initial?.enabled ?? true,
      account_id: accountId,
      action,
      stop_mode: action === "stop" ? (stopMode || "keep_charging") : "",
      time_of_day: timeOfDay,
      days_of_week: days,
      catch_up_minutes: Math.max(0, Math.min(120, Math.round(Number(catchUp) || 15))),
      target_instance_ids: targets,
      dry_run: dryRun,
      confirmed_self_instance_id: includesSelf && selfId ? selfId : "",
    };
    setBusy(true);
    setError(undefined);
    try {
      if (initial) await api.updatePowerSchedule(initial.id, body);
      else await api.createPowerSchedule(body);
      toast(initial ? "定时计划已保存" : "定时计划已创建");
      onSaved();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer wide title={initial ? "编辑定时开关机计划" : "新建定时开关机计划"} sub="按面板业务时区周期性执行启停" onClose={onClose}>
      <form className="stack" onSubmit={submit} noValidate>
        <div className="grid g2">
          <div>
            <label className="lbl">计划名称</label>
            <input type="text" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <label className="lbl">所属账号</label>
            <select
              value={accountId}
              onChange={(e) => {
                setAccountId(e.target.value);
                setTargets([]);
              }}
            >
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
          </div>
        </div>
        <div className="grid g2">
          <div>
            <label className="lbl">动作</label>
            <SegToggle label="动作" value={action} onChange={setAction} options={[["stop", "关机"], ["start", "开机"]]} />
          </div>
          <div>
            <label className="lbl">执行时间 (HH:MM)</label>
            <input type="time" value={timeOfDay} onChange={(e) => setTimeOfDay(e.target.value)} />
          </div>
        </div>
        {action === "stop" && (
          <div>
            <label className="lbl">停机模式</label>
            <SegToggle
              label="关机方式"
              value={(stopMode || "keep_charging") as "keep_charging" | "stop_charging"}
              onChange={setStopMode}
              options={[
                ["keep_charging", STOP_MODE.keep_charging[0]],
                ["stop_charging", STOP_MODE.stop_charging[0]],
              ]}
            />
            <div className="field-help">{STOP_MODE[(stopMode || "keep_charging") as "keep_charging" | "stop_charging"][1]}</div>
          </div>
        )}
        <div>
          <div className="inline" style={{ marginBottom: 6 }}>
            <span className="lbl" style={{ margin: 0 }}>重复周期</span>
            <button type="button" className="linkbtn sub" onClick={() => setDays([1, 2, 3, 4, 5, 6, 0])}>全选每天</button>
            <button type="button" className="linkbtn sub" onClick={() => setDays([1, 2, 3, 4, 5])}>仅工作日</button>
          </div>
          <div className="checks">
            {WEEKDAYS.map(([d, label]) => {
              const on = days.includes(d);
              return (
                <label key={d} className={`check ${on ? "on" : ""}`}>
                  <input type="checkbox" checked={on} onChange={() => setDays(on ? days.filter((x) => x !== d) : [...days, d])} />
                  {label}
                </label>
              );
            })}
          </div>
        </div>
        <div>
          <span className="lbl">目标实例</span>
          <div className="checks">
            {acctInstances.map((i) => {
              const on = targets.includes(i.id);
              return (
                <label key={i.id} className={`check ${on ? "on" : ""}`}>
                  <input type="checkbox" checked={on} onChange={() => setTargets(on ? targets.filter((x) => x !== i.id) : [...targets, i.id])} />
                  {i.name}{i.is_server_self ? "（面板自身）" : ""}
                </label>
              );
            })}
          </div>
        </div>
        <div>
          <label className="lbl">重启补执行窗口（分钟）</label>
          <input type="number" value={catchUp} onChange={(e) => setCatchUp(e.target.value)} min={0} max={120} />
        </div>
        <label className="rowline" style={{ cursor: "pointer" }}>
          <input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} style={{ width: "auto" }} />
          <span className="grow"><b>试运行模式</b><span className="sub" style={{ display: "block" }}>到点仅发通知，不执行真实开关机。</span></span>
        </label>
        {includesSelf && (
          <label className="rowline sim-card" style={{ cursor: "pointer", padding: "10px 12px", borderRadius: "var(--r-md)" }}>
            <input type="checkbox" checked={confirmSelf} onChange={(e) => setConfirmSelf(e.target.checked)} style={{ width: "auto" }} />
            <span className="grow"><b>确认允许对面板自身实例执行定时停机。</b></span>
          </label>
        )}
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="inline" style={{ justifyContent: "flex-end", paddingTop: 8 }}>
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存计划</button>
        </div>
      </form>
    </Drawer>
  );
}

function FailoverDrawer({
  initial,
  accounts,
  instances,
  onClose,
  onSaved,
}: {
  initial?: T.FailoverRule;
  accounts: T.Account[];
  instances: T.Instance[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;
  const [name, setName] = useState(initial?.name ?? "");
  const [primaryId, setPrimaryId] = useState(initial?.primary_instance_id ?? instances[0]?.id ?? "");
  const [standbyId, setStandbyId] = useState(
    initial?.standby_instance_id ?? instances.find((i) => i.id !== (initial?.primary_instance_id ?? instances[0]?.id))?.id ?? "",
  );
  const [dryRun, setDryRun] = useState(initial?.dry_run ?? false);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return toast("请填写规则名称", true);
    if (!primaryId || !standbyId || primaryId === standbyId) return toast("请选择两台不同的实例分别作为主备节点", true);
    setBusy(true);
    try {
      const payload: T.FailoverRuleInput = {
        revision: initial?.revision ?? 0,
        name: name.trim(),
        enabled,
        dry_run: dryRun,
        primary_instance_id: primaryId,
        standby_instance_id: standbyId,
      };
      if (initial) await api.updateFailoverRule(initial.id, payload);
      else await api.createFailoverRule(payload);
      toast(initial ? "主备规则已更新" : "主备规则已创建");
      onSaved();
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer title={initial ? "编辑主备容灾规则" : "新建主备容灾规则"} sub="主实例因额度保护停机时自动拉起跨地域或跨账号备机" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <div>
          <label className="lbl">规则名称</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="如：新加坡主力切香港备机" />
        </div>
        <div>
          <label className="lbl">主实例（被额度保护停机时触发）</label>
          <select value={primaryId} onChange={(e) => setPrimaryId(e.target.value)}>
            {instances.map((i) => (
              <option key={i.id} value={i.id}>{i.name} · {accName(i.account_id)}</option>
            ))}
          </select>
        </div>
        <div>
          <label className="lbl">备实例（自动开机接管）</label>
          <select value={standbyId} onChange={(e) => setStandbyId(e.target.value)}>
            {instances.map((i) => (
              <option key={i.id} value={i.id} disabled={i.id === primaryId}>{i.name} · {accName(i.account_id)}</option>
            ))}
          </select>
        </div>
        <div className="inline">
          <Switch on={dryRun} onChange={setDryRun} label="试运行模式" />
          <span>试运行模式（仅发通知，不真拉起备实例）</span>
        </div>
        <div className="inline">
          <Switch on={enabled} onChange={setEnabled} label="立即启用" />
          <span>保存后启用此规则</span>
        </div>
        <div className="inline" style={{ justifyContent: "flex-end", paddingTop: 8 }}>
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存规则</button>
        </div>
      </form>
    </Drawer>
  );
}

function SimulationModal({
  accounts,
  instances,
  existing,
  onClose,
  onSaved,
}: {
  accounts: T.Account[];
  instances: T.Instance[];
  existing: T.QuotaSimulation[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [accountId, setAccountId] = useState(accounts[0]?.id ?? "");
  const [scope, setScope] = useState<"overseas" | "mainland" | "swas">("overseas");
  const [sourceId, setSourceId] = useState("");
  const [gbStr, setGbStr] = useState("198");
  const [mins, setMins] = useState("15");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const swas = instances.filter((i) => i.account_id === accountId && i.kind === "swas");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const val = Number(gbStr);
    if (!(val >= 0)) return setError("请填写有效的模拟流量数值。");
    if (scope === "swas" && !sourceId) return setError("请选择轻量服务器。");
    const src = scope === "swas" ? sourceId : "";
    const cur = existing.find((s) => s.account_id === accountId && s.scope === scope && s.source_instance_id === src);
    setBusy(true);
    setError(undefined);
    try {
      await api.setQuotaSimulation({
        revision: cur?.revision ?? 0,
        account_id: accountId,
        scope,
        source_instance_id: src,
        usage_bytes: Math.round(val * GB),
        expires_at: new Date(Date.now() + (Number(mins) || 15) * 60_000).toISOString(),
      });
      toast("模拟用量已生效");
      onSaved();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="模拟用量演练" onClose={onClose}>
      <p>
        临时注入虚拟已用流量以检验保护规则判定与多渠道告警投递。<b>演练期间绝不会执行真实关机</b>，到期自动恢复。
      </p>
      <form className="stack" onSubmit={submit} noValidate>
        <div>
          <label className="lbl">云账号</label>
          <select
            value={accountId}
            onChange={(e) => {
              setAccountId(e.target.value);
              setSourceId("");
            }}
          >
            {accounts.map((a) => (
              <option key={a.id} value={a.id}>{a.name}</option>
            ))}
          </select>
        </div>
        <div>
          <label className="lbl">流量池范围</label>
          <select value={scope} onChange={(e) => setScope(e.target.value as typeof scope)}>
            <option value="overseas">非中国内地免费额度</option>
            <option value="mainland">中国内地免费额度</option>
            <option value="swas">轻量服务器套餐流量包</option>
          </select>
        </div>
        {scope === "swas" && (
          <div>
            <label className="lbl">轻量服务器</label>
            <select value={sourceId} onChange={(e) => setSourceId(e.target.value)}>
              <option value="">请选择…</option>
              {swas.map((i) => (
                <option key={i.id} value={i.id}>{i.name}</option>
              ))}
            </select>
          </div>
        )}
        <div className="grid g2">
          <div>
            <label className="lbl">模拟已用流量（GB）</label>
            <input type="number" value={gbStr} onChange={(e) => setGbStr(e.target.value)} min={0} step={0.5} />
          </div>
          <div>
            <label className="lbl">持续时长</label>
            <select value={mins} onChange={(e) => setMins(e.target.value)}>
              <option value="5">5 分钟</option>
              <option value="15">15 分钟</option>
              <option value="30">30 分钟</option>
              <option value="60">60 分钟</option>
            </select>
          </div>
        </div>
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 开始演练</button>
        </div>
      </form>
    </Modal>
  );
}
