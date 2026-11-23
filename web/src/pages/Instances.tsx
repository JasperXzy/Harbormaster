// 实例管理工作区：即时搜索与多维筛选栏、高密度实例表格（支持一键复制 IP）、右侧滑出详情抽屉（概览/账单/曲线/备注）及变动与维护事件视图。
import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { waitForJob } from "../api/jobs";
import { AGENT, CHARGE, errorText, INSTANCE_EVENT, INSTANCE_STATUS, KIND, protectionOf, regionName, RENEWAL, RULE, SCOPE, STOP_MODE, SUBJECT_KIND } from "../api/labels";
import type { Account, AgentInstallCommand, Instance, InstanceEvent, SystemEvent, SystemInfo } from "../api/types";
import { LineChart } from "../components/charts/LineChart";
import { PlusIcon, PowerIcon } from "../components/icons";
import { Async, Badge, Banner, Card, Confirm, CopyButton, Drawer, EmptyState, ErrorState, errMsg, Fresh, InfoTip, Modal, SegToggle, Skeleton, Spinner, SubTabs, Switch, useToast } from "../components/ui";
import { useShared } from "../lib/data";
import { ago, duration, gb, gbValue, num, rate, stamp, time, ver, ymd } from "../lib/format";
import { INSTANCE_SORT_OPTIONS, sortInstances, useInstanceSort, type InstanceSortKey } from "../lib/instanceSort";
import { Link, useQueryTab } from "../lib/router";
import { useNow, useResource } from "../lib/useResource";

const canInstall = (i: Instance) => i.agent.status === "not_installed";

function RateCell({ i }: { i: Instance }) {
  if (i.realtime) {
    return (
      <span className="num" style={{ fontSize: "var(--fs-sm)" }}>
        <span title="出方向速率">↑ {rate(i.realtime.tx_bytes_per_second)}</span>{" "}
        <span className="sub" title="入方向速率">↓ {rate(i.realtime.rx_bytes_per_second)}</span>
      </span>
    );
  }
  const stream = i.cloud_network?.streams?.[0];
  if (stream && (stream.egress.value != null || stream.ingress.value != null)) {
    const tx = (stream.egress.value ?? 0) / 8;
    const rx = (stream.ingress.value ?? 0) / 8;
    return (
      <span className="num" style={{ fontSize: "var(--fs-sm)" }} title="来自云监控分钟级指标">
        <span>↑ {rate(tx)}</span> <span className="sub">↓ {rate(rx)}</span>
      </span>
    );
  }
  return <span className="sub">—</span>;
}

function specNote(i: Instance): string {
  const c = i.network_capacity;
  if (!c?.public_outbound_exceeds_spec || c.tx_kbps === null || c.public_outbound_mbps === null) return "";
  return `公网峰值设为 ${c.public_outbound_mbps} Mbps，但当前实例规格内网出方向上限约 ${Math.round(c.tx_kbps / 1000)} Mbps。`;
}

function BillingCell({ i }: { i: Instance }) {
  const swasUsage = i.swas_traffic_package?.usage;
  if (i.kind === "swas" && swasUsage) {
    return (
      <div className="cell-stack">
        <span>轻量套餐包 · {gb(swasUsage.used_bytes)} / {gb(swasUsage.total_bytes, 0)}</span>
        <span className="sub">
          剩余 {gb(swasUsage.remaining_bytes)}
          {i.bandwidth_mbps ? ` · ${i.bandwidth_mbps}M` : ""}
          {i.expires_at ? ` · ${ymd(i.expires_at)} 到期` : ""}
        </span>
      </div>
    );
  }
  if ((i.billing_subject_ids?.length ?? 0) > 1) {
    return <span className="sub">多公网出口独立计费</span>;
  }
  if (!i.billing_subject_kind) {
    return <span className="sub">{i.public_ips.length ? "计费类型待确认" : "无公网出口"}</span>;
  }
  const shortKind = i.bandwidth_package_name
    ? `共享带宽 · ${i.bandwidth_package_name}`
    : SUBJECT_KIND[i.billing_subject_kind];
  const note = specNote(i);
  return (
    <div className="cell-stack">
      <div className="inline" style={{ gap: 4 }}>
        <span>{shortKind}</span>
        {note && <InfoTip label="规格带宽提示">{note}</InfoTip>}
      </div>
      <span className="sub">
        {i.counts_toward_cdt === null
          ? "CDT 归属待确认"
          : i.counts_toward_cdt && i.cdt_scope
            ? `计入${SCOPE[i.cdt_scope]}免费额度`
            : "不计入 CDT"}
        {i.bandwidth_mbps ? ` · ${i.bandwidth_mbps} Mbps` : ""}
        {i.expires_at ? ` · ${ymd(i.expires_at)} 到期` : ""}
      </span>
    </div>
  );
}

function AgentCell({ i, onInstall }: { i: Instance; onInstall: () => void }) {
  const [al, at] = AGENT[i.agent.status];
  const [pl, pt, pd] = protectionOf(i);
  return (
    <div className="inline" style={{ gap: 6 }}>
      {i.realtime_protection === "not_applicable" ? <Badge level={al}>{at}</Badge> : <Badge level={pl} title={pd}>{pt}</Badge>}
      {i.agent.update_available && <span className="tag" title="采集程序有新版本">可升级</span>}
      {canInstall(i) && (
        <button
          type="button"
          className="linkbtn sub"
          onClick={(e) => {
            e.stopPropagation();
            onInstall();
          }}
        >
          安装
        </button>
      )}
    </div>
  );
}

export function Instances() {
  const toast = useToast();
  const now = useNow(15_000);
  const { accounts, instances, quotas, reloadAll } = useShared();
  const [view, setView] = useQueryTab("list", ["list", "events"] as const);
  const [q, setQ] = useState("");
  const [account, setAccount] = useState("");
  const [group, setGroup] = useState("");
  const [tag, setTag] = useState("");
  const [only, setOnly] = useState(false);
  const { sort, updateSort } = useInstanceSort();
  const [detail, setDetail] = useState<string>();
  const [install, setInstall] = useState<string>();
  const [creatingECS, setCreatingECS] = useState(false);
  const [answering, setAnswering] = useState<string>();
  const sysInfoRes = useResource(api.systemInfo, []);

  if (instances.error) return <div className="card"><ErrorState error={instances.error} onRetry={instances.reload} /></div>;
  if (!instances.data || !accounts.data) return <div className="card"><Skeleton lines={6} height={18} /></div>;

  const liveEstMap = new Map<string, NonNullable<Instance["traffic_estimation"]>>();
  for (const qt of quotas.data ?? []) {
    if (qt.estimation) liveEstMap.set(`${qt.account_id}:${qt.scope}`, qt.estimation);
  }
  const all = instances.data.items.map((i) => {
    const live = i.cdt_scope ? liveEstMap.get(`${i.account_id}:${i.cdt_scope}`) : undefined;
    return live ? { ...i, traffic_estimation: live } : i;
  });
  const accs = accounts.data;
  const accName = (id: string) => accs.find((a) => a.id === id)?.name ?? "—";
  const groups = [...new Set(all.map((i) => i.group).filter((g): g is string => !!g))].sort();
  const tags = [...new Set(all.flatMap((i) => i.tags ?? []))].sort();

  const needs = (i: Instance) =>
    Boolean(i.pending_intent_confirmation) ||
    i.realtime_protection === "worst_case" ||
    (i.realtime_protection === "unprotected" && i.status === "running" && i.agent.status === "not_installed");

  const needsCount = all.filter(needs).length;
  const needle = q.trim().toLowerCase();

  const filteredRows = all.filter((i) => {
    if (account && i.account_id !== account) return false;
    if (group && i.group !== group) return false;
    if (tag && !(i.tags ?? []).includes(tag)) return false;
    if (only && !needs(i)) return false;
    if (needle) {
      const hay = [
        i.name,
        i.cloud_instance_id,
        i.region_id,
        regionName(i.region_id),
        i.group ?? "",
        ...(i.tags ?? []),
        i.note ?? "",
        ...i.public_ips.map((p) => p.address),
      ]
        .join(" ")
        .toLowerCase();
      if (!hay.includes(needle)) return false;
    }
    return true;
  });
  const rows = sortInstances(filteredRows, sort, accName);

  const cur = all.find((i) => i.id === detail);
  const inst = all.find((i) => i.id === install);
  const pendingConfirms = all.filter((i) => i.pending_intent_confirmation);

  const answerIntent = async (target: Instance, decision: "keep_stopped" | "resume") => {
    setAnswering(target.id);
    try {
      const r = await api.confirmInstanceIntent(target.id, decision);
      if (r.job_id) {
        toast(`已提交开机请求，正在恢复 ${target.name}…`);
        await waitForJob(r.job_id);
      } else {
        toast(`已将 ${target.name} 标记为保持停止`);
      }
      reloadAll();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setAnswering(undefined);
    }
  };

  if (!all.length) {
    return (
      <div className="card">
        {accs.length ? (
          accs.some((a) => a.enabled && a.sync_status === "never_synced") ? (
            <EmptyState title="正在执行首次资源扫描">刚添加的云账号正在同步名下实例，通常 1 分钟内自动显示。</EmptyState>
          ) : (
            <EmptyState
              title="尚未发现实例"
              action={
                accs.some((a) => a.enabled) ? (
                  <button className="btn primary" onClick={() => setCreatingECS(true)}>
                    <PlusIcon size={15} /> 创建 ECS 实例
                  </button>
                ) : undefined
              }
            >
              所选地域下暂无 ECS 或轻量应用服务器。若机器在其他地域，请前往设置页勾选对应地域。
            </EmptyState>
          )
        ) : (
          <EmptyState title="尚未添加阿里云账号" action={<Link className="btn primary" to="/settings">添加云账号</Link>}>
            添加账号后将自动发现其名下实例与公网计费状态。
          </EmptyState>
        )}
        {creatingECS && <CreateECSModal accounts={accs} sysInfo={sysInfoRes.data} onClose={() => setCreatingECS(false)} onCreated={() => { setCreatingECS(false); reloadAll(); }} />}
      </div>
    );
  }

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
          <b>{i.name} 在面板外部被关机。</b> 请确认保持停止还是立即恢复运行。
        </Banner>
      ))}

      <SubTabs
        value={view}
        onChange={setView}
        tabs={[
          { key: "list", label: "实例列表", badge: all.length },
          { key: "events", label: "变动与系统维护事件" },
        ]}
        right={
          <button className="btn sm primary" onClick={() => setCreatingECS(true)}>
            <PlusIcon size={14} /> 创建 ECS
          </button>
        }
      />

      {view === "list" && (
        <>
          <div className="toolbar">
            <input
              type="text"
              className="search-input"
              placeholder="搜索名称、公网 IP、地域或备注…"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              aria-label="搜索实例"
            />
            <select value={account} onChange={(e) => setAccount(e.target.value)} aria-label="按账号筛选">
              <option value="">全部账号</option>
              {accs.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
            {groups.length > 0 && (
              <select value={group} onChange={(e) => setGroup(e.target.value)} aria-label="按分组筛选">
                <option value="">全部分组</option>
                {groups.map((g) => (
                  <option key={g} value={g}>{g}</option>
                ))}
              </select>
            )}
            {tags.length > 0 && (
              <select value={tag} onChange={(e) => setTag(e.target.value)} aria-label="按标签筛选">
                <option value="">全部标签</option>
                {tags.map((t) => (
                  <option key={t} value={t}>#{t}</option>
                ))}
              </select>
            )}
            <label className={`check ${only ? "on" : ""}`}>
              <input type="checkbox" checked={only} onChange={(e) => setOnly(e.target.checked)} />
              需关注{needsCount > 0 ? ` (${needsCount})` : ""}
            </label>
            <div className="inline" style={{ gap: 4 }}>
              <select
                value={sort.key}
                onChange={(e) => {
                  const key = e.target.value as InstanceSortKey;
                  const defDir = INSTANCE_SORT_OPTIONS.find(([k]) => k === key)?.[2] ?? "asc";
                  updateSort(key, defDir);
                }}
                aria-label="排序字段"
              >
                {INSTANCE_SORT_OPTIONS.map(([key, label]) => (
                  <option key={key} value={key}>按{label}排序</option>
                ))}
              </select>
              <button
                type="button"
                className="btn sm ghost"
                onClick={() => updateSort(sort.key, sort.dir === "asc" ? "desc" : "asc")}
                title={sort.dir === "asc" ? "当前升序，点击切换为降序" : "当前降序，点击切换为升序"}
                aria-label="切换升降序"
              >
                {sort.dir === "asc" ? "↑ 升序" : "↓ 降序"}
              </button>
            </div>
            <div className="spacer" />
            <Fresh at={instances.data.updated_at} kind="resources" name="实例同步" quiet />
            <span className="sub num">
              {rows.length === all.length ? `共 ${all.length} 台` : `筛选 ${rows.length} / ${all.length} 台`}
            </span>
          </div>

          <div className="card" style={{ padding: rows.length ? "8px 18px" : undefined }}>
            {!rows.length ? (
              <EmptyState
                title="没有符合筛选条件的实例"
                action={
                  <button className="btn sm" onClick={() => { setQ(""); setAccount(""); setGroup(""); setTag(""); setOnly(false); }}>
                    重置筛选
                  </button>
                }
              />
            ) : (
              <>
                <div className="table-wrap only-wide">
                  <table>
                    <thead>
                      <tr>
                        <th>
                          <button type="button" className="linkbtn" style={{ fontWeight: 600, color: "inherit" }} onClick={() => updateSort("name")}>
                            实例与地域{sort.key === "name" ? (sort.dir === "asc" ? " ↑" : " ↓") : ""}
                          </button>
                        </th>
                        <th>
                          <button type="button" className="linkbtn" style={{ fontWeight: 600, color: "inherit" }} onClick={() => updateSort("status")}>
                            运行状态{sort.key === "status" ? (sort.dir === "asc" ? " ↑" : " ↓") : ""}
                          </button>
                        </th>
                        <th>公网地址</th>
                        <th>
                          <button type="button" className="linkbtn" style={{ fontWeight: 600, color: "inherit" }} onClick={() => updateSort("bandwidth")}>
                            计费与额度归属{sort.key === "bandwidth" ? (sort.dir === "asc" ? " ↑" : " ↓") : ""}
                          </button>
                        </th>
                        <th>
                          <button type="button" className="linkbtn" style={{ fontWeight: 600, color: "inherit" }} onClick={() => updateSort("rate")}>
                            实时速率{sort.key === "rate" ? (sort.dir === "asc" ? " ↑" : " ↓") : ""}
                          </button>
                        </th>
                        <th>实时保护</th>
                        <th className="num">操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((i) => {
                        const [sl, st] = INSTANCE_STATUS[i.status];
                        const exempt = Boolean(i.quota_exemption_until && Date.parse(i.quota_exemption_until) > now);
                        return (
                          <tr key={i.id} className="clickable" onClick={() => setDetail(i.id)}>
                            <td>
                              <div className="cell-stack" style={{ justifyItems: "start" }}>
                                <div className="inline" style={{ gap: 6 }}>
                                  <span style={{ fontWeight: 650, color: "var(--text)" }}>{i.name}</span>
                                  {i.is_server_self && <span className="tag" title="运行 Harbormaster 面板自身的机器">面板自身</span>}
                                  {i.kind === "swas" && <span className="tag">轻量</span>}
                                  {i.group && <span className="tag">{i.group}</span>}
                                  {(i.tags ?? []).slice(0, 2).map((t) => (
                                    <span className="tag" key={t}>#{t}</span>
                                  ))}
                                </div>
                                <span className="sub">
                                  {accName(i.account_id)} · {regionName(i.region_id)}
                                  {i.note ? ` · ${i.note.split("\n")[0].slice(0, 26)}` : ""}
                                </span>
                              </div>
                            </td>
                            <td>
                              <div className="cell-stack" style={{ justifyItems: "start" }}>
                                <Badge level={sl}>{st}</Badge>
                                {exempt && <span className="sub">豁免至 {stamp(i.quota_exemption_until!)}</span>}
                                {i.pending_intent_confirmation && <Badge level="warning">待确认</Badge>}
                              </div>
                            </td>
                            <td>
                              {i.public_ips.length ? (
                                <div className="cell-stack">
                                  {i.public_ips.map((p) => (
                                    <div key={p.address} className="inline" style={{ gap: 4 }}>
                                      <span className="mono">{p.address}</span>
                                      {p.type === "eip" && <span className="tag">EIP</span>}
                                      <CopyButton text={p.address} label="复制 IP" iconOnly />
                                    </div>
                                  ))}
                                </div>
                              ) : (
                                <span className="sub">—</span>
                              )}
                            </td>
                            <td><BillingCell i={i} /></td>
                            <td><RateCell i={i} /></td>
                            <td><AgentCell i={i} onInstall={() => setInstall(i.id)} /></td>
                            <td className="num">
                              <button
                                type="button"
                                className="btn sm ghost"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  setDetail(i.id);
                                }}
                              >
                                详情
                              </button>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>

                <div className="icards" style={{ padding: "8px 0" }}>
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
                        <div className="sub">
                          {accName(i.account_id)} · {regionName(i.region_id)} · <span className="mono">{i.public_ips[0]?.address ?? "无公网 IP"}</span>
                        </div>
                        <BillingCell i={i} />
                        <div className="hd">
                          <AgentCell i={i} onInstall={() => setInstall(i.id)} />
                          <div className="spacer" />
                          <RateCell i={i} />
                        </div>
                      </div>
                    );
                  })}
                </div>
              </>
            )}
          </div>
        </>
      )}

      {view === "events" && <EventsWorkspace instances={all} accounts={accs} />}

      {cur && (
        <InstanceDetailDrawer
          i={cur}
          account={accName(cur.account_id)}
          sysInfo={sysInfoRes.data}
          onClose={() => setDetail(undefined)}
          onInstall={() => setInstall(cur.id)}
        />
      )}
      {inst && <AgentInstall i={inst} onClose={() => setInstall(undefined)} />}
      {creatingECS && (
        <CreateECSModal
          accounts={accs}
          sysInfo={sysInfoRes.data}
          onClose={() => setCreatingECS(false)}
          onCreated={() => {
            setCreatingECS(false);
            reloadAll();
          }}
        />
      )}
    </>
  );
}

/* ---------- Events Sub-Workspace ---------- */
function eventDetail(e: InstanceEvent): string {
  const addr = (v: InstanceEvent["after"] | null) => (v?.addresses?.length ? v.addresses.join("、") : "无");
  if (e.kind === "status_changed") return `${e.before?.status ? INSTANCE_STATUS[e.before.status][1] : "未知"} → ${e.after.status ? INSTANCE_STATUS[e.after.status][1] : "未知"}`;
  if (e.kind === "public_ipv4_changed" || e.kind === "ipv6_changed") return `${addr(e.before)} → ${addr(e.after)}`;
  return "";
}

function EventsWorkspace({ instances, accounts }: { instances: Instance[]; accounts: Account[] }) {
  const evRes = useResource(() => api.instanceEvents({ limit: 40 }), []);
  const sysRes = useResource(() => api.systemEvents({ limit: 40 }), []);
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;
  const instName = (id: string | null, cloudId: string) => (id ? instances.find((i) => i.id === id)?.name ?? cloudId : cloudId);

  return (
    <div className="grid g2">
      <Card title="实例状态与地址变更" sub="新增、移除、运行态及公网 IP 变动记录">
        <Async res={evRes} lines={5}>
          {(p) =>
            !p.items.length ? (
              <EmptyState title="暂无实例变动记录" />
            ) : (
              <div className="alist">
                {p.items.map((e) => (
                  <div className="aitem" key={e.id} style={{ gridTemplateColumns: "1fr auto" }}>
                    <div>
                      <span className="t">{e.instance_name}</span> · {INSTANCE_EVENT[e.kind]}
                      {eventDetail(e) && <div className="d mono">{eventDetail(e)}</div>}
                    </div>
                    <span className="when">{stamp(e.observed_at)}</span>
                  </div>
                ))}
              </div>
            )
          }
        </Async>
      </Card>

      <Card title="阿里云计划维护与抢占回收" sub="ECS 系统维护时间窗与 Spot 回收预警">
        <Async res={sysRes} lines={5}>
          {(p) =>
            !p.items.length ? (
              <EmptyState title="近期无云端维护或回收预警">所有实例当前未收到计划维护或抢占中断事件。</EmptyState>
            ) : (
              <div className="alist">
                {p.items.map((ev: SystemEvent) => (
                  <div className="aitem" key={ev.id}>
                    <Badge level={ev.category === "spot_interruption" ? "critical" : ev.category === "maintenance" ? "warning" : "neutral"}>
                      {ev.category === "spot_interruption" ? "抢占回收" : ev.category === "maintenance" ? "计划维护" : "系统事件"}
                    </Badge>
                    <div>
                      <div className="t">
                        {instName(ev.instance_id, ev.cloud_instance_id)}{" "}
                        <span className="sub" style={{ fontWeight: 400 }}>· {accName(ev.account_id)} · {regionName(ev.region_id)}</span>
                      </div>
                      <div className="d">
                        <span className="mono">{ev.event_type}</span> · 状态 <span className="tag">{ev.provider_status}</span>
                        {ev.not_before ? ` · 计划开始 ${stamp(ev.not_before)}` : ""}
                      </div>
                    </div>
                    <span className="when">{stamp(ev.published_at ?? ev.observed_at)}</span>
                  </div>
                ))}
              </div>
            )
          }
        </Async>
      </Card>
    </div>
  );
}

/* ---------- Slide-Over Instance Detail Drawer ---------- */
type DetailTab = "overview" | "traffic" | "meta";

function InstanceDetailDrawer({
  i,
  account,
  sysInfo,
  onClose,
  onInstall,
}: {
  i: Instance;
  account: string;
  sysInfo?: SystemInfo;
  onClose: () => void;
  onInstall: () => void;
}) {
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

  return (
    <>
      <Drawer
        wide
        onClose={onClose}
        title={
          <div className="inline" style={{ gap: 8 }}>
            <span>{i.name}</span>
            <Badge level={sl}>{st}</Badge>
            {i.is_server_self && <span className="tag">面板自身</span>}
          </div>
        }
        sub={
          <span>
            {account} · {regionName(i.region_id)}{i.zone_id ? ` (${i.zone_id})` : ""} · <span className="mono">{i.cloud_instance_id}</span>
          </span>
        }
        right={
          <div className="inline" style={{ gap: 6 }}>
            {i.status === "stopped" && (
              <button className="btn sm primary" disabled={powerBusy} onClick={() => setPowerConfirm("start")}>
                {powerBusy ? <Spinner /> : <PowerIcon size={13} />} 开机
              </button>
            )}
            {i.status === "running" && (
              <>
                <button className="btn sm" disabled={powerBusy} onClick={() => setPowerConfirm("reboot")}>重启</button>
                <button className="btn sm danger" disabled={powerBusy} onClick={() => setPowerConfirm("stop")}>关机</button>
              </>
            )}
          </div>
        }
      >
        <SubTabs
          value={tab}
          onChange={setTab}
          tabs={[
            { key: "overview", label: "概览与运维" },
            { key: "traffic", label: "历史流量曲线" },
            { key: "meta", label: "分组与本地备注" },
          ]}
        />

        {tab === "overview" && (
          <div className="stack">
            <div className="card compact">
              <div className="card-head" style={{ marginBottom: 12 }}>
                <h2>规格、网络与月度成本</h2>
                <div className="spacer" />
                <span className="sub">同步于 {stamp(i.updated_at)}</span>
              </div>
              <dl className="kv">
                <dt>产品与计费</dt>
                <dd>
                  {KIND[i.kind]} · {CHARGE[i.charge_type]}
                  {i.expires_at ? ` · ${ymd(i.expires_at)} 到期` : ""}
                  {i.renewal && i.renewal.status !== "not_applicable" ? ` · ${RENEWAL[i.renewal.status][1]}` : ""}
                </dd>
                <dt>实例规格</dt>
                <dd className="mono">{i.spec}</dd>
                {i.cpu?.usage.value != null && (
                  <>
                    <dt>CPU 负载</dt>
                    <dd>
                      <b>{num(i.cpu.usage.value, 1)}%</b>
                      {i.cpu.credit_balance?.value != null && <span className="sub"> · 突发性能积分剩余 {num(i.cpu.credit_balance.value, 1)}</span>}
                    </dd>
                  </>
                )}
                <dt>公网 IPv4</dt>
                <dd>
                  {i.public_ips.length ? (
                    <div className="inline" style={{ gap: 10 }}>
                      {i.public_ips.map((p) => (
                        <span key={p.address} className="inline" style={{ gap: 4 }}>
                          <span className="mono">{p.address}</span>
                          {p.type === "eip" && <span className="tag">弹性 EIP</span>}
                          <CopyButton text={p.address} iconOnly label="复制 IP" />
                        </span>
                      ))}
                    </div>
                  ) : (
                    "—"
                  )}
                </dd>
                {!!i.ipv6?.addresses.length && (
                  <>
                    <dt>公网 IPv6</dt>
                    <dd className="mono">
                      {i.ipv6.addresses.join("、")}
                      {i.ipv6.status === "stale" ? "（缓存）" : ""}
                    </dd>
                  </>
                )}
                <dt>公网计费方式</dt>
                <dd>
                  {(i.billing_subject_ids?.length ?? 0) > 1
                    ? "多公网出口分别计费"
                    : i.billing_subject_kind
                      ? SUBJECT_KIND[i.billing_subject_kind]
                      : i.public_ips.length
                        ? "待确认"
                        : "无公网出口"}
                  {i.bandwidth_package_name ? `（${i.bandwidth_package_name}）` : ""}
                  {i.bandwidth_mbps ? ` · 峰值 ${i.bandwidth_mbps} Mbps` : ""}
                  {specNote(i) && <div className="field-help">{specNote(i)}</div>}
                </dd>
                <dt>流量计量与 CDT</dt>
                <dd>
                  {i.metering_rule ? RULE[i.metering_rule] : "—"}
                  {i.counts_toward_cdt === null
                    ? " · CDT 归属待确认"
                    : i.counts_toward_cdt && i.cdt_scope
                      ? ` · 计入${SCOPE[i.cdt_scope]}免费额度`
                      : " · 不计入免费额度"}
                </dd>
                {i.kind === "swas" && (
                  <>
                    <dt>轻量套餐流量包</dt>
                    <dd>
                      {i.swas_traffic_package?.usage
                        ? `${gb(i.swas_traffic_package.usage.used_bytes)} / ${gb(i.swas_traffic_package.usage.total_bytes, 0)}（剩余 ${gb(i.swas_traffic_package.usage.remaining_bytes)}）`
                        : i.billing_subject_kind === "swas_no_traffic_package"
                          ? "无流量包限制"
                          : "待确认"}
                    </dd>
                  </>
                )}
                <dt>当月账单明细</dt>
                <dd>
                  {finRes.data ? (
                    finRes.data.bill.data?.items.length ? (
                      <div className="inline" style={{ gap: 10 }}>
                        {finRes.data.bill.data.items.map((it, k) => (
                          <span key={k}>
                            {it.product_name || it.product_code}: <b>{it.currency} {it.after_discount_amount}</b>
                          </span>
                        ))}
                      </div>
                    ) : (
                      <span className="sub">{finRes.data.billing_cycle} 账期暂无独立扣费项</span>
                    )
                  ) : (
                    <span className="sub">读取中…</span>
                  )}
                </dd>
              </dl>
            </div>

            <div className="card compact">
              <div className="card-head" style={{ marginBottom: 10 }}>
                <h2>保护豁免与资源操作</h2>
              </div>
              <div className="rowline" style={{ paddingTop: 0 }}>
                <div className="grow">
                  <b>临时豁免自动关机</b>
                  <div className="sub">在豁免窗口内，即使账号触发额度保护线也跳过本实例</div>
                </div>
                {exemptActive ? (
                  <div className="inline">
                    <Badge level="warning">豁免至 {stamp(i.quota_exemption_until!)}</Badge>
                    <button className="btn sm" onClick={() => setExempt(null)}>取消豁免</button>
                  </div>
                ) : (
                  <div className="inline">
                    <button className="btn sm" onClick={() => setExempt(3600)}>1 小时</button>
                    <button className="btn sm" onClick={() => setExempt(6 * 3600)}>6 小时</button>
                    <button className="btn sm" onClick={() => setExempt(24 * 3600)}>24 小时</button>
                  </div>
                )}
              </div>

              {i.kind === "ecs" && (
                <div className="rowline" style={{ paddingBottom: 0 }}>
                  <div className="grow">
                    <b>弹性公网 IP 与生命周期</b>
                    <div className="sub">安全轮换受管 EIP，或在停机后释放按量 / 抢占式实例</div>
                  </div>
                  <div className="inline">
                    <button className="btn sm" onClick={() => setShowChangeEIP(true)}>更换弹性 IP</button>
                    {i.status === "stopped" && (i.charge_type === "pay_as_you_go" || i.charge_type === "spot") && (
                      <button className="btn sm danger" onClick={() => setShowRelease(true)}>释放实例</button>
                    )}
                  </div>
                </div>
              )}
            </div>

            <div className="card compact">
              <div className="card-head" style={{ marginBottom: 10 }}>
                <h2>实时采集程序（hm-agent）</h2>
                <div className="spacer" />
                <Badge level={pl}>{pt}</Badge>
              </div>
              <dl className="kv">
                <dt>程序状态</dt>
                <dd>
                  <Badge level={al}>{at}</Badge>
                  {i.agent.version ? <span className="mono"> · {ver(i.agent.version)}</span> : ""}
                  {i.agent.update_available && <span className="sub"> · 有新版本可平滑升级</span>}
                </dd>
                <dt>保护说明</dt>
                <dd className="sub">{pd}</dd>
                {i.agent.last_report_at && (
                  <>
                    <dt>最近上报</dt>
                    <dd>{stamp(i.agent.last_report_at)}（{ago(i.agent.last_report_at, now)}）</dd>
                  </>
                )}
                {i.agent.offline_since && (
                  <>
                    <dt>掉线时长</dt>
                    <dd>{duration((now - new Date(i.agent.offline_since).getTime()) / 1000)}</dd>
                  </>
                )}
                {i.agent.worst_case && (
                  <>
                    <dt>保守推算速率</dt>
                    <dd>
                      {i.agent.worst_case.basis === "recent_peak" ? "掉线前 10 分钟峰值" : "实例带宽上限"} · {rate(i.agent.worst_case.rate_bytes_per_second)}
                    </dd>
                  </>
                )}
                {i.realtime && (
                  <>
                    <dt>当前实时速率</dt>
                    <dd>↑ {rate(i.realtime.tx_bytes_per_second)} · ↓ {rate(i.realtime.rx_bytes_per_second)}</dd>
                  </>
                )}
              </dl>
              {i.agent.status === "offline" && (
                <p className="note">
                  排查建议：登录实例执行 <span className="mono">systemctl status hm-agent</span>，并确认实例可访问面板地址。
                </p>
              )}
              <div className="inline" style={{ marginTop: 12, justifyContent: "flex-end" }}>
                {i.agent.status !== "not_installed" && (
                  <button className="btn sm danger" onClick={() => setRevoke(true)}>撤销凭证</button>
                )}
                {i.agent.update_available && (
                  <button className="btn sm" onClick={onInstall}>获取升级命令</button>
                )}
                {canInstall(i) && (
                  <button className="btn sm primary" onClick={onInstall}>安装采集程序</button>
                )}
              </div>
            </div>
          </div>
        )}

        {tab === "traffic" && <InstanceTrafficTab i={i} />}
        {tab === "meta" && <InstanceMetaTab i={i} />}
      </Drawer>

      {revoke && (
        <Confirm
          title={`撤销 ${i.name} 的采集程序凭证？`}
          okText="确认撤销"
          danger
          onClose={() => setRevoke(false)}
          body="撤销后，该实例上的采集程序将立即停止上报并恢复为“未安装”状态。后续如需恢复，重新生成并执行一次安装命令即可。"
          onOk={async () => {
            await api.revokeAgentToken(i.id);
            instances.reload();
            toast("已撤销采集凭证");
          }}
        />
      )}

      {powerConfirm && (
        <Modal
          title={`确认将 ${i.name} ${powerConfirm === "start" ? "开机" : powerConfirm === "stop" ? "关机" : "重启"}？`}
          onClose={() => setPowerConfirm(null)}
        >
          {i.is_server_self && powerConfirm !== "start" && (
            <div className="banner critical" style={{ marginBottom: 12 }}>
              <div className="body"><b>这是运行 Harbormaster 面板自身的实例！</b> 关机或重启期间将无法访问面板。</div>
            </div>
          )}
          {powerConfirm === "stop" && i.supports_stop_charging ? (
            <div className="stack" style={{ marginBottom: 12 }}>
              <SegToggle
                label="关机方式"
                value={stopMode}
                onChange={setStopMode}
                options={[
                  ["keep_charging", STOP_MODE.keep_charging[0]],
                  ["stop_charging", STOP_MODE.stop_charging[0]],
                ]}
              />
              <div className="field-help">{STOP_MODE[stopMode][1]}</div>
            </div>
          ) : (
            <p>
              {powerConfirm === "stop"
                ? "该实例将执行普通停机（包年包月或轻量应用服务器不支持节省停机）。"
                : `将向阿里云发送${powerConfirm === "start" ? "开机" : "重启"}指令并等待状态同步。`}
            </p>
          )}
          <div className="actions">
            <button className="btn" onClick={() => setPowerConfirm(null)}>取消</button>
            <button
              className={`btn ${powerConfirm === "start" ? "primary" : "danger"}`}
              disabled={powerBusy}
              onClick={() => runPower(powerConfirm)}
            >
              {powerBusy && <Spinner />} 确认{powerConfirm === "start" ? "开机" : powerConfirm === "stop" ? "关机" : "重启"}
            </button>
          </div>
        </Modal>
      )}

      {showChangeEIP && (
        <ChangeEIPModal
          i={i}
          enabled={Boolean(sysInfo?.eip_manage_enabled)}
          onClose={() => setShowChangeEIP(false)}
          onDone={() => {
            setShowChangeEIP(false);
            reloadAll();
          }}
        />
      )}

      {showRelease && (
        <ReleaseECSModal
          i={i}
          onClose={() => setShowRelease(false)}
          onReleased={() => {
            setShowRelease(false);
            onClose();
            reloadAll();
          }}
        />
      )}
    </>
  );
}

function InstanceTrafficTab({ i }: { i: Instance }) {
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
          note: "数据来自 hm-agent 本地小时归档。",
        };
      }
      const raw = await api.agentNetworkHistory(i.id, { from, to, limit: 300 });
      const pts = [...raw.items].reverse().filter((s) => s.interval);
      return {
        mode: "raw" as const,
        unit: "Mbps",
        labels: pts.map((s) => (range === "24h" ? time(s.sampled_at) : stamp(s.sampled_at))),
        tx: pts.map((s) => (s.interval!.tx_bytes_per_second * 8) / 1e6),
        rx: pts.map((s) => (s.interval!.rx_bytes_per_second * 8) / 1e6),
        note: "数据来自 hm-agent 10 秒级实时样本。",
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
      note: "未安装 hm-agent，当前展示阿里云云监控分钟级带宽曲线。",
    };
  }, [i.id, range, hasAgent]);

  return (
    <div className="card compact stack">
      <div className="inline">
        <SegToggle label="时间范围" value={range} onChange={setRange} options={[["24h", "过去 24 小时"], ["7d", "过去 7 天"]]} />
        <div className="spacer" />
        {res.data && <span className="sub">{res.data.note}</span>}
      </div>
      <Async res={res} lines={6}>
        {(d) =>
          d.labels.length < 2 ? (
            <EmptyState title="暂无足够历史数据点">刚启动或刚安装采集程序后，稍等片刻即可生成曲线。</EmptyState>
          ) : (
            <>
              <div className="legend">
                <span><i className="line" style={{ background: "var(--series-1)" }} />出方向（{d.unit}）</span>
                <span><i className="line" style={{ background: "var(--series-3)" }} />入方向（{d.unit}）</span>
              </div>
              <LineChart
                ariaLabel={`${i.name} 公网收发历史曲线`}
                labels={d.labels}
                unit={d.unit}
                digits={2}
                series={[
                  { name: "出方向", color: "var(--series-1)", data: d.tx },
                  { name: "入方向", color: "var(--series-3)", data: d.rx },
                ]}
              />
            </>
          )
        }
      </Async>
    </div>
  );
}

function InstanceMetaTab({ i }: { i: Instance }) {
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
      toast("分组与备注已保存");
    } catch (err) {
      toast(errMsg(err), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="card compact stack" onSubmit={submit}>
      <div>
        <label className="lbl" htmlFor="im-group">分组（单选归类，如：出口节点、核心业务）</label>
        <input id="im-group" type="text" value={group} onChange={(e) => setGroup(e.target.value)} maxLength={40} placeholder="留空表示不分组" />
      </div>
      <div>
        <label className="lbl" htmlFor="im-tags">标签（逗号分隔，最多 10 个）</label>
        <input id="im-tags" type="text" value={tagsStr} onChange={(e) => setTagsStr(e.target.value)} placeholder="例如：主力, 香港, 晚高峰" />
      </div>
      <div>
        <label className="lbl" htmlFor="im-note">本地备注</label>
        <textarea id="im-note" rows={4} value={note} onChange={(e) => setNote(e.target.value)} maxLength={1000} placeholder="记录机器用途或端口说明，仅保存在本地面板数据库，不会写回阿里云。" />
      </div>
      <div className="inline" style={{ justifyContent: "flex-end" }}>
        <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存备注</button>
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
    api.agentInstallCommand(i.id).then(
      (c) => {
        setCmd(c);
        setStartedAt(Date.now());
      },
      (e) => setError(errMsg(e)),
    );
  };
  useEffect(generate, [i.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const left = cmd ? Math.max(0, (new Date(cmd.expires_at).getTime() - now) / 1000) : 0;
  const expired = !!cmd && left <= 0 && !online;
  const waited = (now - startedAt) / 1000;

  return (
    <Modal title={`安装 / 升级采集程序：${i.name}`} onClose={onClose} wide>
      <p>hm-agent 仅读取网卡字节计数并每 10 秒上报一次，不监听任何入站端口，也不具备远程命令执行权限。</p>
      <ol className="steps">
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">复制一次性安装命令</div>
            {error ? (
              <div className="field-error">{error} <button className="linkbtn" onClick={generate}>重试</button></div>
            ) : !cmd ? (
              <div className="sub"><Spinner /> 正在生成…</div>
            ) : (
              <>
                <div className="codebox" style={{ marginTop: 6 }}>
                  <code>{cmd.command}</code>
                  <CopyButton text={cmd.command} className="btn" />
                </div>
                <div className="sub" style={{ marginTop: 4 }}>
                  {online
                    ? "命令已生效。"
                    : expired
                      ? <>命令已过期。<button className="linkbtn" onClick={generate}>重新生成</button></>
                      : `单次有效，${duration(left)}后过期，仅绑定 ${i.name}。`}
                </div>
              </>
            )}
          </div>
        </li>
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">在 {i.name} 终端执行</div>
            <div className="sub">支持主流 x86_64 / arm64 Linux 发行版（systemd）。</div>
          </div>
        </li>
        <li className={online ? "done" : ""}>
          <div>
            <div className="st">自动连接确认</div>
            {online ? (
              <div style={{ marginTop: 4 }}>
                <Badge level="good">已在线（最新版）</Badge>{" "}
                {i.realtime && <span className="sub">当前 ↑ {rate(i.realtime.tx_bytes_per_second)} · ↓ {rate(i.realtime.rx_bytes_per_second)}</span>}
              </div>
            ) : cmd && !expired ? (
              <div className="sub">
                <Spinner /> 等待首次心跳上报（已等待 {duration(waited)}）…
                {waited > 90 && <div style={{ marginTop: 4 }}>等待较久，请检查终端输出并确认机器可访问面板域名。</div>}
              </div>
            ) : (
              <div className="sub">执行命令后此处将自动变为“已在线”。</div>
            )}
          </div>
        </li>
      </ol>
      <div className="actions">
        <button className={`btn ${online ? "primary" : ""}`} onClick={onClose}>{online ? "完成" : "后台等待"}</button>
      </div>
    </Modal>
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
      toast("已提交更换弹性公网 IP 任务…");
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
            服务端默认关闭弹性公网 IP 写操作（<span className="mono">HM_EIP_MANAGE_ENABLED=false</span>）。如需启用请在服务端环境变量开启。
          </Banner>
        )}
        {hasFixedIP && (
          <Banner level="critical">
            该实例带有固定公网 IP（非弹性 EIP），不支持直接轮换 EIP。
          </Banner>
        )}
        <p className="note" style={{ margin: 0 }}>
          安全保护：仅允许为空实例绑定新 EIP，或轮换由 Harbormaster 创建的受管 EIP；<b>用户原有的自有 EIP 绝不会被自动解绑或释放</b>。
        </p>
        <div className="grid g2">
          <div>
            <label className="lbl">计费方式</label>
            <select value={chargeType} onChange={(e) => setChargeType(e.target.value as "PayByTraffic" | "PayByBandwidth")}>
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
            {busy && <Spinner />} 确认更换 IP
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
                  服务端默认关闭实例释放开关（<span className="mono">HM_INSTANCE_RELEASE_ENABLED=false</span>），当前仅展示只读释放影响预览。
                </Banner>
              )}
              {pv.block_reasons.length > 0 && (
                <Banner level="critical">不满足安全释放条件：{pv.block_reasons.join("、")}</Banner>
              )}
              {pv.impact && (
                <dl className="kv">
                  <dt>实例名称</dt><dd><b>{pv.impact.instance_name}</b>（<span className="mono">{pv.impact.cloud_instance_id}</span>）</dd>
                  <dt>计算与挂载盘</dt><dd>不可恢复销毁（{pv.impact.disks.length} 块云盘）</dd>
                  <dt>弹性公网 IP</dt><dd>保留不删除（{pv.impact.eips_action}）</dd>
                  <dt>手动快照</dt><dd>保留不删除（{pv.impact.manual_snapshots_action}）</dd>
                </dl>
              )}
              {canExec && (
                <div>
                  <label className="lbl">
                    请输入完整实例名称 <b>{pv.impact?.instance_name ?? i.name}</b> 确认永久释放
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
      toast("已提交创建 ECS 任务，正在编排网络与实例…");
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
            服务端默认关闭创建实例开关（<span className="mono">HM_INSTANCE_CREATE_ENABLED=false</span>）。可先预览地域规格目录，如需启用请在服务端开启该配置。
          </Banner>
        )}
        <div className="grid g2">
          <div>
            <label className="lbl">云账号</label>
            <select value={accountId} onChange={(e) => setAccountId(e.target.value)}>
              {enabledAccs.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="lbl">地域</label>
            <select
              value={regionId}
              onChange={(e) => {
                setRegionId(e.target.value);
                setZoneId("");
                setInstanceType("");
                setImageId("");
              }}
            >
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
                    <select value={zoneId} onChange={(e) => setZoneId(e.target.value)}>
                      {cat.zones.map((z) => (
                        <option key={z.id} value={z.id}>{z.name || z.id}</option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="lbl">实例规格</label>
                    <select value={instanceType} onChange={(e) => setInstanceType(e.target.value)}>
                      {cat.instance_types.map((t) => (
                        <option key={t.id} value={t.id}>{t.id} ({t.cpu}C / {t.memory_gib}G)</option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="lbl">系统镜像</label>
                    <select value={imageId} onChange={(e) => setImageId(e.target.value)}>
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
                    <label className="lbl">计费模式</label>
                    <select value={chargeType} onChange={(e) => setChargeType(e.target.value as "PostPaid" | "Spot")}>
                      <option value="PostPaid">按量付费（PostPaid）</option>
                      <option value="Spot">抢占式实例（Spot）</option>
                    </select>
                  </div>
                  <div>
                    <label className="lbl">系统盘（GiB）</label>
                    <input type="number" min={20} max={500} value={diskGiB} onChange={(e) => setDiskGiB(e.target.value)} />
                  </div>
                </div>
                <div className="grid g2">
                  <div>
                    <label className="lbl">公网带宽计费</label>
                    <select value={netCharge} onChange={(e) => setNetCharge(e.target.value as "PayByTraffic" | "PayByBandwidth")}>
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
                  <span>若无可用网络则自动创建 VPC、交换机与安全组</span>
                </div>
                {!autoNet && (
                  <div className="grid g2">
                    <div>
                      <label className="lbl">已有 VPC</label>
                      <select value={vpcId} onChange={(e) => setVpcId(e.target.value)}>
                        {cat.vpcs.map((v) => (
                          <option key={v.id} value={v.id}>{v.name || v.id} ({v.cidr_block})</option>
                        ))}
                      </select>
                    </div>
                    <div>
                      <label className="lbl">已有交换机</label>
                      <select value={vswId} onChange={(e) => setVswId(e.target.value)}>
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
          <span>我已知晓创建按量或抢占式 ECS 将立即在阿里云产生资源费用</span>
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
