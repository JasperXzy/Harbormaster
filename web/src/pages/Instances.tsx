// 实例：表格（手机上是卡片）、分组与标签筛选、详情弹窗（开关机控制、临时豁免、流量曲线、本地备注标签）、采集程序安装引导。
import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { waitForJob } from "../api/jobs";
import { AGENT, CHARGE, errorText, INSTANCE_EVENT, INSTANCE_STATUS, KIND, protectionOf, regionName, RENEWAL, RULE, SCOPE, STOP_MODE, SUBJECT_KIND } from "../api/labels";
import type { Account, AgentInstallCommand, Instance, InstanceEvent, SystemEvent, SystemInfo } from "../api/types";
import { LineChart } from "../components/charts/LineChart";
import { Async, Badge, Banner, Card, Confirm, CopyButton, EmptyState, ErrorState, errMsg, Fresh, Modal, SegToggle, Skeleton, Spinner, Switch, useToast } from "../components/ui";
import { useShared } from "../lib/data";
import { ago, duration, gb, gbValue, num, rate, stamp, time, ymd } from "../lib/format";
import { Link } from "../lib/router";
import { useNow, useResource } from "../lib/useResource";

const canInstall = (i: Instance) => i.agent.status === "not_installed";

function Rate({ i }: { i: Instance }) {
  if (i.agent.status === "offline") return <span className="sub">掉线，没有数据</span>;
  if (!i.realtime) {
    const stream = i.cloud_network?.streams?.[0];
    if (stream && (stream.egress.value != null || stream.ingress.value != null)) {
      const tx = (stream.egress.value ?? 0) / 8;
      const rx = (stream.ingress.value ?? 0) / 8;
      return (
        <span className="num" title="来自云监控（约晚 1–2 分钟）">
          <span>↑ {rate(tx)}</span> <span className="sub">↓ {rate(rx)}</span>
        </span>
      );
    }
    return <span className="sub">—</span>;
  }
  return (
    <span className="num">
      <span title="发出">↑ {rate(i.realtime.tx_bytes_per_second)}</span>{" "}
      <span className="sub" title="收到">↓ {rate(i.realtime.rx_bytes_per_second)}</span>
    </span>
  );
}

function specNote(i: Instance): string {
  const c = i.network_capacity;
  if (!c?.public_outbound_exceeds_spec || c.tx_kbps === null || c.public_outbound_mbps === null) return "";
  return `公网带宽是 ${c.public_outbound_mbps} Mbps，但这台机器的规格最多只能跑约 ${Math.round(c.tx_kbps / 1000)} Mbps，多出来的带宽用不上。`;
}

function Billing({ i }: { i: Instance }) {
  if ((i.billing_subject_ids?.length ?? 0) > 1) return <span className="sub">多个公网出口 · 分别计费</span>;
  if (!i.billing_subject_kind) return <span className="sub">{i.public_ips.length ? "公网计费类型待确认" : "未发现公网出口"}</span>;
  return (
    <div className="cell-stack">
      <span>{i.bandwidth_package_name ? `共享带宽 ${i.bandwidth_package_name} · ${SUBJECT_KIND[i.billing_subject_kind].split(" · ")[1]}` : SUBJECT_KIND[i.billing_subject_kind]}</span>
      <span className="sub">{i.counts_toward_cdt === null ? "是否计入免费额度待确认" : i.counts_toward_cdt && i.cdt_scope ? `计入${SCOPE[i.cdt_scope]}免费额度` : "不计入免费额度"}{i.bandwidth_mbps ? ` · ${i.bandwidth_mbps} Mbps` : ""}{i.expires_at ? ` · ${ymd(i.expires_at)} 到期` : ""}</span>
      {i.network_capacity?.public_outbound_exceeds_spec && <span><Badge level="warning" title={specNote(i)}>规格带宽不足</Badge></span>}
    </div>
  );
}

function AgentCell({ i, onInstall }: { i: Instance; onInstall: () => void }) {
  const [al, at] = AGENT[i.agent.status];
  const [pl, pt, pd] = protectionOf(i);
  return (
    <div className="cell-stack" style={{ justifyItems: "start" }}>
      <div className="inline" style={{ gap: 4 }}>
        {i.realtime_protection === "not_applicable" ? <Badge level={al}>{at}</Badge> : <Badge level={pl} title={pd}>{pt}</Badge>}
        {i.agent.update_available && <span className="tag" title="采集程序有新版本，点进实例详情可生成升级命令">可升级</span>}
      </div>
      {canInstall(i) && (
        <button className="linkbtn sub" onClick={(e) => { e.stopPropagation(); onInstall(); }}>
          {i.realtime_protection === "unprotected" ? "安装采集程序" : "也可以安装"}
        </button>
      )}
    </div>
  );
}

export function Instances() {
  const toast = useToast();
  const now = useNow(15_000);
  const { accounts, instances, reloadAll } = useShared();
  const [account, setAccount] = useState("");
  const [group, setGroup] = useState("");
  const [tag, setTag] = useState("");
  const [only, setOnly] = useState(false);
  const [detail, setDetail] = useState<string>();
  const [install, setInstall] = useState<string>();
  const [creatingECS, setCreatingECS] = useState(false);
  const [answering, setAnswering] = useState<string>();
  const sysInfoRes = useResource(api.systemInfo, []);

  if (instances.error) return <div className="card"><ErrorState error={instances.error} onRetry={instances.reload} /></div>;
  if (!instances.data || !accounts.data) return <div className="card"><Skeleton lines={6} height={18} /></div>;
  const all = instances.data.items, accs = accounts.data;
  const accName = (id: string) => accs.find((a) => a.id === id)?.name ?? "—";
  const groups = [...new Set(all.map((i) => i.group).filter((g): g is string => !!g))].sort();
  const tags = [...new Set(all.flatMap((i) => i.tags ?? []))].sort();
  const needs = (i: Instance) =>
    Boolean(i.pending_intent_confirmation) ||
    i.realtime_protection === "worst_case" ||
    (i.realtime_protection === "unprotected" && i.status === "running" && i.agent.status === "not_installed");
  const rows = all.filter(
    (i) =>
      (!account || i.account_id === account) &&
      (!group || i.group === group) &&
      (!tag || (i.tags ?? []).includes(tag)) &&
      (!only || needs(i)),
  );
  const cur = all.find((i) => i.id === detail), inst = all.find((i) => i.id === install);
  const pendingConfirms = all.filter((i) => i.pending_intent_confirmation);

  const answerIntent = async (target: Instance, decision: "keep_stopped" | "resume") => {
    setAnswering(target.id);
    try {
      const r = await api.confirmInstanceIntent(target.id, decision);
      if (r.job_id) {
        toast(`已发送开机请求，正在恢复 ${target.name}…`);
        await waitForJob(r.job_id);
      } else {
        toast(`已将 ${target.name} 设为保持停止`);
      }
      reloadAll();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setAnswering(undefined);
    }
  };

  if (!all.length)
    return (
      <div className="card">
        {accs.length ? (
          accs.some((a) => a.enabled && a.sync_status === "never_synced") ? (
            <EmptyState title="正在第一次扫描">刚添加的账号正在读取名下的机器，通常一两分钟内完成，这里会自动出现。</EmptyState>
          ) : (
            <EmptyState
              title="还没有发现实例"
              action={accs.some((a) => a.enabled) ? <button className="btn primary" onClick={() => setCreatingECS(true)}>＋ 创建云服务器 (ECS)</button> : undefined}
            >
              这些账号在所选地域里没有云服务器或轻量服务器。如果机器在别的地域，请到设置页为账号勾选对应地域。
            </EmptyState>
          )
        ) : (
          <EmptyState title="还没有添加阿里云账号" action={<Link className="btn primary" to="/settings">去添加账号</Link>}>添加账号后会自动发现它名下的机器。</EmptyState>
        )}
        {creatingECS && <CreateECSModal accounts={accs} sysInfo={sysInfoRes.data} onClose={() => setCreatingECS(false)} onCreated={() => { setCreatingECS(false); reloadAll(); }} />}
      </div>
    );

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
          <b>发现 {i.name} 在面板外被关机。</b> 请确认这是你有意关机，还是需要恢复运行。
        </Banner>
      ))}

      <Card
        title={rows.length === all.length ? `${all.length} 台` : `${rows.length} / ${all.length} 台`}
        right={
          <div className="filters">
            <Fresh at={instances.data.updated_at} kind="resources" name="资源信息" quiet />
            <select value={account} onChange={(e) => setAccount(e.target.value)} aria-label="按账号筛选">
              <option value="">全部账号</option>
              {accs.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
            {groups.length > 0 && (
              <select value={group} onChange={(e) => setGroup(e.target.value)} aria-label="按分组筛选">
                <option value="">全部分组</option>
                {groups.map((g) => <option key={g} value={g}>{g}</option>)}
              </select>
            )}
            {tags.length > 0 && (
              <select value={tag} onChange={(e) => setTag(e.target.value)} aria-label="按标签筛选">
                <option value="">全部标签</option>
                {tags.map((t) => <option key={t} value={t}>{t}</option>)}
              </select>
            )}
            <label className={`check ${only ? "on" : ""}`}><input type="checkbox" checked={only} onChange={(e) => setOnly(e.target.checked)} />只看需要处理的</label>
            <button className="btn sm primary" onClick={() => setCreatingECS(true)}>＋ 创建 ECS</button>
          </div>
        }
      >
        {!rows.length ? (
          <EmptyState title="没有符合条件的实例" action={<button className="btn" onClick={() => { setAccount(""); setGroup(""); setTag(""); setOnly(false); }}>清除筛选</button>} />
        ) : (
          <>
            <div className="table-wrap only-wide">
              <table>
                <thead>
                  <tr><th>实例</th><th>状态</th><th>公网 IP</th><th>公网怎么计费</th><th>当前速度</th><th>实时保护</th></tr>
                </thead>
                <tbody>
                  {rows.map((i) => {
                    const [sl, st] = INSTANCE_STATUS[i.status];
                    const exempt = i.quota_exemption_until && Date.parse(i.quota_exemption_until) > now;
                    return (
                      <tr key={i.id} className="clickable" onClick={() => setDetail(i.id)}>
                        <td>
                          <div className="cell-stack" style={{ justifyItems: "start" }}>
                            <div className="inline" style={{ gap: 6 }}>
                              <button className="linkbtn" style={{ fontWeight: 650, color: "var(--text)", textDecoration: "none" }} onClick={(e) => { e.stopPropagation(); setDetail(i.id); }}>{i.name}</button>
                              {i.is_server_self && <span className="tag" title="运行 Harbormaster 面板自身的机器">面板自身</span>}
                              {i.group && <span className="tag">{i.group}</span>}
                              {(i.tags ?? []).slice(0, 2).map((t) => <span className="tag" key={t}>#{t}</span>)}
                            </div>
                            <span className="sub">{accName(i.account_id)} · {regionName(i.region_id)}{i.kind === "swas" ? " · 轻量" : ""}{i.note ? ` · ${i.note.split("\n")[0].slice(0, 24)}` : ""}</span>
                          </div>
                        </td>
                        <td>
                          <div className="cell-stack" style={{ justifyItems: "start" }}>
                            <Badge level={sl}>{st}</Badge>
                            {exempt && <span className="sub">豁免至 {stamp(i.quota_exemption_until!)}</span>}
                            {i.pending_intent_confirmation && <Badge level="warning">待确认关机</Badge>}
                          </div>
                        </td>
                        <td className="mono">{i.public_ips.map((p) => <div key={p.address}>{p.address}{p.type === "eip" ? "（弹性）" : ""}</div>)}{!i.public_ips.length && "—"}</td>
                        <td><Billing i={i} /></td>
                        <td><Rate i={i} /></td>
                        <td><AgentCell i={i} onInstall={() => setInstall(i.id)} /></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div className="icards">
              {rows.map((i) => {
                const [sl, st] = INSTANCE_STATUS[i.status];
                return (
                  <div key={i.id} className="icard" role="button" tabIndex={0} onClick={() => setDetail(i.id)} onKeyDown={(e) => e.key === "Enter" && setDetail(i.id)}>
                    <div className="hd">
                      <b>{i.name}</b>
                      {i.kind === "swas" && <span className="tag">轻量</span>}
                      {i.is_server_self && <span className="tag">面板自身</span>}
                      {i.group && <span className="tag">{i.group}</span>}
                      <div className="spacer" />
                      <Badge level={sl}>{st}</Badge>
                    </div>
                    <div className="sub">{accName(i.account_id)} · {regionName(i.region_id)} · <span className="mono">{i.public_ips[0]?.address ?? "没有公网 IP"}</span></div>
                    <Billing i={i} />
                    <div className="hd"><AgentCell i={i} onInstall={() => setInstall(i.id)} /><div className="spacer" /><Rate i={i} /></div>
                  </div>
                );
              })}
            </div>
          </>
        )}
      </Card>
      <Events />
      <SystemEventsFold instances={all} accounts={accs} />
      {cur && <InstanceDetail i={cur} account={accName(cur.account_id)} sysInfo={sysInfoRes.data} onClose={() => setDetail(undefined)} onInstall={() => { setDetail(undefined); setInstall(cur.id); }} />}
      {inst && <AgentInstall i={inst} onClose={() => setInstall(undefined)} />}
      {creatingECS && <CreateECSModal accounts={accs} sysInfo={sysInfoRes.data} onClose={() => setCreatingECS(false)} onCreated={() => { setCreatingECS(false); reloadAll(); }} />}
    </>
  );
}

type DetailTab = "overview" | "traffic" | "meta";

function InstanceDetail({ i, account, sysInfo, onClose, onInstall }: { i: Instance; account: string; sysInfo?: SystemInfo; onClose: () => void; onInstall: () => void }) {
  const toast = useToast();
  const now = useNow();
  const [tab, setTab] = useState<DetailTab>("overview");
  const [revoke, setRevoke] = useState(false);
  const [powerConfirm, setPowerConfirm] = useState<"start" | "stop" | "reboot" | null>(null);
  const [stopMode, setStopMode] = useState<"keep_charging" | "stop_charging">("keep_charging");
  const [powerBusy, setPowerBusy] = useState(false);
  const [showChangeEIP, setShowChangeEIP] = useState(false);
  const [showRelease, setShowRelease] = useState(false);
  const finRes = useResource(() => api.instanceFinance(i.id), [i.id]);
  const { instances, reloadAll } = useShared();
  const [sl, st] = INSTANCE_STATUS[i.status];
  const [al, at] = AGENT[i.agent.status];
  const [pl, pt, pd] = protectionOf(i);
  const exemptActive = Boolean(i.quota_exemption_until && Date.parse(i.quota_exemption_until) > now);

  const runPower = async (action: "start" | "stop" | "reboot") => {
    setPowerBusy(true);
    try {
      const mode = action === "stop" && i.kind === "ecs" ? (i.supports_stop_charging ? stopMode : "keep_charging") : undefined;
      const { job_id } = await api.controlInstancePower(i.id, action, mode);
      setPowerConfirm(null);
      toast(`已提交${action === "start" ? "开机" : action === "stop" ? "关机" : "重启"}任务，等待阿里云确认…`);
      const job = await waitForJob(job_id);
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast(`${i.name} ${action === "start" ? "已开机" : action === "stop" ? "已关机" : "已重启"}`);
      reloadAll();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setPowerBusy(false);
    }
  };

  const setExempt = async (secs: number | null) => {
    try {
      if (secs === null) {
        await api.clearProtectionExemption(i.id);
        toast("已取消临时豁免");
      } else {
        await api.setProtectionExemption(i.id, secs);
        toast("已设置临时豁免自动关机");
      }
      instances.reload();
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  if (revoke)
    return (
      <Confirm
        title={`撤销 ${i.name} 的采集程序凭证？`} okText="撤销" danger onClose={() => setRevoke(false)}
        body="撤销后，这台机器上的采集程序立刻无法再上报，这台机器会变回“未安装”。想恢复需要重新生成安装命令并在机器上再执行一次。"
        onOk={async () => {
          await api.revokeAgentToken(i.id);
          instances.reload();
          toast("已撤销");
          onClose();
        }}
      />
    );

  if (powerConfirm) {
    const label = powerConfirm === "start" ? "开机" : powerConfirm === "stop" ? "关机" : "重启";
    return (
      <Modal title={`确认将 ${i.name} ${label}？`} onClose={() => setPowerConfirm(null)}>
        {i.is_server_self && powerConfirm !== "start" && (
          <div className="banner critical" style={{ marginBottom: 12 }}>
            <div className="body"><b>这是运行 Harbormaster 面板自身的实例！</b> 关机或重启期间你将无法打开本面板。</div>
          </div>
        )}
        {powerConfirm === "stop" && i.supports_stop_charging ? (
          <div className="stack" style={{ marginBottom: 12 }}>
            <SegToggle label="关机方式" value={stopMode} onChange={setStopMode} options={[["keep_charging", STOP_MODE.keep_charging[0]], ["stop_charging", STOP_MODE.stop_charging[0]]]} />
            <div className="field-help">{STOP_MODE[stopMode][1]}</div>
          </div>
        ) : (
          <p>{powerConfirm === "stop" ? "这台机器将执行普通停机（包年包月或轻量服务器不支持节省停机）。" : `将向阿里云发送${label}指令，并等待实例状态确认。`}</p>
        )}
        <div className="actions">
          <button className="btn" onClick={() => setPowerConfirm(null)}>取消</button>
          <button className={`btn ${powerConfirm === "start" ? "primary" : "danger"}`} disabled={powerBusy} onClick={() => runPower(powerConfirm)}>
            {powerBusy && <Spinner />} 确认{label}
          </button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title={i.name} onClose={onClose} wide>
      <div className="inline" style={{ marginBottom: 14 }}>
        <SegToggle label="详情分页" value={tab} onChange={setTab} options={[["overview", "概览与控制"], ["traffic", "流量曲线"], ["meta", "备注与分组"]]} />
        <div className="spacer" />
        {i.kind === "ecs" && (
          <button className="btn sm" onClick={() => setShowChangeEIP(true)}>更换弹性 IP</button>
        )}
        {i.kind === "ecs" && i.status === "stopped" && (i.charge_type === "pay_as_you_go" || i.charge_type === "spot") && (
          <button className="btn sm danger" onClick={() => setShowRelease(true)}>释放实例</button>
        )}
        {i.status === "stopped" && <button className="btn sm primary" disabled={powerBusy} onClick={() => setPowerConfirm("start")}>{powerBusy && <Spinner />} 开机</button>}
        {i.status === "running" && (
          <>
            <button className="btn sm" disabled={powerBusy} onClick={() => setPowerConfirm("reboot")}>重启</button>
            <button className="btn sm danger" disabled={powerBusy} onClick={() => setPowerConfirm("stop")}>关机</button>
          </>
        )}
      </div>

      {tab === "overview" && (
        <>
          <dl className="kv">
            <dt>状态</dt>
            <dd>
              <Badge level={sl}>{st}</Badge>
              {i.desired_state && <span className="sub"> · 期望状态：{i.desired_state === "running" ? "运行中" : "已停止"}</span>}
              {i.is_server_self && <span className="tag" style={{ marginLeft: 6 }}>面板自身</span>}
            </dd>
            <dt>账号 / 地域</dt><dd>{account} · {regionName(i.region_id)}{i.zone_id ? `（${i.zone_id}）` : ""}</dd>
            <dt>类型</dt><dd>{KIND[i.kind]} · {CHARGE[i.charge_type]}{i.expires_at ? ` · ${ymd(i.expires_at)} 到期` : ""}{i.renewal && i.renewal.status !== "not_applicable" ? ` · ${RENEWAL[i.renewal.status][1]}` : ""}</dd>
            <dt>规格</dt><dd>{i.spec}</dd>
            {i.cpu?.usage.value != null && (
              <>
                <dt>CPU 负载</dt>
                <dd>
                  {num(i.cpu.usage.value, 1)}%
                  {i.cpu.credit_balance?.value != null && <span className="sub"> · 突发性能积分剩余 {num(i.cpu.credit_balance.value, 1)}</span>}
                </dd>
              </>
            )}
            <dt>阿里云实例 ID</dt><dd className="mono">{i.cloud_instance_id}</dd>
            <dt>公网 IP</dt><dd className="mono">{i.public_ips.map((p) => `${p.address}${p.type === "eip" ? "（弹性公网 IP）" : ""}`).join("、") || "—"}</dd>
            {!!i.ipv6?.addresses.length && <><dt>IPv6</dt><dd className="mono">{i.ipv6.addresses.join("、")}{i.ipv6.status === "stale" ? "（上次查到的，这次没查成功）" : ""}</dd></>}
            <dt>公网怎么计费</dt><dd>{(i.billing_subject_ids?.length ?? 0) > 1 ? "多个公网出口，按各自规则计费，详见流量页" : i.billing_subject_kind ? SUBJECT_KIND[i.billing_subject_kind] : i.public_ips.length ? "公网计费类型待确认" : "未发现公网出口"}{i.bandwidth_package_name ? `（${i.bandwidth_package_name}）` : ""}</dd>
            <dt>计量规则</dt><dd>{i.metering_rule ? RULE[i.metering_rule] : "—"}{i.counts_toward_cdt === null ? " · 是否计入免费额度待确认" : i.counts_toward_cdt && i.cdt_scope ? ` · 计入${SCOPE[i.cdt_scope]}免费额度` : " · 不计入免费额度"}</dd>
            {i.kind === "swas" && (
              <>
                <dt>套餐流量包</dt>
                <dd>
                  {i.swas_traffic_package?.usage ? `${gb(i.swas_traffic_package.usage.used_bytes)} / ${gb(i.swas_traffic_package.usage.total_bytes, 0)}（剩余 ${gb(i.swas_traffic_package.usage.remaining_bytes)}）` : i.billing_subject_kind === "swas_no_traffic_package" ? "无流量包（这个套餐不按流量计费）" : "待确认"}
                </dd>
              </>
            )}
            <dt>带宽上限</dt><dd>{i.bandwidth_mbps ? `${i.bandwidth_mbps} Mbps` : "—"}{specNote(i) && <div style={{ marginTop: 4 }}><Badge level="warning">规格带宽不足</Badge><div className="sub" style={{ marginTop: 4 }}>{specNote(i)}</div></div>}</dd>
            <dt>本月账单</dt>
            <dd>
              {finRes.data ? (
                finRes.data.bill.data?.items.length ? (
                  <span>
                    {finRes.data.bill.data.items.map((it, k) => (
                      <span key={k} style={{ marginRight: 10 }}>
                        {it.product_name || it.product_code}: <b>{it.currency} {it.after_discount_amount}</b>
                      </span>
                    ))}
                  </span>
                ) : (
                  <span className="sub">本月（{finRes.data.billing_cycle}）暂无独立扣费行</span>
                )
              ) : (
                <span className="sub">读取中…</span>
              )}
            </dd>
            <dt>临时豁免关机</dt>
            <dd>
              {exemptActive ? (
                <div className="inline">
                  <span>豁免至 {stamp(i.quota_exemption_until!)}</span>
                  <button className="btn sm" onClick={() => setExempt(null)}>取消豁免</button>
                </div>
              ) : (
                <div className="inline">
                  <span className="sub">未豁免</span>
                  <button className="btn sm" onClick={() => setExempt(3600)}>豁免 1 小时</button>
                  <button className="btn sm" onClick={() => setExempt(6 * 3600)}>6 小时</button>
                  <button className="btn sm" onClick={() => setExempt(24 * 3600)}>24 小时</button>
                </div>
              )}
            </dd>
            {(i.group || (i.tags?.length ?? 0) > 0 || i.note) && (
              <>
                <dt>本地备注</dt>
                <dd>
                  {i.group && <span className="tag" style={{ marginRight: 4 }}>{i.group}</span>}
                  {(i.tags ?? []).map((t) => <span className="tag" key={t} style={{ marginRight: 4 }}>#{t}</span>)}
                  {i.note && <div className="sub" style={{ whiteSpace: "pre-wrap", marginTop: 4 }}>{i.note}</div>}
                </dd>
              </>
            )}
            <dt>资源信息更新于</dt><dd>{stamp(i.updated_at)}</dd>
          </dl>

          <h3 style={{ fontSize: "var(--fs-md)", margin: "18px 0 8px" }}>采集程序（hm-agent）</h3>
          <dl className="kv">
            <dt>实时保护</dt><dd><Badge level={pl}>{pt}</Badge><div className="sub" style={{ marginTop: 4 }}>{pd}</div></dd>
            <dt>采集程序</dt>
            <dd>
              <Badge level={al}>{at}</Badge>{i.agent.version ? ` 版本 ${i.agent.version}` : ""}
              {i.agent.update_available && <span className="sub"> · 有新版本可升级（重新执行安装命令即可平滑覆盖，不丢数据）</span>}
            </dd>
            {i.agent.last_report_at && <><dt>最后上报</dt><dd>{stamp(i.agent.last_report_at)}（{ago(i.agent.last_report_at, now)}）</dd></>}
            {i.agent.offline_since && <><dt>已掉线</dt><dd>{duration((now - new Date(i.agent.offline_since).getTime()) / 1000)}</dd></>}
            {i.agent.worst_case && <><dt>推算依据</dt><dd>{i.agent.worst_case.basis === "recent_peak" ? "掉线前 10 分钟的最高速度" : "带宽上限"} {rate(i.agent.worst_case.rate_bytes_per_second)}</dd></>}
            {i.realtime && <><dt>当前速度</dt><dd>发出 {rate(i.realtime.tx_bytes_per_second)} · 收到 {rate(i.realtime.rx_bytes_per_second)}</dd></>}
          </dl>
          {i.agent.status === "offline" && (
            <p className="note">排查方法：登录这台机器，执行 <span className="mono">systemctl status hm-agent</span> 看它是否在运行；再确认机器能访问面板的地址。</p>
          )}
          <div className="actions">
            {i.agent.status !== "not_installed" && <button className="btn danger" onClick={() => setRevoke(true)}>撤销凭证</button>}
            {i.agent.update_available && <button className="btn" onClick={onInstall}>获取升级命令</button>}
            {canInstall(i) && <button className="btn primary" onClick={onInstall}>安装采集程序</button>}
            <button className="btn" onClick={onClose}>关闭</button>
          </div>
        </>
      )}

      {tab === "traffic" && <InstanceTrafficTab i={i} onClose={onClose} />}
      {tab === "meta" && <InstanceMetaTab i={i} onClose={onClose} />}
      {showChangeEIP && (
        <ChangeEIPModal
          i={i}
          enabled={Boolean(sysInfo?.eip_manage_enabled)}
          onClose={() => setShowChangeEIP(false)}
          onDone={() => { setShowChangeEIP(false); reloadAll(); }}
        />
      )}
      {showRelease && (
        <ReleaseECSModal
          i={i}
          onClose={() => setShowRelease(false)}
          onReleased={() => { setShowRelease(false); onClose(); reloadAll(); }}
        />
      )}
    </Modal>
  );
}

function InstanceTrafficTab({ i, onClose }: { i: Instance; onClose: () => void }) {
  const [range, setRange] = useState<"24h" | "7d">("24h");
  const hasAgent = i.agent.status !== "not_installed";
  const res = useResource(async () => {
    const to = new Date().toISOString();
    const from = new Date(Date.now() - (range === "24h" ? 24 : 7 * 24) * 3600_000).toISOString();
    if (hasAgent) {
      const hourly = await api.agentHourlyHistory(i.id, { from, to, limit: 200 });
      if (hourly.items.length >= 2) {
        return {
          mode: "hourly" as const,
          unit: "GB",
          labels: hourly.items.map((b) => (range === "24h" ? time(b.hour_start) : stamp(b.hour_start))),
          tx: hourly.items.map((b) => gbValue(b.tx_bytes)),
          rx: hourly.items.map((b) => gbValue(b.rx_bytes)),
          note: "数据来自 hm-agent 本地小时归档（跨整点样本按时长分摊，仅用于历史图表展示）。",
        };
      }
      const raw = await api.agentNetworkHistory(i.id, { from, to, limit: 300 });
      const pts = [...raw.items].reverse().filter((s) => s.interval);
      return {
        mode: "raw" as const,
        unit: "Mbps",
        labels: pts.map((s) => (range === "24h" ? time(s.sampled_at) : stamp(s.sampled_at))),
        tx: pts.map((s) => ((s.interval!.tx_bytes_per_second * 8) / 1e6)),
        rx: pts.map((s) => ((s.interval!.rx_bytes_per_second * 8) / 1e6)),
        note: "数据来自 hm-agent 实时采集样本。",
      };
    }
    const cloud = await api.cloudNetworkHistory(i.id, { from, to, limit: 400 });
    const byTime = new Map<string, { tx: number | null; rx: number | null }>();
    for (const s of cloud.items) {
      const entry = byTime.get(s.sampled_at) ?? { tx: null, rx: null };
      if (s.direction === "egress") entry.tx = s.value / 1e6;
      else entry.rx = s.value / 1e6;
      byTime.set(s.sampled_at, entry);
    }
    const sorted = [...byTime.entries()].sort((a, b) => a[0].localeCompare(b[0]));
    return {
      mode: "cloud" as const,
      unit: "Mbps",
      labels: sorted.map(([t]) => (range === "24h" ? time(t) : stamp(t))),
      tx: sorted.map(([, v]) => v.tx),
      rx: sorted.map(([, v]) => v.rx),
      note: "这台机器尚未安装采集程序，当前展示阿里云云监控的分钟级带宽曲线（约延迟 1–2 分钟）。",
    };
  }, [i.id, range, hasAgent]);

  return (
    <div className="stack">
      <div className="inline">
        <SegToggle label="时间范围" value={range} onChange={setRange} options={[["24h", "过去 24 小时"], ["7d", "过去 7 天"]]} />
      </div>
      <Async res={res} lines={6}>
        {(d) =>
          d.labels.length < 2 ? (
            <EmptyState title="还没有足够的历史点画图">刚启动或刚装上采集程序时，稍等几分钟就会积累出曲线。</EmptyState>
          ) : (
            <>
              <div className="legend">
                <span><i className="line" style={{ background: "var(--series-1)" }} />发出（{d.unit}）</span>
                <span><i className="line" style={{ background: "var(--series-3)" }} />收到（{d.unit}）</span>
              </div>
              <LineChart
                ariaLabel={`${i.name} 公网收发历史曲线`}
                labels={d.labels}
                unit={d.unit}
                digits={2}
                series={[
                  { name: "发出", color: "var(--series-1)", data: d.tx },
                  { name: "收到", color: "var(--series-3)", data: d.rx },
                ]}
              />
              <p className="note" style={{ margin: 0 }}>{d.note}</p>
            </>
          )
        }
      </Async>
      <div className="actions"><button className="btn" onClick={onClose}>关闭</button></div>
    </div>
  );
}

function InstanceMetaTab({ i, onClose }: { i: Instance; onClose: () => void }) {
  const toast = useToast();
  const { instances } = useShared();
  const [group, setGroup] = useState(i.group ?? "");
  const [tagsStr, setTagsStr] = useState((i.tags ?? []).join(", "));
  const [note, setNote] = useState(i.note ?? "");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const tags = tagsStr.split(/[,，]+/).map((t) => t.trim()).filter(Boolean);
      const saved = await api.updateInstanceMetadata(i.id, { group: group.trim(), tags, note });
      instances.mutate((l) => ({
        ...l,
        items: l.items.map((x) => (x.id === i.id ? { ...x, group: saved.group, tags: saved.tags, note: saved.note } : x)),
      }));
      toast("备注与分组已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      <div>
        <label className="lbl" htmlFor="im-group">分组（单选归类，例如：出口节点、核心业务）</label>
        <input id="im-group" type="text" value={group} onChange={(e) => setGroup(e.target.value)} maxLength={40} placeholder="留空表示不分组" />
      </div>
      <div>
        <label className="lbl" htmlFor="im-tags">标签（多个标签用逗号分隔，最多 10 个）</label>
        <input id="im-tags" type="text" value={tagsStr} onChange={(e) => setTagsStr(e.target.value)} placeholder="例如：主力, 香港, 晚高峰" />
      </div>
      <div>
        <label className="lbl" htmlFor="im-note">多行备注（记录用途、端口说明等）</label>
        <textarea id="im-note" rows={4} value={note} onChange={(e) => setNote(e.target.value)} maxLength={1000} style={{ width: "100%" }} placeholder="只保存在 Harbormaster 本地数据库，不会写回阿里云。" />
      </div>
      <p className="note" style={{ margin: 0 }}>分组、标签与备注仅存放在本面板数据库中，绝不会调用阿里云标签接口修改云上资源。</p>
      <div className="actions">
        <button type="button" className="btn" onClick={onClose}>关闭</button>
        <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存</button>
      </div>
    </form>
  );
}

/** 采集程序安装引导：生成命令 -> 复制 -> 等待上线 */
function AgentInstall({ i, onClose }: { i: Instance; onClose: () => void }) {
  const now = useNow();
  const [cmd, setCmd] = useState<AgentInstallCommand>();
  const [error, setError] = useState<string>();
  const [startedAt, setStartedAt] = useState(Date.now());
  const online = i.agent.status === "online" && !i.agent.update_available;

  const generate = () => {
    setCmd(undefined);
    setError(undefined);
    api.agentInstallCommand(i.id).then((c) => { setCmd(c); setStartedAt(Date.now()); }, (e) => setError(errMsg(e)));
  };
  useEffect(generate, [i.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const left = cmd ? Math.max(0, (new Date(cmd.expires_at).getTime() - now) / 1000) : 0;
  const expired = !!cmd && left <= 0 && !online;
  const waited = (now - startedAt) / 1000;

  return (
    <Modal title={`给 ${i.name} 安装 / 升级采集程序`} onClose={onClose} wide>
      <p>采集程序只统计这台机器的公网流量并每 10 秒上报一次。它不会执行任何其他命令，也不需要在机器上开放端口。</p>
      <ol className="steps">
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">复制安装命令</div>
            {error ? (
              <div className="field-error">{error} <button className="linkbtn" onClick={generate}>再试一次</button></div>
            ) : !cmd ? (
              <div className="sub"><Spinner /> 正在生成…</div>
            ) : (
              <>
                <div className="codebox" style={{ marginTop: 6 }}>
                  <code>{cmd.command}</code>
                  <CopyButton text={cmd.command} className="btn" />
                </div>
                <div className="sub" style={{ marginTop: 4 }}>
                  {online ? "命令已使用。" : expired ? <>命令已过期。<button className="linkbtn" onClick={generate}>重新生成</button></> : `这条命令只能用一次，${duration(left)}后失效。它只对 ${i.name} 有效。`}
                </div>
              </>
            )}
          </div>
        </li>
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">登录 {i.name}，粘贴并执行</div>
            <div className="sub">需要管理员权限（命令里已经带了 sudo）。支持常见的 64 位 Linux 系统。</div>
          </div>
        </li>
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">等待上线</div>
            {online ? (
              <div style={{ marginTop: 4 }}>
                <Badge level="good">已上线（最新版本）</Badge>{" "}
                {i.realtime && <span className="sub">当前速度：发出 {rate(i.realtime.tx_bytes_per_second)} · 收到 {rate(i.realtime.rx_bytes_per_second)}</span>}
              </div>
            ) : cmd && !expired ? (
              <div className="sub">
                <Spinner /> 正在等它上报（已等 {duration(waited)}）。执行命令后通常几十秒内出现。
                {waited > 90 && <div style={{ marginTop: 4 }}>等了比较久。请确认命令执行没有报错，并且这台机器能访问面板的地址。</div>}
              </div>
            ) : (
              <div className="sub">执行命令后，这里会自动变成“已上线”。</div>
            )}
          </div>
        </li>
      </ol>
      <div className="actions">
        <button className={`btn ${online ? "primary" : ""}`} onClick={onClose}>{online ? "完成" : "关闭（不影响安装）"}</button>
      </div>
    </Modal>
  );
}

function eventDetail(e: InstanceEvent): string {
  const addr = (v: InstanceEvent["after"] | null) => (v?.addresses?.length ? v.addresses.join("、") : "无");
  if (e.kind === "status_changed") return `${e.before?.status ? INSTANCE_STATUS[e.before.status][1] : "未知"} → ${e.after.status ? INSTANCE_STATUS[e.after.status][1] : "未知"}`;
  if (e.kind === "public_ipv4_changed" || e.kind === "ipv6_changed") return `${addr(e.before)} → ${addr(e.after)}`;
  return "";
}

/** 实例变化记录：默认折叠，展开时才读取 */
function Events() {
  const [open, setOpen] = useState(false);
  const res = useResource(() => (open ? api.instanceEvents({ limit: 30 }) : Promise.resolve(null)), [open]);
  return (
    <details className="card fold" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary><h2>变化记录</h2><span className="sub">实例的新增、消失、状态和 IP 变化</span><div className="spacer" /><span className="sub">展开</span></summary>
      {open && (
        <div style={{ marginTop: 10 }}>
          <Async res={res}>
            {(p) => !p || !p.items.length ? <EmptyState title="还没有变化记录" /> : (
              <div className="alist">
                {p.items.map((e) => (
                  <div className="aitem" key={e.id} style={{ gridTemplateColumns: "92px 1fr" }}>
                    <span className="when">{stamp(e.observed_at)}</span>
                    <div><span className="t">{e.instance_name}</span> {INSTANCE_EVENT[e.kind]}{eventDetail(e) && <span className="sub mono"> · {eventDetail(e)}</span>}</div>
                  </div>
                ))}
              </div>
            )}
          </Async>
        </div>
      )}
    </details>
  );
}

function SystemEventsFold({ instances, accounts }: { instances: Instance[]; accounts: Account[] }) {
  const [open, setOpen] = useState(false);
  const res = useResource(() => (open ? api.systemEvents({ limit: 30 }) : Promise.resolve(null)), [open]);
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;
  const instName = (id: string | null, cloudId: string) => (id ? instances.find((i) => i.id === id)?.name ?? cloudId : cloudId);

  return (
    <details className="card fold" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>
        <h2>阿里云系统维护与抢占回收事件</h2>
        <span className="sub">ECS 计划维护时间窗与抢占式实例回收中断预警</span>
        <div className="spacer" />
        <span className="sub">展开</span>
      </summary>
      {open && (
        <div style={{ marginTop: 10 }}>
          <Async res={res}>
            {(p) =>
              !p || !p.items.length ? (
                <EmptyState title="近期没有阿里云系统维护或抢占回收事件" />
              ) : (
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr><th>发布/观测时间</th><th>实例</th><th>类别与事件</th><th>云端状态</th></tr>
                    </thead>
                    <tbody>
                      {p.items.map((ev: SystemEvent) => (
                        <tr key={ev.id}>
                          <td>{stamp(ev.published_at ?? ev.observed_at)}</td>
                          <td>
                            <b>{instName(ev.instance_id, ev.cloud_instance_id)}</b>
                            <div className="sub">{accName(ev.account_id)} · {regionName(ev.region_id)}</div>
                          </td>
                          <td>
                            <Badge level={ev.category === "spot_interruption" ? "critical" : ev.category === "maintenance" ? "warning" : "neutral"}>
                              {ev.category === "spot_interruption" ? "抢占回收预警" : ev.category === "maintenance" ? "计划维护" : "系统事件"}
                            </Badge>
                            <span className="mono sub" style={{ marginLeft: 6 }}>{ev.event_type}</span>
                            {ev.not_before && <div className="sub">计划开始：{stamp(ev.not_before)}</div>}
                          </td>
                          <td><span className="tag">{ev.provider_status}</span></td>
                        </tr>
                      ))}
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

function ChangeEIPModal({ i, enabled, onClose, onDone }: { i: Instance; enabled: boolean; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [bw, setBw] = useState("100");
  const [chargeType, setChargeType] = useState<"PayByTraffic" | "PayByBandwidth">("PayByTraffic");
  const [busy, setBusy] = useState(false);

  const hasFixedIP = i.public_ips.some((p) => p.type === "instance_public_ip");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const { job_id } = await api.rotateInstanceEIP(i.id, {
        bandwidth_mbps: Math.max(1, Math.min(2000, Number(bw) || 100)),
        internet_charge_type: chargeType,
      });
      toast("已提交更换弹性公网 IP 任务，正在执行…");
      const job = await waitForJob(job_id);
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("弹性公网 IP 更换完成");
      onDone();
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={`更换弹性公网 IP：${i.name}`} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        {!enabled && (
          <Banner level="warning">
            当前服务器默认关闭弹性公网 IP 写操作（<span className="mono">HM_EIP_MANAGE_ENABLED=false</span>），防止误触发计费。如需启用请在服务端配置中显式开启。
          </Banner>
        )}
        {hasFixedIP && (
          <Banner level="critical">
            该实例当前带有固定实例公网 IP（非弹性公网 IP），阿里云不支持直接轮换 EIP。
          </Banner>
        )}
        <p className="note" style={{ margin: 0 }}>
          安全边界：仅当实例当前未绑定公网 IP、或当前绑定的 EIP 是由 Harbormaster 之前创建的受管 EIP 时才允许替换与释放旧 EIP；<b>原有的用户自有 EIP 会被严格拒绝解绑和释放</b>。
        </p>
        <div className="grid g2">
          <div>
            <label className="lbl">计费方式</label>
            <select value={chargeType} onChange={(e) => setChargeType(e.target.value as "PayByTraffic" | "PayByBandwidth")} style={{ width: "100%" }}>
              <option value="PayByTraffic">按使用流量（PayByTraffic）</option>
              <option value="PayByBandwidth">按固定带宽（PayByBandwidth）</option>
            </select>
          </div>
          <div>
            <label className="lbl">峰值带宽（Mbps）</label>
            <input type="number" min={1} max={2000} value={bw} onChange={(e) => setBw(e.target.value)} />
          </div>
        </div>
        <div className="actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy || !enabled || hasFixedIP}>
            {busy && <Spinner />} 确认申请并更换 IP
          </button>
        </div>
      </form>
    </Modal>
  );
}

function ReleaseECSModal({ i, onClose, onReleased }: { i: Instance; onClose: () => void; onReleased: () => void }) {
  const toast = useToast();
  const previewRes = useResource(() => api.releasePreview(i.id), [i.id]);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    const pv = previewRes.data;
    if (!pv?.preview_token) return;
    setBusy(true);
    try {
      const { job_id } = await api.releaseInstance(i.id, typed, pv.preview_token);
      toast("已提交释放任务，等待阿里云确认…");
      const job = await waitForJob(job_id);
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast(`实例 ${i.name} 已释放`);
      onReleased();
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={`释放按量 ECS 实例：${i.name}`} onClose={onClose} wide>
      <Async res={previewRes} lines={4}>
        {(pv) => {
          const canExec = pv.execution_enabled && pv.block_reasons.length === 0 && !!pv.preview_token;
          return (
            <div className="stack">
              {!pv.execution_enabled && (
                <Banner level="warning">
                  当前服务器默认关闭高危实例释放开关（<span className="mono">HM_INSTANCE_RELEASE_ENABLED=false</span>）。以下仅展示只读释放影响预览。
                </Banner>
              )}
              {pv.block_reasons.length > 0 && (
                <Banner level="critical">
                  当前不满足安全释放条件：{pv.block_reasons.join("、")}
                </Banner>
              )}
              {pv.impact && (
                <dl className="kv">
                  <dt>云端实例名称</dt><dd><b>{pv.impact.instance_name}</b>（<span className="mono">{pv.impact.cloud_instance_id}</span>）</dd>
                  <dt>实例计算与系统盘</dt><dd>不可恢复销毁（{pv.impact.disks.length} 块挂载云盘）</dd>
                  <dt>弹性公网 IP</dt><dd>保留不自动删除（{pv.impact.eips_action}）</dd>
                  <dt>手动快照</dt><dd>保留不自动删除（{pv.impact.manual_snapshots_action}）</dd>
                </dl>
              )}
              {canExec && (
                <div>
                  <label className="lbl">
                    不可恢复操作：请输入完整实例名称 <b>{pv.impact?.instance_name ?? i.name}</b> 确认释放
                  </label>
                  <input type="text" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
                </div>
              )}
              <div className="actions">
                <button className="btn" onClick={onClose}>取消</button>
                <button
                  className="btn danger"
                  disabled={!canExec || busy || typed !== (pv.impact?.instance_name ?? i.name)}
                  onClick={submit}
                >
                  {busy && <Spinner />} 永久释放此实例
                </button>
              </div>
            </div>
          );
        }}
      </Async>
    </Modal>
  );
}

function CreateECSModal({
  accounts,
  sysInfo,
  onClose,
  onCreated,
}: {
  accounts: Account[];
  sysInfo?: SystemInfo;
  onClose: () => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const enabledAccs = accounts.filter((a) => a.enabled && a.region_ids.length > 0);
  const [accountId, setAccountId] = useState(enabledAccs[0]?.id ?? "");
  const curAcc = enabledAccs.find((a) => a.id === accountId) ?? enabledAccs[0];
  const [regionId, setRegionId] = useState(curAcc?.region_ids[0] ?? "ap-southeast-1");

  useEffect(() => {
    if (curAcc && !curAcc.region_ids.includes(regionId)) {
      setRegionId(curAcc.region_ids[0] ?? "ap-southeast-1");
    }
  }, [curAcc, regionId]);

  const catRes = useResource(
    () => (accountId && regionId ? api.ecsCatalog(accountId, regionId) : Promise.resolve(null)),
    [accountId, regionId],
  );

  const [zoneId, setZoneId] = useState("");
  const [instanceType, setInstanceType] = useState("");
  const [imageId, setImageId] = useState("");
  const [instanceName, setInstanceName] = useState("hm-node-01");
  const [chargeType, setChargeType] = useState<"PostPaid" | "Spot">("PostPaid");
  const [netCharge, setNetCharge] = useState<"PayByTraffic" | "PayByBandwidth">("PayByTraffic");
  const [bwOut, setBwOut] = useState("100");
  const [diskGiB, setDiskGiB] = useState("20");
  const [autoNet, setAutoNet] = useState(true);
  const [vpcId, setVpcId] = useState("");
  const [vswId, setVswId] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const c = catRes.data;
    if (!c) return;
    if (!zoneId && c.zones[0]) setZoneId(c.zones[0].id);
    if (!instanceType && c.instance_types[0]) setInstanceType(c.instance_types[0].id);
    if (!imageId && c.images[0]) setImageId(c.images[0].id);
    if (!vpcId && c.vpcs[0]) setVpcId(c.vpcs[0].id);
    if (!vswId && c.vswitches[0]) setVswId(c.vswitches[0].id);
  }, [catRes.data, zoneId, instanceType, imageId, vpcId, vswId]);

  const createEnabled = Boolean(sysInfo?.instance_create_enabled);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const { job_id } = await api.createECSInstance({
        account_id: accountId,
        region_id: regionId,
        zone_id: zoneId,
        instance_name: instanceName.trim(),
        instance_type: instanceType,
        image_id: imageId,
        instance_charge_type: chargeType,
        internet_charge_type: netCharge,
        bandwidth_out_mbps: Number(bwOut) || 0,
        system_disk_gib: Number(diskGiB) || 20,
        auto_create_network: autoNet,
        vpc_id: autoNet ? "" : vpcId,
        vswitch_id: autoNet ? "" : vswId,
        confirm,
      });
      toast("已提交创建 ECS 任务，正在创建网络与实例…");
      const job = await waitForJob(job_id);
      if (job.status === "failed") toast(errorText(job.error?.code ?? "internal_error"), true);
      else toast("ECS 实例已创建成功");
      onCreated();
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="创建阿里云 ECS 实例（按量 / 抢占式）" onClose={onClose} wide>
      <form className="stack" onSubmit={submit}>
        {!createEnabled && (
          <Banner level="warning">
            当前服务器默认关闭创建实例开关（<span className="mono">HM_INSTANCE_CREATE_ENABLED=false</span>），防止产生预期外云资源费用。你可以先预览地域目录，如需直接创建请在服务端开启该环境变量。
          </Banner>
        )}
        <div className="grid g2">
          <div>
            <label className="lbl">阿里云账号</label>
            <select value={accountId} onChange={(e) => setAccountId(e.target.value)} style={{ width: "100%" }}>
              {enabledAccs.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="lbl">地域</label>
            <select value={regionId} onChange={(e) => { setRegionId(e.target.value); setZoneId(""); setInstanceType(""); setImageId(""); }} style={{ width: "100%" }}>
              {(curAcc?.region_ids ?? []).map((r) => (
                <option key={r} value={r}>{regionName(r)} ({r})</option>
              ))}
            </select>
          </div>
        </div>

        <Async res={catRes} lines={4}>
          {(cat) =>
            !cat ? null : (
              <>
                <div className="grid g3">
                  <div>
                    <label className="lbl">可用区</label>
                    <select value={zoneId} onChange={(e) => setZoneId(e.target.value)} style={{ width: "100%" }}>
                      {cat.zones.map((z) => (
                        <option key={z.id} value={z.id}>{z.name || z.id}</option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="lbl">实例规格</label>
                    <select value={instanceType} onChange={(e) => setInstanceType(e.target.value)} style={{ width: "100%" }}>
                      {cat.instance_types.map((t) => (
                        <option key={t.id} value={t.id}>{t.id} ({t.cpu}C / {t.memory_gib}G)</option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="lbl">系统镜像</label>
                    <select value={imageId} onChange={(e) => setImageId(e.target.value)} style={{ width: "100%" }}>
                      {cat.images.map((im) => (
                        <option key={im.id} value={im.id}>{im.name}</option>
                      ))}
                    </select>
                  </div>
                </div>
                <div className="grid g3">
                  <div>
                    <label className="lbl">实例名称</label>
                    <input type="text" value={instanceName} onChange={(e) => setInstanceName(e.target.value)} />
                  </div>
                  <div>
                    <label className="lbl">实例计费方式（不支持包年包月）</label>
                    <select value={chargeType} onChange={(e) => setChargeType(e.target.value as "PostPaid" | "Spot")} style={{ width: "100%" }}>
                      <option value="PostPaid">按量付费（PostPaid）</option>
                      <option value="Spot">抢占式实例（Spot）</option>
                    </select>
                  </div>
                  <div>
                    <label className="lbl">系统盘大小（GiB）</label>
                    <input type="number" min={20} max={500} value={diskGiB} onChange={(e) => setDiskGiB(e.target.value)} />
                  </div>
                </div>
                <div className="grid g2">
                  <div>
                    <label className="lbl">公网带宽计费</label>
                    <select value={netCharge} onChange={(e) => setNetCharge(e.target.value as "PayByTraffic" | "PayByBandwidth")} style={{ width: "100%" }}>
                      <option value="PayByTraffic">按使用流量</option>
                      <option value="PayByBandwidth">按固定带宽</option>
                    </select>
                  </div>
                  <div>
                    <label className="lbl">公网出带宽峰值（0–200 Mbps）</label>
                    <input type="number" min={0} max={200} value={bwOut} onChange={(e) => setBwOut(e.target.value)} />
                  </div>
                </div>
                <div className="inline">
                  <Switch on={autoNet} onChange={setAutoNet} label="自动创建专有网络与安全组" />
                  <span>若无可用网络则自动创建 VPC、交换机与最小权限安全组</span>
                </div>
                {!autoNet && (
                  <div className="grid g2">
                    <div>
                      <label className="lbl">已有 VPC</label>
                      <select value={vpcId} onChange={(e) => setVpcId(e.target.value)} style={{ width: "100%" }}>
                        {cat.vpcs.map((v) => (
                          <option key={v.id} value={v.id}>{v.name || v.id} ({v.cidr_block})</option>
                        ))}
                      </select>
                    </div>
                    <div>
                      <label className="lbl">已有交换机</label>
                      <select value={vswId} onChange={(e) => setVswId(e.target.value)} style={{ width: "100%" }}>
                        {cat.vswitches.filter((s) => !vpcId || s.vpc_id === vpcId).map((s) => (
                          <option key={s.id} value={s.id}>{s.name || s.id} ({s.zone_id})</option>
                        ))}
                      </select>
                    </div>
                  </div>
                )}
              </>
            )
          }
        </Async>

        <label className="inline" style={{ cursor: "pointer" }}>
          <input type="checkbox" checked={confirm} onChange={(e) => setConfirm(e.target.checked)} style={{ width: "auto" }} />
          <span>我已知晓创建按量或抢占式 ECS 会立即在阿里云产生计算、存储与公网费用</span>
        </label>

        <div className="actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy || !createEnabled || !confirm || !zoneId || !instanceType || !imageId}>
            {busy && <Spinner />} 确认创建 ECS
          </button>
        </div>
      </form>
    </Modal>
  );
}
