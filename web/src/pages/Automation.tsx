// 自动化：额度保护规则、模拟用量演练、定时开关机、意外停止保活、每月 1 号自动开机、任务记录。
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { waitForJob } from "../api/jobs";
import { DECISION_OUTCOME, DECISION_STATE, JOB_STATUS, JOB_TRIGGER, JOB_TYPE, SCOPE, STOP_MODE } from "../api/labels";
import type * as T from "../api/types";
import { Async, Badge, Banner, Card, Confirm, EmptyState, ErrorState, errMsg, Modal, SegToggle, Skeleton, Spinner, Switch, useToast } from "../components/ui";
import { useShared } from "../lib/data";
import { GB, gb, stamp } from "../lib/format";
import { Link } from "../lib/router";
import { useResource } from "../lib/useResource";

const WEEKDAYS: [number, string][] = [[1, "周一"], [2, "周二"], [3, "周三"], [4, "周四"], [5, "周五"], [6, "周六"], [0, "周日"]];

export function Automation() {
  const toast = useToast();
  const { accounts, instances, reloadAll } = useShared();
  const status = useResource(api.protectionStatus, []);
  const rules = useResource(api.protectionRules, []);
  const sims = useResource(api.quotaSimulations, [], { refreshMs: 15_000 });
  const schedules = useResource(api.powerSchedules, []);
  const keepalive = useResource(api.keepaliveSettings, []);
  const monthly = useResource(api.monthlyResetSettings, []);
  const failovers = useResource(api.failoverRules, []);

  const [ruleModal, setRuleModal] = useState<{ rule?: T.QuotaProtectionRule } | null>(null);
  const [delRule, setDelRule] = useState<T.QuotaProtectionRule>();
  const [schModal, setSchModal] = useState<{ sch?: T.PowerSchedule } | null>(null);
  const [delSch, setDelSch] = useState<T.PowerSchedule>();
  const [foModal, setFoModal] = useState<{ rule?: T.FailoverRule } | null>(null);
  const [delFo, setDelFo] = useState<T.FailoverRule>();
  const [simModal, setSimModal] = useState(false);
  const [answering, setAnswering] = useState<string>();

  if (accounts.error) return <div className="card"><ErrorState error={accounts.error} onRetry={accounts.reload} /></div>;
  if (!accounts.data || !instances.data) return <div className="card"><Skeleton lines={6} /></div>;
  const accs = accounts.data;
  const insts = instances.data.items;
  if (!accs.length) {
    return (
      <div className="card">
        <EmptyState title="还没有添加阿里云账号" action={<Link className="btn primary" to="/settings">去添加账号</Link>}>
          添加账号并发现实例后，可以在这里设置额度保护停机、定时开关机与保活策略。
        </EmptyState>
      </div>
    );
  }

  const accName = (id: string) => accs.find((a) => a.id === id)?.name ?? id;
  const instName = (id: string) => insts.find((i) => i.id === id)?.name ?? id;
  const pendingConfirms = insts.filter((i) => i.pending_intent_confirmation);

  const answerIntent = async (inst: T.Instance, decision: "keep_stopped" | "resume") => {
    setAnswering(inst.id);
    try {
      const r = await api.confirmInstanceIntent(inst.id, decision);
      if (r.job_id) {
        toast(`已发送开机请求，正在恢复 ${inst.name}…`);
        await waitForJob(r.job_id);
      } else {
        toast(`已将 ${inst.name} 设为保持停止`);
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
      toast(enabled ? `已启用“${r.name}”` : `已停用“${r.name}”`);
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
      toast(enabled ? `已启用“${s.name}”，从下一个计划时间点生效` : `已停用“${s.name}”`);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  const st = status.data;
  const selfLabel = !st ? "读取中…" : !st.self_identity_known ? "未确认面板所在实例（自动停机将拦截）" : st.self_instance_id ? `自动排除面板自身（${instName(st.self_instance_id)}）` : "面板独立部署（不在受管实例上）";

  return (
    <>
      {pendingConfirms.map((i) => (
        <Banner
          key={i.id}
          level="warning"
          action={
            <div className="inline">
              <button className="btn sm" disabled={answering === i.id} onClick={() => answerIntent(i, "keep_stopped")}>保持停止</button>
              <button className="btn sm primary" disabled={answering === i.id} onClick={() => answerIntent(i, "resume")}>{answering === i.id && <Spinner />} 恢复运行</button>
            </div>
          }
        >
          <b>发现 {i.name} 在面板外被关机。</b> 期望状态原本是运行中；在确认前已暂停对它的意外停止保活。
        </Banner>
      ))}

      {(sims.data ?? []).map((s) => (
        <div className="card sim-card" key={`${s.account_id}-${s.scope}-${s.source_instance_id}`}>
          <div className="inline">
            <Badge level="warning">模拟生效中</Badge>
            <b>{accName(s.account_id)} · {s.scope === "swas" ? `轻量 ${instName(s.source_instance_id)}` : SCOPE[s.scope]}</b>
            <span>模拟已用 <b>{gb(s.usage_bytes)}</b>，持续到 {stamp(s.expires_at)}</span>
            <span className="sub">模拟期间只发带【模拟】标记的通知与判定记录，绝不会真实停机。</span>
            <div className="spacer" />
            <button className="btn sm" onClick={() => clearSim(s)}>立即清除模拟</button>
          </div>
        </div>
      ))}

      <Card
        title="安全边界与演练"
        sub="优先级：额度保护 > 定时关机 > 意外停止保活 / 每月 1 号自动开机"
        right={<button className="btn" onClick={() => setSimModal(true)}>模拟用量演练</button>}
      >
        <div className="inline" style={{ gap: "8px 16px" }}>
          {st ? (
            <Badge level={st.power_enabled ? "good" : "warning"}>{st.power_enabled ? "已开启实例开关机" : "未开启真实开关机（仅试运行）"}</Badge>
          ) : (
            <Skeleton lines={1} />
          )}
          <span className="chip">{selfLabel}</span>
          {st && <span className="chip">熔断上限：{Math.round(st.automatic_window_seconds / 60)} 分钟内最多自动操作 {st.automatic_limit} 台</span>}
        </div>
      </Card>

      <Card title="额度保护自动关机" sub="免费流量或轻量套餐快用完时，提前关掉指定机器避免超额计费" right={<button className="btn primary" onClick={() => setRuleModal({})}>＋ 添加保护规则</button>}>
        <Async res={rules}>
          {(list) =>
            !list.length ? (
              <EmptyState title="还没有额度保护规则" action={<button className="btn primary" onClick={() => setRuleModal({})}>创建第一条规则</button>}>
                建议先勾选“试运行”创建一条规则，配合右上角“模拟用量演练”验证通知无误后再开启真实停机。
              </EmptyState>
            ) : (
              <div>
                {list.map((r) => (
                  <div className={`rowline ${r.dry_run ? "sim-card" : ""}`} key={r.id} style={r.dry_run ? { padding: "10px 12px", borderRadius: "var(--r-md)", marginBottom: 8 } : { alignItems: "flex-start" }}>
                    <div className="grow">
                      <div className="inline">
                        <b>{r.name}</b>
                        {r.dry_run && <Badge level="warning">试运行</Badge>}
                        {r.allow_unverified_execution && !r.dry_run && <span className="tag">含实时估算触发</span>}
                        <span className="tag">{r.action === "stop_notify" ? (r.stop_mode === "stop_charging" ? "节省停机 + 通知" : "普通停机 + 通知") : "仅发通知"}</span>
                      </div>
                      <div className="sub" style={{ marginTop: 4 }}>
                        账号 {accName(r.account_id)} · {r.scope === "swas" ? `轻量套餐（${instName(r.source_instance_id)}）` : `${SCOPE[r.scope]}免费额度`} · 达到{" "}
                        <b>{r.threshold_kind === "percent" ? `${r.threshold}%` : gb(r.threshold)}</b>（含提前 {r.reserve_seconds} 秒速度余量）
                        {r.action === "stop_notify" && ` → 关机：${r.target_instance_ids.map(instName).join("、")}`}
                      </div>
                    </div>
                    <div className="inline">
                      <button className="btn sm" onClick={() => setRuleModal({ rule: r })}>修改</button>
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

      <Card title="定时开关机" sub="按面板业务时区每天或指定星期几自动执行；新启用的计划从下一个时间点开始生效" right={<button className="btn primary" onClick={() => setSchModal({})}>＋ 添加定时计划</button>}>
        <Async res={schedules}>
          {(list) =>
            !list.length ? (
              <EmptyState title="还没有定时开关机计划">适合测试机夜间自动节省停机、白天按时开机。</EmptyState>
            ) : (
              <div>
                {list.map((s) => (
                  <div className={`rowline ${s.dry_run ? "sim-card" : ""}`} key={s.id} style={s.dry_run ? { padding: "10px 12px", borderRadius: "var(--r-md)", marginBottom: 8 } : { alignItems: "flex-start" }}>
                    <div className="grow">
                      <div className="inline">
                        <b>{s.name}</b>
                        <span className="mono">{s.time_of_day}</span>
                        <span className="tag">{s.action === "start" ? "自动开机" : s.stop_mode === "stop_charging" ? "节省停机" : "普通停机"}</span>
                        {s.dry_run && <Badge level="warning">试运行</Badge>}
                      </div>
                      <div className="sub" style={{ marginTop: 4 }}>
                        {s.days_of_week.length === 7 ? "每天" : WEEKDAYS.filter(([d]) => s.days_of_week.includes(d)).map(([, n]) => n).join("、")} · 账号 {accName(s.account_id)} · 目标：{s.target_instance_ids.map(instName).join("、")}
                        {s.last_triggered_at && ` · 上次触发：${stamp(s.last_triggered_at)}`}
                      </div>
                    </div>
                    <div className="inline">
                      <button className="btn sm" onClick={() => setSchModal({ sch: s })}>修改</button>
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

      <div className="grid g2">
        <KeepaliveCard res={keepalive} instances={insts} accName={accName} />
        <MonthlyResetCard res={monthly} instances={insts} accName={accName} />
      </div>

      <Card
        title="主备切换（跨账号容灾）"
        sub="当主实例因流量额度保护自动停机时，自动拉起备实例（支持跨阿里云账号）并发出通知"
        right={<button className="btn primary" disabled={insts.length < 2} onClick={() => setFoModal({})}>＋ 添加主备规则</button>}
      >
        <Async res={failovers}>
          {(list) =>
            !list.length ? (
              <EmptyState title="还没有主备切换规则">
                {insts.length < 2 ? "至少需要两台纳管实例才能配置主备切换。" : "适合主节点流量耗尽自动停机后，立即将同地域或跨账号备机开机接管。"}
              </EmptyState>
            ) : (
              <div>
                {list.map((f) => {
                  const pInst = insts.find((x) => x.id === f.primary_instance_id);
                  const sInst = insts.find((x) => x.id === f.standby_instance_id);
                  return (
                    <div className={`rowline ${f.dry_run ? "sim-card" : ""}`} key={f.id} style={f.dry_run ? { padding: "10px 12px", borderRadius: "var(--r-md)", marginBottom: 8 } : { alignItems: "flex-start" }}>
                      <div className="grow">
                        <div className="inline">
                          <b>{f.name}</b>
                          {f.dry_run && <Badge level="warning">试运行</Badge>}
                          <span className="tag">额度停机 → 拉起备机</span>
                        </div>
                        <div className="sub" style={{ marginTop: 4 }}>
                          主实例：<b>{pInst ? `${pInst.name}（${accName(pInst.account_id)}）` : f.primary_instance_id}</b> → 备实例：<b>{sInst ? `${sInst.name}（${accName(sInst.account_id)}）` : f.standby_instance_id}</b>
                          {f.last_triggered_at && ` · 上次触发：${stamp(f.last_triggered_at)}`}
                        </div>
                      </div>
                      <div className="inline">
                        <button className="btn sm" onClick={() => setFoModal({ rule: f })}>修改</button>
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
                              toast(enabled ? `已启用主备规则“${f.name}”` : `已停用“${f.name}”`);
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

      <DecisionsFold instName={instName} accName={accName} rules={rules.data ?? []} />
      <JobsFold instName={instName} accName={accName} accounts={accs} />

      {ruleModal && (
        <RuleModal
          initial={ruleModal.rule}
          accounts={accs}
          instances={insts}
          selfId={st?.self_instance_id ?? null}
          onClose={() => setRuleModal(null)}
          onSaved={() => { setRuleModal(null); rules.reload(); }}
        />
      )}
      {delRule && (
        <Confirm
          title={`删除规则“${delRule.name}”？`}
          okText="删除"
          danger
          body="删除后不再按这条规则检查流量或执行自动停机。"
          onClose={() => setDelRule(undefined)}
          onOk={async () => {
            await api.deleteProtectionRule(delRule.id, delRule.revision);
            rules.reload();
            toast("规则已删除");
          }}
        />
      )}
      {schModal && (
        <ScheduleModal
          initial={schModal.sch}
          accounts={accs}
          instances={insts}
          selfId={st?.self_instance_id ?? null}
          onClose={() => setSchModal(null)}
          onSaved={() => { setSchModal(null); schedules.reload(); }}
        />
      )}
      {delSch && (
        <Confirm
          title={`删除定时计划“${delSch.name}”？`}
          okText="删除"
          danger
          body="删除后不会再按这个时间自动开关机。"
          onClose={() => setDelSch(undefined)}
          onOk={async () => {
            await api.deletePowerSchedule(delSch.id, delSch.revision);
            schedules.reload();
            toast("定时计划已删除");
          }}
        />
      )}
      {simModal && (
        <SimulationModal
          accounts={accs}
          instances={insts}
          existing={sims.data ?? []}
          onClose={() => setSimModal(false)}
          onSaved={() => { setSimModal(false); sims.reload(); }}
        />
      )}
      {foModal && (
        <FailoverModal
          initial={foModal.rule}
          accounts={accs}
          instances={insts}
          onClose={() => setFoModal(null)}
          onSaved={() => { setFoModal(null); failovers.reload(); }}
        />
      )}
      {delFo && (
        <Confirm
          title={`删除主备切换规则“${delFo.name}”？`}
          okText="删除"
          danger
          body="删除后，主实例因额度停机时不再自动启动备实例。"
          onClose={() => setDelFo(undefined)}
          onOk={async () => {
            await api.deleteFailoverRule(delFo.id, delFo.revision);
            failovers.reload();
            toast("主备切换规则已删除");
          }}
        />
      )}
    </>
  );
}

function FailoverModal({
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
  const [standbyId, setStandbyId] = useState(initial?.standby_instance_id ?? instances.find((i) => i.id !== (initial?.primary_instance_id ?? instances[0]?.id))?.id ?? "");
  const [dryRun, setDryRun] = useState(initial?.dry_run ?? false);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return toast("请填写规则名称", true);
    if (!primaryId || !standbyId || primaryId === standbyId) return toast("请选择两台不同的实例分别作为主实例与备实例", true);
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
    <Modal title={initial ? "修改主备切换规则" : "添加主备切换规则"} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <div>
          <label className="lbl">规则名称</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="如：新加坡主力耗尽切香港备机" />
        </div>
        <div>
          <label className="lbl">主实例（被额度保护停机时触发）</label>
          <select value={primaryId} onChange={(e) => setPrimaryId(e.target.value)} style={{ width: "100%" }}>
            {instances.map((i) => (
              <option key={i.id} value={i.id}>{i.name} · {accName(i.account_id)}</option>
            ))}
          </select>
        </div>
        <div>
          <label className="lbl">备实例（自动开机接管，支持跨账号）</label>
          <select value={standbyId} onChange={(e) => setStandbyId(e.target.value)} style={{ width: "100%" }}>
            {instances.map((i) => (
              <option key={i.id} value={i.id} disabled={i.id === primaryId}>{i.name} · {accName(i.account_id)}</option>
            ))}
          </select>
        </div>
        <div className="inline">
          <Switch on={dryRun} onChange={setDryRun} label="试运行模式" />
          <span>试运行模式（只发命中提醒，不真正启动备实例）</span>
        </div>
        <div className="inline">
          <Switch on={enabled} onChange={setEnabled} label="立即启用" />
          <span>保存后启用此规则</span>
        </div>
        <div className="actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存</button>
        </div>
      </form>
    </Modal>
  );
}

function KeepaliveCard({ res, instances, accName }: { res: ReturnType<typeof useResource<T.KeepaliveSettings>>; instances: T.Instance[]; accName: (id: string) => string }) {
  const toast = useToast();
  const [editing, setEditing] = useState<T.KeepaliveSettings | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <Card
      title="意外停止保活"
      sub="期望运行中的机器意外停止时自动拉起；额度保护或定时关机期间不会抢着开机"
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
                  {k.spot_auto_recover && <span className="tag">抢占式实例自动拉起</span>}
                </div>
                <div className="sub">
                  受保活实例：{k.target_instance_ids.length ? k.target_instance_ids.map((id) => instances.find((i) => i.id === id)?.name ?? id).join("、") : "尚未勾选"}
                </div>
                <p className="note" style={{ margin: 0 }}>如果检测到你在阿里云控制台手动关机，面板会先发通知询问“保持停止”还是“恢复运行”，不会贸然开机。</p>
              </div>
            );
          }
          return (
            <div className="stack">
              <div className="inline">
                <Switch on={cur.enabled} label="开启意外停止保活" onChange={(v) => setEditing({ ...cur, enabled: v })} />
                <span>开启意外停止保活</span>
                <label className={`check ${cur.spot_auto_recover ? "on" : ""}`}>
                  <input type="checkbox" checked={cur.spot_auto_recover} onChange={(e) => setEditing({ ...cur, spot_auto_recover: e.target.checked })} />
                  包含抢占式实例
                </label>
                <label className={`check ${cur.dry_run ? "on" : ""}`} style={{ marginLeft: "auto" }}>
                  <input type="checkbox" checked={cur.dry_run} onChange={(e) => setEditing({ ...cur, dry_run: e.target.checked })} />
                  试运行（只发通知不真开机）
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
                        onChange={() => setEditing({ ...cur, target_instance_ids: checked ? cur.target_instance_ids.filter((x) => x !== i.id) : [...cur.target_instance_ids, i.id] })}
                      />
                      {i.name}（{accName(i.account_id)}）
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
      title="每月 1 号自动开机"
      sub="新月免费额度重置后，自动恢复上月因额度保护停机的实例"
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
              toast("每月 1 号自动开机已保存");
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
                  {m.last_cycle_start && <span className="sub">最近执行周期：{stamp(m.last_cycle_start)}</span>}
                </div>
                <div className="sub">
                  目标实例：{m.target_instance_ids.length ? m.target_instance_ids.map((id) => instances.find((i) => i.id === id)?.name ?? id).join("、") : "尚未勾选"}
                </div>
                <p className="note" style={{ margin: 0 }}>只会拉起确实因额度保护停机的机器；你手动关机并确认保持停止的实例不会被误开。</p>
              </div>
            );
          }
          return (
            <div className="stack">
              <div className="inline">
                <Switch on={cur.enabled} label="开启每月 1 号自动开机" onChange={(v) => setEditing({ ...cur, enabled: v })} />
                <span>开启每月 1 号自动开机</span>
                <label className={`check ${cur.dry_run ? "on" : ""}`} style={{ marginLeft: "auto" }}>
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
                        onChange={() => setEditing({ ...cur, target_instance_ids: checked ? cur.target_instance_ids.filter((x) => x !== i.id) : [...cur.target_instance_ids, i.id] })}
                      />
                      {i.name}（{accName(i.account_id)}）
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

function RuleModal({ initial, accounts, instances, selfId, onClose, onSaved }: { initial?: T.QuotaProtectionRule; accounts: T.Account[]; instances: T.Instance[]; selfId: string | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [name, setName] = useState(initial?.name ?? "海外免费额度 98% 保护");
  const [accountId, setAccountId] = useState(initial?.account_id ?? accounts[0]?.id ?? "");
  const [scope, setScope] = useState<"overseas" | "mainland" | "swas">(initial?.scope ?? "overseas");
  const [sourceId, setSourceId] = useState(initial?.source_instance_id ?? "");
  const [kind, setKind] = useState<"percent" | "bytes">(initial?.threshold_kind ?? "percent");
  const [thresholdVal, setThresholdVal] = useState(initial ? (initial.threshold_kind === "bytes" ? String(Math.round((initial.threshold / GB) * 10) / 10) : String(initial.threshold)) : "98");
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
    if (!(numVal > 0) || (kind === "percent" && (numVal > 100 || !Number.isInteger(numVal)))) return setError(kind === "percent" ? "百分比请填 1–100 的整数。" : "阈值 GB 必须大于 0。");
    if (scope === "swas" && !sourceId) return setError("请选择要监控套餐流量的轻量服务器。");
    if (action === "stop_notify" && !targets.length) return setError("请至少勾选一台要关机的目标实例。");
    if (includesSelf && !confirmSelf) return setError("目标里包含运行面板自身的实例，请勾选底部确认框或取消选择该机器。");

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
      toast(initial ? "规则已更新" : "规则已创建");
      onSaved();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={initial ? "修改额度保护规则" : "添加额度保护规则"} onClose={onClose} wide>
      <form className="stack" onSubmit={submit} noValidate>
        <div>
          <label className="lbl" htmlFor="pr-name">规则名称</label>
          <input id="pr-name" type="text" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="grid g2">
          <div>
            <label className="lbl" htmlFor="pr-acc">所属账号</label>
            <select id="pr-acc" value={accountId} onChange={(e) => { setAccountId(e.target.value); setTargets([]); setSourceId(""); }}>
              {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          </div>
          <div>
            <label className="lbl" htmlFor="pr-scope">监控范围</label>
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
            <select id="pr-swas" value={sourceId} onChange={(e) => { setSourceId(e.target.value); if (e.target.value) setTargets([e.target.value]); }}>
              <option value="">请选择…</option>
              {swasInstances.map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
            </select>
          </div>
        )}
        <div className="grid g2">
          <div>
            <label className="lbl">触发阈值</label>
            <div className="inline">
              <SegToggle label="阈值类型" value={kind} onChange={(v) => { setKind(v); setThresholdVal(v === "percent" ? "98" : "195"); }} options={[["percent", "按百分比"], ["bytes", "按 GB"]]} />
              <input type="number" value={thresholdVal} onChange={(e) => setThresholdVal(e.target.value)} style={{ width: 96 }} min={1} max={kind === "percent" ? 100 : undefined} step={kind === "percent" ? 1 : 0.5} />
              <span>{kind === "percent" ? "%" : "GB"}</span>
            </div>
          </div>
          <div>
            <label className="lbl" htmlFor="pr-res">提前预留秒数（抵消关机耗时）</label>
            <input id="pr-res" type="number" value={reserve} onChange={(e) => setReserve(e.target.value)} min={0} max={3600} />
          </div>
        </div>
        <div>
          <label className="lbl">触发后的动作</label>
          <SegToggle label="动作" value={action} onChange={setAction} options={[["stop_notify", "自动关机并通知"], ["notify", "只发通知不关机"]]} />
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
                <label className="lbl">ECS 关机方式</label>
                <SegToggle label="关机方式" value={(stopMode || "keep_charging") as "keep_charging" | "stop_charging"} onChange={setStopMode} options={[["keep_charging", STOP_MODE.keep_charging[0]], ["stop_charging", STOP_MODE.stop_charging[0]]]} />
                <div className="field-help">{STOP_MODE[(stopMode || "keep_charging") as "keep_charging" | "stop_charging"][1]}</div>
              </div>
            )}
          </>
        )}
        <label className="rowline" style={{ cursor: "pointer" }}>
          <input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} style={{ width: "auto" }} />
          <span className="grow"><b>试运行模式</b><span className="sub" style={{ display: "block" }}>开启后只记录判定结果并发送通知，不会向阿里云发送真实关机指令。</span></span>
        </label>
        {scope !== "swas" && (
          <label className="rowline" style={{ cursor: "pointer" }}>
            <input type="checkbox" checked={allowUnverified} onChange={(e) => setAllowUnverified(e.target.checked)} style={{ width: "auto" }} />
            <span className="grow"><b>允许按本地窗口实时估算执行真实关机</b><span className="sub" style={{ display: "block" }}>阿里云 CDT 官方累计通常延迟约 1 小时且不返回精确截止时刻。勾选后，当采集程序覆盖完整且估算越线时立即关机，不等官方整点账单。</span></span>
          </label>
        )}
        {includesSelf && (
          <label className="rowline sim-card" style={{ cursor: "pointer", padding: "8px 12px", borderRadius: "var(--r-md)" }}>
            <input type="checkbox" checked={confirmSelf} onChange={(e) => setConfirmSelf(e.target.checked)} style={{ width: "auto" }} />
            <span className="grow"><b>我已知晓目标包含运行 Harbormaster 面板自身的实例，关机后面板将无法访问。</b></span>
          </label>
        )}
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存规则</button>
        </div>
      </form>
    </Modal>
  );
}

function ScheduleModal({ initial, accounts, instances, selfId, onClose, onSaved }: { initial?: T.PowerSchedule; accounts: T.Account[]; instances: T.Instance[]; selfId: string | null; onClose: () => void; onSaved: () => void }) {
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
    if (!/^\d{2}:\d{2}$/.test(timeOfDay)) return setError("时间请按 HH:MM 填写，例如 01:30。");
    if (!days.length) return setError("至少选择一天。");
    if (!targets.length) return setError("至少选择一台目标实例。");
    if (includesSelf && !confirmSelf) return setError("目标包含面板所在实例，请先勾选确认框。");
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
      toast(initial ? "定时计划已保存" : "定时计划已创建，将从下一个计划时间点生效");
      onSaved();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={initial ? "修改定时开关机" : "添加定时开关机"} onClose={onClose} wide>
      <form className="stack" onSubmit={submit} noValidate>
        <div className="grid g2">
          <div>
            <label className="lbl" htmlFor="sc-name">计划名称</label>
            <input id="sc-name" type="text" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <label className="lbl" htmlFor="sc-acc">所属账号</label>
            <select id="sc-acc" value={accountId} onChange={(e) => { setAccountId(e.target.value); setTargets([]); }}>
              {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          </div>
        </div>
        <div className="grid g2">
          <div>
            <label className="lbl">执行动作</label>
            <SegToggle label="动作" value={action} onChange={setAction} options={[["stop", "关机"], ["start", "开机"]]} />
          </div>
          <div>
            <label className="lbl" htmlFor="sc-time">每天时间（按面板时区）</label>
            <input id="sc-time" type="text" value={timeOfDay} onChange={(e) => setTimeOfDay(e.target.value)} placeholder="01:30" />
          </div>
        </div>
        {action === "stop" && (
          <div>
            <label className="lbl">关机方式</label>
            <SegToggle label="关机方式" value={(stopMode || "keep_charging") as "keep_charging" | "stop_charging"} onChange={setStopMode} options={[["keep_charging", STOP_MODE.keep_charging[0]], ["stop_charging", STOP_MODE.stop_charging[0]]]} />
            <div className="field-help">{STOP_MODE[(stopMode || "keep_charging") as "keep_charging" | "stop_charging"][1]}</div>
          </div>
        )}
        <div>
          <div className="inline" style={{ marginBottom: 4 }}>
            <span className="lbl" style={{ margin: 0 }}>重复星期</span>
            <button type="button" className="linkbtn sub" onClick={() => setDays([1, 2, 3, 4, 5, 6, 0])}>每天</button>
            <button type="button" className="linkbtn sub" onClick={() => setDays([1, 2, 3, 4, 5])}>工作日</button>
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
          <label className="lbl" htmlFor="sc-catch">服务重启补执行窗口（分钟）</label>
          <input id="sc-catch" type="number" value={catchUp} onChange={(e) => setCatchUp(e.target.value)} min={0} max={120} />
          <div className="field-help">仅对早已启用的计划在 hm-server 短暂重启后补跑一次；新建或刚重新打开的计划一律从下一个时间点生效。</div>
        </div>
        <label className="rowline" style={{ cursor: "pointer" }}>
          <input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} style={{ width: "auto" }} />
          <span className="grow"><b>试运行模式</b><span className="sub" style={{ display: "block" }}>到点只发通知，不真开关机。</span></span>
        </label>
        {includesSelf && (
          <label className="rowline sim-card" style={{ cursor: "pointer", padding: "8px 12px", borderRadius: "var(--r-md)" }}>
            <input type="checkbox" checked={confirmSelf} onChange={(e) => setConfirmSelf(e.target.checked)} style={{ width: "auto" }} />
            <span className="grow"><b>确认允许对面板自身所在实例执行定时关机。</b></span>
          </label>
        )}
        {error && <div className="field-error" role="alert">{error}</div>}
        <div className="actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存计划</button>
        </div>
      </form>
    </Modal>
  );
}

function SimulationModal({ accounts, instances, existing, onClose, onSaved }: { accounts: T.Account[]; instances: T.Instance[]; existing: T.QuotaSimulation[]; onClose: () => void; onSaved: () => void }) {
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
      toast("模拟用量已生效，保护评估器将在下一轮检查并记录结果");
      onSaved();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="模拟用量演练" onClose={onClose}>
      <p>临时注入一个假想的已用流量，用来检验额度保护规则与 Telegram 通知是否符合预期。<b>模拟状态下绝不会执行真实关机</b>，到期后自动恢复真实数据。</p>
      <form className="stack" onSubmit={submit} noValidate>
        <div>
          <label className="lbl" htmlFor="sim-acc">账号</label>
          <select id="sim-acc" value={accountId} onChange={(e) => { setAccountId(e.target.value); setSourceId(""); }}>
            {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
        </div>
        <div>
          <label className="lbl" htmlFor="sim-scope">范围</label>
          <select id="sim-scope" value={scope} onChange={(e) => setScope(e.target.value as typeof scope)}>
            <option value="overseas">非中国内地免费额度</option>
            <option value="mainland">中国内地免费额度</option>
            <option value="swas">轻量服务器套餐流量包</option>
          </select>
        </div>
        {scope === "swas" && (
          <div>
            <label className="lbl" htmlFor="sim-swas">轻量服务器</label>
            <select id="sim-swas" value={sourceId} onChange={(e) => setSourceId(e.target.value)}>
              <option value="">请选择…</option>
              {swas.map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
            </select>
          </div>
        )}
        <div className="grid g2">
          <div>
            <label className="lbl" htmlFor="sim-gb">模拟已用流量（GB）</label>
            <input id="sim-gb" type="number" value={gbStr} onChange={(e) => setGbStr(e.target.value)} min={0} step={0.5} />
          </div>
          <div>
            <label className="lbl" htmlFor="sim-min">持续时间</label>
            <select id="sim-min" value={mins} onChange={(e) => setMins(e.target.value)}>
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
          <button className="btn primary" disabled={busy}>{busy && <Spinner />} 开始模拟</button>
        </div>
      </form>
    </Modal>
  );
}

function DecisionsFold({ instName, accName, rules }: { instName: (id: string) => string; accName: (id: string) => string; rules: T.QuotaProtectionRule[] }) {
  const [open, setOpen] = useState(false);
  const [ruleId, setRuleId] = useState("");
  const res = useResource(() => (open ? api.quotaDecisions({ rule_id: ruleId || undefined, limit: 30 }) : Promise.resolve(null)), [open, ruleId]);
  const ruleName = (id: string) => rules.find((r) => r.id === id)?.name ?? id;
  return (
    <details className="card fold" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary><h2>额度保护判定记录</h2><span className="sub">每次状态切换、试运行或触发停机的判定依据</span><div className="spacer" /><span className="sub">展开</span></summary>
      {open && (
        <div style={{ marginTop: 12 }}>
          {rules.length > 0 && (
            <div className="filters" style={{ marginBottom: 10 }}>
              <select value={ruleId} onChange={(e) => setRuleId(e.target.value)} aria-label="按规则筛选">
                <option value="">全部规则</option>
                {rules.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
              </select>
            </div>
          )}
          <Async res={res}>
            {(p) =>
              !p || !p.items.length ? <EmptyState title="还没有判定记录" /> : (
                <div className="table-wrap">
                  <table>
                    <thead><tr><th>时间</th><th>规则</th><th>状态</th><th className="num">判定值 / 阈值</th><th>执行结果</th></tr></thead>
                    <tbody>
                      {p.items.map((d) => {
                        const [lv, lb] = DECISION_STATE[d.state];
                        return (
                          <tr key={d.id}>
                            <td>{stamp(d.evaluated_at)}</td>
                            <td><b>{ruleName(d.rule_id)}</b><div className="sub">{accName(d.account_id)} · {d.scope === "swas" ? "轻量" : SCOPE[d.scope as T.QuotaScope] ?? d.scope}</div></td>
                            <td><Badge level={lv}>{lb}</Badge>{d.simulation && <span className="tag" style={{ marginLeft: 4 }}>模拟</span>}</td>
                            <td className="num">{d.evaluated_bytes === null ? "—" : gb(d.evaluated_bytes)} / {d.threshold_bytes === null ? "—" : gb(d.threshold_bytes)}</td>
                            <td className="wrap">{d.targets.length ? d.targets.map((t) => `${instName(t.instance_id)}：${DECISION_OUTCOME[t.outcome] ?? t.outcome}`).join("；") : <span className="sub">{d.reasons.join("、") || "—"}</span>}</td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )
            }
          </Async>
        </div>
      )}
    </details>
  );
}

function JobsFold({ instName, accName, accounts }: { instName: (id: string) => string; accName: (id: string) => string; accounts: T.Account[] }) {
  const [open, setOpen] = useState(false);
  const [type, setType] = useState<T.JobType | "">("");
  const [status, setStatus] = useState<T.JobStatus | "">("");
  const [accountId, setAccountId] = useState("");
  const [cursor, setCursor] = useState<string>();
  const res = useResource(
    () => (open ? api.jobs({ type: type || undefined, status: status || undefined, account_id: accountId || undefined, cursor, limit: 30 }) : Promise.resolve(null)),
    [open, type, status, accountId, cursor],
  );
  return (
    <details className="card fold" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary><h2>后台任务记录</h2><span className="sub">开关机、刷新、权限测试、通知测试的执行历史</span><div className="spacer" /><span className="sub">展开</span></summary>
      {open && (
        <div style={{ marginTop: 12 }}>
          <div className="filters" style={{ marginBottom: 10 }}>
            <select value={type} onChange={(e) => { setType(e.target.value as T.JobType | ""); setCursor(undefined); }} aria-label="按任务类型筛选">
              <option value="">全部类型</option>
              {(Object.keys(JOB_TYPE) as T.JobType[]).map((k) => <option key={k} value={k}>{JOB_TYPE[k]}</option>)}
            </select>
            <select value={status} onChange={(e) => { setStatus(e.target.value as T.JobStatus | ""); setCursor(undefined); }} aria-label="按状态筛选">
              <option value="">全部状态</option>
              {(Object.keys(JOB_STATUS) as T.JobStatus[]).map((k) => <option key={k} value={k}>{JOB_STATUS[k][1]}</option>)}
            </select>
            <select value={accountId} onChange={(e) => { setAccountId(e.target.value); setCursor(undefined); }} aria-label="按账号筛选">
              <option value="">全部账号</option>
              {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          </div>
          <Async res={res}>
            {(p) =>
              !p || !p.items.length ? <EmptyState title="没有符合条件的任务记录" /> : (
                <>
                  <div className="table-wrap">
                    <table>
                      <thead><tr><th>创建时间</th><th>任务</th><th>来源</th><th>状态</th><th>耗时 / 说明</th></tr></thead>
                      <tbody>
                        {p.items.map((j) => {
                          const [lv, lb] = JOB_STATUS[j.status];
                          const durMs = j.started_at && j.finished_at ? Math.max(0, Date.parse(j.finished_at) - Date.parse(j.started_at)) : null;
                          return (
                            <tr key={j.id}>
                              <td>{stamp(j.created_at)}</td>
                              <td>
                                <b>{JOB_TYPE[j.type] ?? j.type}</b>
                                <div className="sub">
                                  {j.instance_id ? instName(j.instance_id) : j.account_ids?.length ? j.account_ids.map(accName).join("、") : "系统"}
                                </div>
                              </td>
                              <td>{j.trigger ? JOB_TRIGGER[j.trigger] ?? j.trigger : "—"}</td>
                              <td><Badge level={lv}>{lb}</Badge></td>
                              <td className="wrap">
                                {j.error ? <span className="field-error">{j.error.code}</span> : durMs !== null ? <span className="sub">{(durMs / 1000).toFixed(1)} 秒</span> : <span className="sub">—</span>}
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                  {(cursor || p.next_cursor) && (
                    <div className="inline" style={{ marginTop: 10 }}>
                      {cursor && <button className="btn sm" onClick={() => setCursor(undefined)}>回到最新</button>}
                      {p.next_cursor && <button className="btn sm" onClick={() => setCursor(p.next_cursor!)}>加载更早任务</button>}
                    </div>
                  )}
                </>
              )
            }
          </Async>
        </div>
      )}
    </details>
  );
}
