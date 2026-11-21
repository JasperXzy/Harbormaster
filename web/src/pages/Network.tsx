// M3 网络延迟与线路监测（F10.1-F10.11）：延迟矩阵、24h/7d 延迟与丢包曲线、单向/双向去回程路由追踪与导出、目标与组合管理、每日线路快照、离线 IP 归属库状态。
import { useCallback, useEffect, useMemo, useState } from "react";
import { api, downloadText, randomIdempotencyKey } from "../api/client";
import type * as T from "../api/types";
import { LineChart } from "../components/charts/LineChart";
import { Async, Badge, Banner, Card, Confirm, CopyButton, EmptyState, errMsg, Modal, SegToggle, Spinner, Switch, useToast, type Level } from "../components/ui";
import { useShared } from "../lib/data";
import { bytes, num, stamp } from "../lib/format";
import { useResource } from "../lib/useResource";

type NetTab = "matrix" | "trace" | "manage";

const PRESET_TARGETS: { name: string; host: string; tcp_port: number | null; desc: string }[] = [
  { name: "Cloudflare DNS", host: "1.1.1.1", tcp_port: null, desc: "ICMP · Anycast" },
  { name: "Google DNS", host: "8.8.8.8", tcp_port: null, desc: "ICMP · 海外连通" },
  { name: "阿里公共 DNS", host: "223.5.5.5", tcp_port: null, desc: "ICMP · 中国内地连通" },
  { name: "Cloudflare HTTPS", host: "1.1.1.1", tcp_port: 443, desc: "TCP 443 · 建连探测" },
];

const ROUTE_LABEL_TEXT: Record<string, string> = {
  cn2_gia: "电信 CN2 GIA",
  cn2_gt: "电信 CN2 GT",
  cn2: "电信 CN2 (AS4809)",
  ct_163: "电信 163 骨干 (AS4134)",
  cu_9929: "联通精品网 (AS9929)",
  cu_4837: "联通 169 骨干 (AS4837)",
  cm_cmi: "移动 CMI (AS58453)",
  cm_9808: "移动骨干 (AS9808)",
  aliyun: "阿里云骨干 (AS45102)",
};

function routeTagText(lb: T.TraceDetail["enrichment"] extends infer E ? (E extends { labels: (infer L)[] } ? L : never) : never): string {
  if (ROUTE_LABEL_TEXT[lb.code]) return ROUTE_LABEL_TEXT[lb.code];
  const tier = lb.service_tier ? ` · ${lb.service_tier}` : "";
  return `${lb.code.toUpperCase()} (AS${lb.asn}${tier})`;
}

function runBadge(run: T.ProbeRun | null | undefined): [Level, string] {
  if (!run) return ["neutral", "尚无记录"];
  if (run.status === "queued" || run.status === "running") return ["neutral", "探测中"];
  if (run.status === "skipped") return ["warning", "已跳过"];
  if (run.status !== "succeeded" || !run.result?.summary) return ["critical", "失败"];
  const s = run.result.summary;
  if (s.succeeded === 0 || s.loss_percent >= 100) return ["critical", "不可达"];
  if (s.loss_percent >= 20 || (s.average_ms !== null && s.average_ms >= 250)) return ["serious", `${s.average_ms !== null ? `${num(s.average_ms, 1)} ms` : "高丢包"}`];
  if (s.loss_percent > 0 || (s.average_ms !== null && s.average_ms >= 120)) return ["warning", `${s.average_ms !== null ? `${num(s.average_ms, 1)} ms` : "轻微丢包"}`];
  return ["good", s.average_ms !== null ? `${num(s.average_ms, 1)} ms` : "正常"];
}

function downloadBlobText(text: string, filename: string) {
  const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export function Network() {
  const [tab, setTab] = useState<NetTab>("matrix");
  const toast = useToast();
  const { instances } = useShared();

  const pointsRes = useResource(api.networkPoints, [], { refreshMs: 30_000 });
  const targetsRes = useResource(api.networkTargets, [], { refreshMs: 30_000 });
  const plansRes = useResource(api.networkPlans, [], { refreshMs: 15_000 });

  const reloadCore = useCallback(() => {
    pointsRes.reload();
    targetsRes.reload();
    plansRes.reload();
  }, [pointsRes, targetsRes, plansRes]);

  const [selectedPlanId, setSelectedPlanId] = useState<string | null>(null);
  const [quickPlanPair, setQuickPlanPair] = useState<{ point: T.ProbePoint; target: T.NetworkTarget } | null>(null);

  const points = pointsRes.data ?? [];
  const targets = targetsRes.data ?? [];
  const plans = plansRes.data ?? [];
  const selectedPlan = useMemo(() => plans.find((p) => p.id === selectedPlanId) ?? plans[0] ?? null, [plans, selectedPlanId]);

  const addPreset = async (preset: (typeof PRESET_TARGETS)[number]) => {
    try {
      await api.createNetworkTarget({
        revision: 0,
        name: preset.name,
        kind: "custom",
        host: preset.host,
        instance_id: null,
        tcp_port: preset.tcp_port,
        enabled: true,
      });
      toast(`已添加目标「${preset.name}」`);
      targetsRes.reload();
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  return (
    <>
      <div style={{ display: "flex", alignItems: "center", gap: 12, flexWrap: "wrap" }}>
        <SegToggle<NetTab>
          value={tab}
          onChange={setTab}
          label="网络功能切换"
          options={[
            ["matrix", "延迟矩阵与曲线"],
            ["trace", "线路查询 (Traceroute)"],
            ["manage", "目标、组合与每日快照"],
          ]}
        />
        <div className="spacer" />
        <button className="btn sm" onClick={reloadCore}>
          刷新探测状态
        </button>
      </div>

      {tab === "matrix" && (
        <>
          <Card
            title="网络延迟矩阵"
            sub="测速点 × 探测目标；点击任意已启用单元格查看 24 小时 / 7 天延迟、抖动与丢包曲线"
            right={
              <button className="btn sm" onClick={() => setTab("manage")}>
                管理目标与组合
              </button>
            }
          >
            <Async res={plansRes}>
              {() => {
                if (targets.length === 0) {
                  return (
                    <EmptyState
                      title="还没有配置探测目标"
                      action={
                        <div style={{ display: "flex", gap: 8, flexWrap: "wrap", justifyContent: "center" }}>
                          {PRESET_TARGETS.map((pr) => (
                            <button key={pr.name} className="btn sm" onClick={() => addPreset(pr)}>
                              + {pr.name} ({pr.desc})
                            </button>
                          ))}
                          <button className="btn primary sm" onClick={() => setTab("manage")}>
                            自定义目标
                          </button>
                        </div>
                      }
                    >
                      可以先一键添加常用公共节点，或添加自己的域名、IP 与纳管实例作为探测目标。
                    </EmptyState>
                  );
                }
                if (points.length === 0) {
                  return <EmptyState title="暂无可用测速点">面板服务端或已安装 hm-agent 的在线实例会自动出现在这里。</EmptyState>;
                }
                return (
                  <div className="table-wrap">
                    <table className="tbl">
                      <thead>
                        <tr>
                          <th>测速点 \ 目标</th>
                          {targets.map((t) => (
                            <th key={t.id}>
                              <div>{t.name}</div>
                              <div className="sub" style={{ fontWeight: 400 }}>
                                {t.kind === "custom" ? t.host : "纳管实例"}
                                {t.tcp_port ? `:${t.tcp_port}` : ""}
                              </div>
                            </th>
                          ))}
                        </tr>
                      </thead>
                      <tbody>
                        {points.map((pt) => (
                          <tr key={pt.id}>
                            <td>
                              <div style={{ fontWeight: 600 }}>{pt.name}</div>
                              <div className="sub">
                                {pt.point_kind === "server" ? "面板服务端" : "实例 Agent"} · {pt.available ? "在线" : "不可用"}
                              </div>
                            </td>
                            {targets.map((tg) => {
                              const cellPlans = plans.filter(
                                (pl) =>
                                  pl.target_id === tg.id &&
                                  pl.point_kind === pt.point_kind &&
                                  (pt.point_kind === "server" ? pl.instance_id === null : pl.instance_id === pt.instance_id),
                              );
                              const pl = cellPlans[0];
                              if (!pl) {
                                return (
                                  <td key={tg.id}>
                                    <button
                                      type="button"
                                      className="btn sm"
                                      style={{ opacity: 0.75 }}
                                      onClick={() => setQuickPlanPair({ point: pt, target: tg })}
                                    >
                                      + 启用探测
                                    </button>
                                  </td>
                                );
                              }
                              const [lvl, text] = runBadge(pl.last_run);
                              const active = selectedPlan?.id === pl.id;
                              const loss = pl.last_run?.result?.summary?.loss_percent;
                              return (
                                <td key={tg.id}>
                                  <button
                                    type="button"
                                    onClick={() => setSelectedPlanId(pl.id)}
                                    style={{
                                      all: "unset",
                                      cursor: "pointer",
                                      display: "inline-flex",
                                      flexDirection: "column",
                                      gap: 3,
                                      padding: "6px 10px",
                                      borderRadius: "var(--r-md)",
                                      border: active ? "1.5px solid var(--accent)" : "1px solid var(--border)",
                                      background: active ? "var(--surface-2)" : "transparent",
                                    }}
                                  >
                                    <Badge level={pl.enabled ? lvl : "neutral"}>{pl.enabled ? text : "已停用"}</Badge>
                                    <span className="sub" style={{ fontSize: "var(--fs-xs)" }}>
                                      {pl.method.toUpperCase()}
                                      {loss != null ? ` · 丢包 ${num(loss, 0)}%` : ""}
                                    </span>
                                  </button>
                                </td>
                              );
                            })}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                );
              }}
            </Async>
          </Card>

          {selectedPlan && <PlanHistoryCard plan={selectedPlan} points={points} targets={targets} />}

          {quickPlanPair && (
            <PlanModal
              initialPoint={quickPlanPair.point}
              initialTarget={quickPlanPair.target}
              points={points}
              targets={targets}
              onSaved={(created) => {
                setQuickPlanPair(null);
                plansRes.reload();
                setSelectedPlanId(created.id);
              }}
              onClose={() => setQuickPlanPair(null)}
            />
          )}
        </>
      )}

      {tab === "trace" && <TracerouteSection points={points} targets={targets} instances={instances.data?.items ?? []} />}

      {tab === "manage" && (
        <ManageNetworkSection
          points={points}
          targets={targets}
          plans={plans}
          instances={instances.data?.items ?? []}
          onReload={reloadCore}
          onAddPreset={addPreset}
        />
      )}
    </>
  );
}

/* ---------- 单组合 24h / 7d 历史曲线 ---------- */
function PlanHistoryCard({ plan, points, targets }: { plan: T.ProbePlan; points: T.ProbePoint[]; targets: T.NetworkTarget[] }) {
  const [range, setRange] = useState<"24h" | "7d">("24h");
  const fetchRuns = useCallback(async () => {
    const toDate = new Date(Date.now() - 5_000);
    const spanMs = range === "24h" ? 24 * 3600_000 : 7 * 86400_000 - 120_000;
    const fromDate = new Date(toDate.getTime() - spanMs);
    const page = await api.probeRuns(plan.id, { from: fromDate.toISOString(), to: toDate.toISOString(), limit: 200 });
    return [...page.items].reverse();
  }, [plan.id, range]);

  const runsRes = useResource(fetchRuns, [plan.id, range], { refreshMs: 30_000 });
  const ptName = points.find((p) => p.point_kind === plan.point_kind && (p.point_kind === "server" || p.instance_id === plan.instance_id))?.name ?? plan.point_kind;
  const tgName = targets.find((t) => t.id === plan.target_id)?.name ?? plan.target_id;

  return (
    <Card
      title={`延迟与丢包趋势：${plan.name}`}
      sub={`${ptName} → ${tgName} · ${plan.method.toUpperCase()} (${plan.family.toUpperCase()}) · 每 ${Math.round((plan.frequency_seconds ?? 300) / 60)} 分钟`}
      right={
        <SegToggle<"24h" | "7d">
          value={range}
          onChange={setRange}
          label="时间范围"
          options={[
            ["24h", "最近 24 小时"],
            ["7d", "最近 7 天"],
          ]}
        />
      }
    >
      <Async res={runsRes}>
        {(runs) => {
          if (runs.length === 0) {
            return <EmptyState title="所选时间段还没有探测记录">新创建的测速组合会在后台调度器运行后自动产生数据点。</EmptyState>;
          }
          const labels = runs.map((r) => stamp(r.created_at));
          const avgSeries = runs.map((r) => r.result?.summary?.average_ms ?? null);
          const maxSeries = runs.map((r) => r.result?.summary?.maximum_ms ?? null);
          const jitterSeries = runs.map((r) => r.result?.summary?.jitter_ms ?? null);
          const lossSeries = runs.map((r) => r.result?.summary?.loss_percent ?? null);

          const validAvgs = avgSeries.filter((v): v is number => v !== null);
          const validJitters = jitterSeries.filter((v): v is number => v !== null);
          const validLosses = lossSeries.filter((v): v is number => v !== null);
          const meanRtt = validAvgs.length ? validAvgs.reduce((a, b) => a + b, 0) / validAvgs.length : null;
          const meanJitter = validJitters.length ? validJitters.reduce((a, b) => a + b, 0) / validJitters.length : null;
          const meanLoss = validLosses.length ? validLosses.reduce((a, b) => a + b, 0) / validLosses.length : null;

          return (
            <div style={{ display: "grid", gap: 16 }}>
              <div className="statstrip">
                <div className="stat">
                  <div className="label">样本批次数</div>
                  <div className="value">{runs.length}</div>
                </div>
                <div className="stat">
                  <div className="label">平均延迟 (RTT)</div>
                  <div className="value">{meanRtt !== null ? `${num(meanRtt, 1)} ms` : "—"}</div>
                </div>
                <div className="stat">
                  <div className="label">平均抖动 (Jitter)</div>
                  <div className="value">{meanJitter !== null ? `${num(meanJitter, 1)} ms` : "—"}</div>
                </div>
                <div className="stat">
                  <div className="label">{plan.method === "tcp" ? "建连失败率" : "平均丢包率"}</div>
                  <div className="value">{meanLoss !== null ? `${num(meanLoss, 1)}%` : "—"}</div>
                </div>
              </div>
              <div className="g2">
                <div>
                  <div className="sub" style={{ marginBottom: 6 }}>
                    往返延迟与峰值 (ms)
                  </div>
                  <LineChart
                    labels={labels}
                    unit="ms"
                    digits={1}
                    height={210}
                    ariaLabel="往返延迟趋势图"
                    series={[
                      { name: "平均 RTT", color: "var(--accent)", data: avgSeries },
                      { name: "最大 RTT", color: "var(--warning)", data: maxSeries, dashed: true },
                    ]}
                  />
                </div>
                <div>
                  <div className="sub" style={{ marginBottom: 6 }}>
                    {plan.method === "tcp" ? "TCP 建连失败比例 (%)" : "探测未响应 / 丢包比例 (%)"}
                  </div>
                  <LineChart
                    labels={labels}
                    unit="%"
                    digits={1}
                    height={210}
                    ariaLabel="丢包比例趋势图"
                    series={[{ name: "失败比例", color: "var(--critical)", data: lossSeries }]}
                  />
                </div>
              </div>
            </div>
          );
        }}
      </Async>
    </Card>
  );
}

/* ---------- 线路查询：单向 & 去回程双向 ---------- */
function TracerouteSection({ points, targets, instances }: { points: T.ProbePoint[]; targets: T.NetworkTarget[]; instances: T.Instance[] }) {
  const toast = useToast();
  const [mode, setMode] = useState<"single" | "duplex">("single");

  // 单向表单
  const [pointSel, setPointSel] = useState<string>("server");
  const [destMode, setDestMode] = useState<"target" | "host">(targets.length > 0 ? "target" : "host");
  const [targetId, setTargetId] = useState<string>(targets[0]?.id ?? "");
  const [customHost, setCustomHost] = useState<string>("1.1.1.1");
  const [family, setFamily] = useState<"ipv4" | "ipv6">("ipv4");
  const [method, setMethod] = useState<"icmp" | "tcp">("icmp");
  const [port, setPort] = useState<number>(443);

  // 双向表单
  const agentInstances = useMemo(() => instances.filter((i) => i.agent.status === "online" && i.public_ips.length > 0), [instances]);
  const [fromInst, setFromInst] = useState<string>(agentInstances[0]?.id ?? "");
  const [toInst, setToInst] = useState<string>(agentInstances[1]?.id ?? "");

  const [submitting, setSubmitting] = useState(false);
  const [activeTraceId, setActiveTraceId] = useState<string | null>(null);
  const [activeTrace, setActiveTrace] = useState<T.TraceDetail | null>(null);
  const [activeDuplexId, setActiveDuplexId] = useState<string | null>(null);
  const [activeDuplex, setActiveDuplex] = useState<T.DuplexTrace | null>(null);

  const fetchHistory = useCallback(async () => {
    const toDate = new Date(Date.now() - 5_000);
    const fromDate = new Date(toDate.getTime() - 7 * 86400_000);
    const page = await api.networkTraces({ from: fromDate.toISOString(), to: toDate.toISOString(), limit: 25 });
    return page.items;
  }, []);
  const historyRes = useResource(fetchHistory, [], { refreshMs: 20_000 });

  useEffect(() => {
    if (!targetId && targets[0]) setTargetId(targets[0].id);
  }, [targets, targetId]);

  useEffect(() => {
    if (!fromInst && agentInstances[0]) setFromInst(agentInstances[0].id);
    if (!toInst && agentInstances[1]) setToInst(agentInstances[1].id);
  }, [agentInstances, fromInst, toInst]);

  // 轮询正在执行或富化中的单向追踪
  useEffect(() => {
    if (!activeTraceId) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const d = await api.networkTrace(activeTraceId);
        if (cancelled) return;
        setActiveTrace(d);
        const running = d.status === "queued" || d.status === "running" || d.enrichment_status === "waiting_result" || d.enrichment_status === "queued" || d.enrichment_status === "running";
        if (running) timer = setTimeout(poll, 1500);
        else historyRes.reload();
      } catch (e) {
        if (!cancelled) toast(errMsg(e), true);
      }
    };
    poll();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [activeTraceId, historyRes, toast]);

  // 轮询正在执行或富化中的双向追踪
  useEffect(() => {
    if (!activeDuplexId) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const d = await api.duplexTrace(activeDuplexId);
        if (cancelled) return;
        setActiveDuplex(d);
        const fRun = d.forward.status === "queued" || d.forward.status === "running" || d.forward.enrichment_status === "waiting_result" || d.forward.enrichment_status === "queued" || d.forward.enrichment_status === "running";
        const rRun = d.reverse.status === "queued" || d.reverse.status === "running" || d.reverse.enrichment_status === "waiting_result" || d.reverse.enrichment_status === "queued" || d.reverse.enrichment_status === "running";
        if (fRun || rRun) timer = setTimeout(poll, 1500);
        else historyRes.reload();
      } catch (e) {
        if (!cancelled) toast(errMsg(e), true);
      }
    };
    poll();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [activeDuplexId, historyRes, toast]);

  const submitSingle = async () => {
    setSubmitting(true);
    try {
      const isServer = pointSel === "server";
      const tg = targets.find((x) => x.id === targetId);
      const body: T.TraceSubmission = {
        request_key: randomIdempotencyKey("trc"),
        point_kind: isServer ? "server" : "agent",
        instance_id: isServer ? null : pointSel,
        target_id: destMode === "target" && tg ? tg.id : null,
        target_revision: destMode === "target" && tg ? tg.revision : null,
        host: destMode === "host" ? customHost.trim() : null,
        family,
        method,
        port: method === "tcp" ? port : 0,
        max_hops: 30,
        count: 3,
        timeout_ms: 1000,
      };
      const res = await api.submitTrace(body);
      setActiveDuplexId(null);
      setActiveDuplex(null);
      setActiveTraceId(res.trace_id);
      toast("已发起线路追踪，正在逐跳采集中…");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setSubmitting(false);
    }
  };

  const submitDuplex = async () => {
    if (!fromInst || !toInst || fromInst === toInst) {
      toast("请选择两台不同的在线实例", true);
      return;
    }
    setSubmitting(true);
    try {
      const p = method === "tcp" ? port : 0;
      const res = await api.submitDuplexTrace({
        request_key: randomIdempotencyKey("dpx"),
        from_instance_id: fromInst,
        to_instance_id: toInst,
        family,
        method,
        forward_port: p,
        reverse_port: p,
        max_hops: 30,
        count: 3,
        timeout_ms: 1000,
      });
      setActiveTraceId(null);
      setActiveTrace(null);
      setActiveDuplexId(res.duplex_id);
      toast("已同时发起去程与回程追踪…");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setSubmitting(false);
    }
  };

  const exportSingle = async (id: string) => {
    try {
      const { text, filename } = await downloadText(`/network/traces/${id}/export`, `trace-${id}.txt`);
      downloadBlobText(text, filename);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  const exportDuplex = async (id: string) => {
    try {
      const { text, filename } = await downloadText(`/network/duplex-traces/${id}/export`, `duplex-trace-${id}.txt`);
      downloadBlobText(text, filename);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  return (
    <>
      <Card
        title="发起路由追踪 (Traceroute)"
        sub="查看每一跳 IP、PTR 反向解析、ASN/运营商归属、地理位置与跨境线路特征标签"
        right={
          <SegToggle<"single" | "duplex">
            value={mode}
            onChange={setMode}
            label="追踪模式"
            options={[
              ["single", "单向追踪"],
              ["duplex", "去回程双向追踪"],
            ]}
          />
        }
      >
        {mode === "single" ? (
          <div style={{ display: "grid", gap: 12 }}>
            <div className="g3">
              <div>
                <label className="sub">起点（测速点）</label>
                <select value={pointSel} onChange={(e) => setPointSel(e.target.value)} style={{ width: "100%", marginTop: 4 }}>
                  <option value="server">面板服务端</option>
                  {points
                    .filter((p) => p.point_kind === "agent" && p.instance_id)
                    .map((p) => (
                      <option key={p.id} value={p.instance_id!} disabled={!p.trace.available}>
                        {p.name} {!p.trace.available ? "（当前不可用）" : ""}
                      </option>
                    ))}
                </select>
              </div>
              <div>
                <label className="sub">目标来源</label>
                <div style={{ display: "flex", gap: 8, marginTop: 4 }}>
                  <select value={destMode} onChange={(e) => setDestMode(e.target.value as "target" | "host")}>
                    <option value="host">输入 IP / 域名</option>
                    {targets.length > 0 && <option value="target">已保存目标</option>}
                  </select>
                  {destMode === "target" ? (
                    <select value={targetId} onChange={(e) => setTargetId(e.target.value)} style={{ flex: 1 }}>
                      {targets.map((t) => (
                        <option key={t.id} value={t.id}>
                          {t.name} ({t.kind === "custom" ? t.host : "纳管实例"})
                        </option>
                      ))}
                    </select>
                  ) : (
                    <input
                      type="text"
                      value={customHost}
                      onChange={(e) => setCustomHost(e.target.value)}
                      placeholder="例如 1.1.1.1 或 www.aliyun.com"
                      style={{ flex: 1 }}
                    />
                  )}
                </div>
              </div>
              <div>
                <label className="sub">协议与地址族</label>
                <div style={{ display: "flex", gap: 8, marginTop: 4 }}>
                  <select value={method} onChange={(e) => setMethod(e.target.value as "icmp" | "tcp")}>
                    <option value="icmp">ICMP</option>
                    <option value="tcp">TCP</option>
                  </select>
                  <select value={family} onChange={(e) => setFamily(e.target.value as "ipv4" | "ipv6")}>
                    <option value="ipv4">IPv4</option>
                    <option value="ipv6">IPv6</option>
                  </select>
                  {method === "tcp" && (
                    <input
                      type="number"
                      min={1}
                      max={65535}
                      value={port}
                      onChange={(e) => setPort(Number(e.target.value) || 443)}
                      style={{ width: 88 }}
                      placeholder="端口"
                    />
                  )}
                </div>
              </div>
            </div>
            <div style={{ display: "flex", justifyContent: "flex-end" }}>
              <button className="btn primary" disabled={submitting} onClick={submitSingle}>
                {submitting && <Spinner />} 开始单向追踪
              </button>
            </div>
          </div>
        ) : (
          <div style={{ display: "grid", gap: 12 }}>
            {agentInstances.length < 2 ? (
              <Banner level="neutral">去回程双向追踪需要至少两台在线（已安装 hm-agent 且具备公网地址）的纳管实例。</Banner>
            ) : (
              <>
                <div className="g3">
                  <div>
                    <label className="sub">实例 A（去程起点 / 回程终点）</label>
                    <select value={fromInst} onChange={(e) => setFromInst(e.target.value)} style={{ width: "100%", marginTop: 4 }}>
                      {agentInstances.map((i) => (
                        <option key={i.id} value={i.id}>
                          {i.name} ({i.public_ips[0]?.address})
                        </option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="sub">实例 B（去程终点 / 回程起点）</label>
                    <select value={toInst} onChange={(e) => setToInst(e.target.value)} style={{ width: "100%", marginTop: 4 }}>
                      {agentInstances.map((i) => (
                        <option key={i.id} value={i.id}>
                          {i.name} ({i.public_ips[0]?.address})
                        </option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="sub">协议与地址族</label>
                    <div style={{ display: "flex", gap: 8, marginTop: 4 }}>
                      <select value={method} onChange={(e) => setMethod(e.target.value as "icmp" | "tcp")}>
                        <option value="icmp">ICMP</option>
                        <option value="tcp">TCP</option>
                      </select>
                      <select value={family} onChange={(e) => setFamily(e.target.value as "ipv4" | "ipv6")}>
                        <option value="ipv4">IPv4</option>
                        <option value="ipv6">IPv6</option>
                      </select>
                      {method === "tcp" && (
                        <input type="number" min={1} max={65535} value={port} onChange={(e) => setPort(Number(e.target.value) || 443)} style={{ width: 88 }} />
                      )}
                    </div>
                  </div>
                </div>
                <div style={{ display: "flex", justifyContent: "flex-end" }}>
                  <button className="btn primary" disabled={submitting || fromInst === toInst} onClick={submitDuplex}>
                    {submitting && <Spinner />} 同时追踪去程与回程
                  </button>
                </div>
              </>
            )}
          </div>
        )}
      </Card>

      {activeTrace && (
        <Card
          title={`单向路径详情：${activeTrace.point_name} → ${activeTrace.request.host}`}
          sub={`状态：${activeTrace.status} · 归属富化：${activeTrace.enrichment_status} · ${stamp(activeTrace.created_at)}`}
          right={
            <button className="btn sm" onClick={() => exportSingle(activeTrace.id)}>
              导出文本 (.txt)
            </button>
          }
        >
          <TraceDetailView detail={activeTrace} />
        </Card>
      )}

      {activeDuplex && (
        <Card
          title={`去回程并列对比：${activeDuplex.from.name} ⇄ ${activeDuplex.to.name}`}
          sub={`${activeDuplex.from.public_address} ⇄ ${activeDuplex.to.public_address} · 正反向独立执行，不假设对称路由`}
          right={
            <button className="btn sm" onClick={() => exportDuplex(activeDuplex.id)}>
              导出双向报告 (.txt)
            </button>
          }
        >
          <div className="g2">
            <div>
              <h3 style={{ margin: "0 0 8px", fontSize: "var(--fs-md)" }}>
                去程：{activeDuplex.from.name} → {activeDuplex.to.name}
              </h3>
              <TraceDetailView detail={activeDuplex.forward} />
            </div>
            <div>
              <h3 style={{ margin: "0 0 8px", fontSize: "var(--fs-md)" }}>
                回程：{activeDuplex.to.name} → {activeDuplex.from.name}
              </h3>
              <TraceDetailView detail={activeDuplex.reverse} />
            </div>
          </div>
        </Card>
      )}

      <Card title="最近线路追踪记录" sub="保留最近 7 天内的手动与快照追踪，点击可回看逐跳详情与导出">
        <Async res={historyRes}>
          {(items) =>
            items.length === 0 ? (
              <EmptyState title="最近 7 天还没有线路追踪记录" />
            ) : (
              <div className="table-wrap">
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>起点 → 目标</th>
                      <th>协议</th>
                      <th>结果</th>
                      <th>跳数</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((tr) => (
                      <tr key={tr.id}>
                        <td className="sub">{stamp(tr.created_at)}</td>
                        <td>
                          <b>{tr.point_name}</b> → {tr.target?.name ?? tr.request.host}
                          {tr.duplex_direction && <span className="chip" style={{ marginLeft: 6 }}>{tr.duplex_direction === "forward" ? "去程" : "回程"}</span>}
                        </td>
                        <td className="sub">{tr.request.method.toUpperCase()} / {tr.request.family.toUpperCase()}</td>
                        <td>
                          <Badge level={tr.status === "succeeded" ? (tr.outcome?.reached ? "good" : "warning") : tr.status === "failed" ? "critical" : "neutral"}>
                            {tr.status === "succeeded" ? (tr.outcome?.reached ? "已到达终点" : "未达终点") : tr.status}
                          </Badge>
                        </td>
                        <td>{tr.outcome?.hop_count ?? "—"}</td>
                        <td>
                          <div style={{ display: "flex", gap: 6 }}>
                            <button
                              className="btn sm"
                              onClick={() => {
                                setActiveDuplexId(null);
                                setActiveDuplex(null);
                                setActiveTraceId(tr.id);
                              }}
                            >
                              查看逐跳
                            </button>
                            <button className="btn sm" onClick={() => exportSingle(tr.id)}>
                              导出
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Async>
      </Card>
    </>
  );
}

function TraceDetailView({ detail }: { detail: T.TraceDetail }) {
  const addrMap = useMemo(() => {
    const m = new Map<string, NonNullable<T.TraceDetail["enrichment"]>["addresses"][number]>();
    for (const a of detail.enrichment?.addresses ?? []) m.set(a.address, a);
    return m;
  }, [detail.enrichment]);

  if (detail.status === "queued" || detail.status === "running") {
    return (
      <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "18px 0" }}>
        <Spinner /> <span>正在执行逐跳探测（{detail.status}），完成后会自动刷新…</span>
      </div>
    );
  }
  if (detail.error) {
    return <Banner level="critical">追踪失败：{detail.error.code}</Banner>;
  }
  const labels = detail.enrichment?.labels ?? [];
  const enrichedHops = detail.enrichment?.hops ?? [];
  const rawHops = detail.result?.hops ?? [];

  return (
    <div style={{ display: "grid", gap: 12 }}>
      <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center" }}>
        <Badge level={detail.result?.reached ? "good" : "warning"}>
          {detail.result?.reached ? `已到达目标 (${detail.result.address})` : `停止原因：${detail.result?.stop_reason ?? "未知"}`}
        </Badge>
        {labels.length > 0 ? (
          labels.map((lb, idx) => (
            <span key={idx} className="chip" title={`出现跳数 TTL: ${lb.ttls.join(", ")}`}>
              🏷️ {routeTagText(lb)}
            </span>
          ))
        ) : (
          <span className="chip">未识别到特殊专线 ASN 标签（普通直连 / 公网 BGP）</span>
        )}
      </div>
      <div className="sub" style={{ fontSize: "var(--fs-xs)" }}>
        注：中间跳「*」仅表示该路由器未响应 TTL 超时探测，不代表真实转发丢包；ASN 与地理位置来自离线 IP 数据库估算。
      </div>
      <div className="table-wrap">
        <table className="tbl">
          <thead>
            <tr>
              <th>#</th>
              <th>节点 IP / PTR 反向解析</th>
              <th>ASN / 运营商</th>
              <th>地区</th>
              <th>延迟 (平均 / 最小–最大)</th>
              <th>未响应</th>
            </tr>
          </thead>
          <tbody>
            {(enrichedHops.length > 0
              ? enrichedHops.map((h) => ({
                  ttl: h.ttl,
                  addresses: h.addresses,
                  avg: h.average_ms,
                  min: h.minimum_ms,
                  max: h.maximum_ms,
                  loss: h.no_response_percent,
                }))
              : rawHops.map((h) => {
                  const addrs = Array.from(new Set(h.samples.map((s) => s.address).filter((x): x is string => !!x)));
                  const rtts = h.samples.map((s) => s.rtt_ms).filter((x): x is number => x !== null);
                  const avg = rtts.length ? rtts.reduce((a, b) => a + b, 0) / rtts.length : null;
                  const loss = h.samples.length ? ((h.samples.length - rtts.length) / h.samples.length) * 100 : null;
                  return { ttl: h.ttl, addresses: addrs, avg, min: rtts.length ? Math.min(...rtts) : null, max: rtts.length ? Math.max(...rtts) : null, loss };
                })
            ).map((row) => {
              const primaryAddr = row.addresses[0];
              const info = primaryAddr ? addrMap.get(primaryAddr) : undefined;
              const ptr = info?.ptr.names?.[0];
              const asnText = info?.asn ? `AS${info.asn.number}${info.asn.organization ? ` · ${info.asn.organization}` : ""}` : primaryAddr ? "内网 / 未知" : "*";
              const locParts = [info?.location?.country_name || info?.location?.country_code, info?.location?.region, info?.location?.city].filter(Boolean);
              return (
                <tr key={row.ttl}>
                  <td>{row.ttl}</td>
                  <td>
                    {row.addresses.length === 0 ? (
                      <span className="sub">* (无响应)</span>
                    ) : (
                      <>
                        <div style={{ fontFamily: "var(--mono)", fontSize: "var(--fs-sm)" }}>{row.addresses.join(", ")}</div>
                        {ptr && <div className="sub" style={{ fontSize: "var(--fs-xs)" }}>{ptr}</div>}
                      </>
                    )}
                  </td>
                  <td className="sub">{asnText}</td>
                  <td className="sub">{locParts.length ? locParts.join(" · ") : "—"}</td>
                  <td>
                    {row.avg !== null ? (
                      <span>
                        <b>{num(row.avg, 1)} ms</b>
                        {row.min !== null && row.max !== null && (
                          <span className="sub"> ({num(row.min, 1)}–{num(row.max, 1)})</span>
                        )}
                      </span>
                    ) : (
                      <span className="sub">—</span>
                    )}
                  </td>
                  <td className="sub">{row.loss !== null ? `${num(row.loss, 0)}%` : "—"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/* ---------- 目标、测速组合、每日线路快照与离线库 ---------- */
function ManageNetworkSection({
  points,
  targets,
  plans,
  instances,
  onReload,
  onAddPreset,
}: {
  points: T.ProbePoint[];
  targets: T.NetworkTarget[];
  plans: T.ProbePlan[];
  instances: T.Instance[];
  onReload: () => void;
  onAddPreset: (p: (typeof PRESET_TARGETS)[number]) => void;
}) {
  const toast = useToast();
  const [editingTarget, setEditingTarget] = useState<T.NetworkTarget | "new" | null>(null);
  const [deletingTarget, setDeletingTarget] = useState<T.NetworkTarget | null>(null);
  const [editingPlan, setEditingPlan] = useState<T.ProbePlan | "new" | null>(null);
  const [deletingPlan, setDeletingPlan] = useState<T.ProbePlan | null>(null);
  const [snapshotPlan, setSnapshotPlan] = useState<T.ProbePlan | null>(null);
  const ipdbRes = useResource(api.networkIPDB, []);

  return (
    <>
      <Card
        title="探测目标"
        sub="支持外部 IP / 域名（可选 TCP 端口）或纳管实例公网地址"
        right={
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
            {PRESET_TARGETS.slice(0, 3).map((pr) => (
              <button key={pr.name} className="btn sm" onClick={() => onAddPreset(pr)}>
                + {pr.name}
              </button>
            ))}
            <button className="btn primary sm" onClick={() => setEditingTarget("new")}>
              + 新建目标
            </button>
          </div>
        }
      >
        {targets.length === 0 ? (
          <EmptyState title="尚未创建探测目标" />
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>类型与地址</th>
                  <th>TCP 端口</th>
                  <th>启用</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {targets.map((t) => (
                  <tr key={t.id}>
                    <td><b>{t.name}</b></td>
                    <td className="sub">
                      {t.kind === "custom" ? t.host : `纳管实例 (${instances.find((i) => i.id === t.instance_id)?.name ?? t.instance_id})`}
                    </td>
                    <td className="sub">{t.tcp_port ?? "仅 ICMP"}</td>
                    <td>
                      <Switch
                        on={t.enabled}
                        label={`启用目标 ${t.name}`}
                        onChange={async (v) => {
                          try {
                            await api.updateNetworkTarget(t.id, {
                              revision: t.revision,
                              name: t.name,
                              kind: t.kind,
                              host: t.host,
                              instance_id: t.instance_id,
                              tcp_port: t.tcp_port,
                              enabled: v,
                            });
                            onReload();
                          } catch (e) {
                            toast(errMsg(e), true);
                          }
                        }}
                      />
                    </td>
                    <td>
                      <div style={{ display: "flex", gap: 6 }}>
                        <button className="btn sm" onClick={() => setEditingTarget(t)}>编辑</button>
                        <button className="btn sm danger" onClick={() => setDeletingTarget(t)}>删除</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card
        title="测速组合与每日线路快照"
        sub="定时采集延迟与丢包，并可按天保存去程 AS 路径快照、在路径变化时发送提醒"
        right={
          <button className="btn primary sm" disabled={targets.length === 0} onClick={() => setEditingPlan("new")}>
            + 新建测速组合
          </button>
        }
      >
        {plans.length === 0 ? (
          <EmptyState title="尚未创建测速组合">请先创建探测目标，再把「测速点」与「目标」组合成周期探测计划。</EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>组合名称</th>
                  <th>测速点 → 目标</th>
                  <th>方式 / 周期</th>
                  <th>最新状态</th>
                  <th>启用</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {plans.map((p) => {
                  const [lvl, txt] = runBadge(p.last_run);
                  const tgName = targets.find((x) => x.id === p.target_id)?.name ?? p.target_id;
                  const ptName = p.point_kind === "server" ? "面板服务端" : (instances.find((i) => i.id === p.instance_id)?.name ?? p.instance_id);
                  return (
                    <tr key={p.id}>
                      <td><b>{p.name}</b></td>
                      <td className="sub">{ptName} → {tgName}</td>
                      <td className="sub">{p.method.toUpperCase()} · {p.family.toUpperCase()} · 每 {Math.round((p.frequency_seconds ?? 300) / 60)} 分钟</td>
                      <td><Badge level={lvl}>{txt}</Badge></td>
                      <td>
                        <Switch
                          on={p.enabled}
                          label={`启用组合 ${p.name}`}
                          onChange={async (v) => {
                            try {
                              await api.updateNetworkPlan(p.id, {
                                revision: p.revision,
                                name: p.name,
                                point_kind: p.point_kind,
                                instance_id: p.instance_id,
                                target_id: p.target_id,
                                family: p.family,
                                method: p.method,
                                enabled: v,
                                frequency_seconds: p.frequency_seconds,
                                count: p.count,
                                timeout_ms: p.timeout_ms,
                                interval_ms: p.interval_ms,
                              });
                              onReload();
                            } catch (e) {
                              toast(errMsg(e), true);
                            }
                          }}
                        />
                      </td>
                      <td>
                        <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                          <button className="btn sm" onClick={() => setSnapshotPlan(p)}>每日快照</button>
                          <button className="btn sm" onClick={() => setEditingPlan(p)}>编辑</button>
                          <button className="btn sm danger" onClick={() => setDeletingPlan(p)}>删除</button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title="离线 IP 归属数据库 (ASN / 地理位置)" sub="本地高速查询，不向外部公共 API 泄露纳管机器 IP">
        <Async res={ipdbRes}>
          {(db) => (
            <div style={{ display: "grid", gap: 10 }}>
              <div className="g3">
                <div className="card" style={{ padding: 12 }}>
                  <div className="sub">ASN 自治系统库</div>
                  <div><b>{db.asn.name}</b> · {db.asn.edition}</div>
                  <div className="sub" style={{ fontSize: "var(--fs-xs)" }}>{bytes(db.asn.size_bytes)} · {db.asn.origin === "bundled" ? "内置" : "已在线更新"}</div>
                </div>
                <div className="card" style={{ padding: 12 }}>
                  <div className="sub">国家 / 地区库</div>
                  <div><b>{db.country.name}</b> · {db.country.edition}</div>
                  <div className="sub" style={{ fontSize: "var(--fs-xs)" }}>{bytes(db.country.size_bytes)} · 粒度：{db.mode === "city" ? "城市级" : "国家级"}</div>
                </div>
                <div className="card" style={{ padding: 12 }}>
                  <div className="sub">自动更新与内存占用</div>
                  <div><b>{db.auto_update ? "已开启自动更新" : "仅使用离线内置库"}</b></div>
                  <div className="sub" style={{ fontSize: "var(--fs-xs)" }}>Go 堆内存：{bytes(db.go_heap_alloc_bytes)}</div>
                </div>
              </div>
              {db.attribution.length > 0 && (
                <div className="sub" style={{ fontSize: "var(--fs-xs)" }}>
                  数据来源：{db.attribution.map((a) => `${a.name} (${a.license})`).join(" · ")}
                </div>
              )}
            </div>
          )}
        </Async>
      </Card>

      {editingTarget && (
        <TargetModal
          existing={editingTarget === "new" ? undefined : editingTarget}
          instances={instances}
          onSaved={() => {
            setEditingTarget(null);
            onReload();
          }}
          onClose={() => setEditingTarget(null)}
        />
      )}

      {deletingTarget && (
        <Confirm
          title="删除探测目标"
          body={`确定删除目标「${deletingTarget.name}」吗？已被测速组合引用的目标需先移除组合。`}
          okText="删除"
          danger
          onOk={async () => {
            try {
              await api.deleteNetworkTarget(deletingTarget.id, deletingTarget.revision);
              toast("已删除目标");
              onReload();
            } catch (e) {
              toast(errMsg(e), true);
              throw e;
            }
          }}
          onClose={() => setDeletingTarget(null)}
        />
      )}

      {editingPlan && (
        <PlanModal
          existing={editingPlan === "new" ? undefined : editingPlan}
          points={points}
          targets={targets}
          onSaved={() => {
            setEditingPlan(null);
            onReload();
          }}
          onClose={() => setEditingPlan(null)}
        />
      )}

      {deletingPlan && (
        <Confirm
          title="删除测速组合"
          body={`确定删除测速组合「${deletingPlan.name}」及其关联的每日快照计划吗？`}
          okText="删除"
          danger
          onOk={async () => {
            try {
              await api.deleteNetworkPlan(deletingPlan.id, deletingPlan.revision);
              toast("已删除测速组合");
              onReload();
            } catch (e) {
              toast(errMsg(e), true);
              throw e;
            }
          }}
          onClose={() => setDeletingPlan(null)}
        />
      )}

      {snapshotPlan && <TraceSnapshotModal plan={snapshotPlan} onClose={() => setSnapshotPlan(null)} />}
    </>
  );
}

function TargetModal({
  existing,
  instances,
  onSaved,
  onClose,
}: {
  existing?: T.NetworkTarget;
  instances: T.Instance[];
  onSaved: () => void;
  onClose: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(existing?.name ?? "");
  const [kind, setKind] = useState<"custom" | "instance">(existing?.kind ?? "custom");
  const [host, setHost] = useState(existing?.host ?? "");
  const [instId, setInstId] = useState(existing?.instance_id ?? instances[0]?.id ?? "");
  const [portStr, setPortStr] = useState(existing?.tcp_port ? String(existing.tcp_port) : "");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setBusy(true);
    try {
      const tcp_port = portStr.trim() ? Number(portStr.trim()) : null;
      const payload: T.NetworkTargetInput = {
        revision: existing?.revision ?? 0,
        name: name.trim(),
        kind,
        host: kind === "custom" ? host.trim() : null,
        instance_id: kind === "instance" ? instId : null,
        tcp_port,
        enabled: existing?.enabled ?? true,
      };
      if (existing) await api.updateNetworkTarget(existing.id, payload);
      else await api.createNetworkTarget(payload);
      toast(existing ? "已更新目标" : "已创建目标");
      onSaved();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={existing ? "编辑探测目标" : "新建探测目标"} onClose={onClose}>
      <div style={{ display: "grid", gap: 12 }}>
        <div>
          <label className="sub">目标名称</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="如：上海电信网关 / 香港落地" style={{ width: "100%", marginTop: 4 }} />
        </div>
        <div>
          <label className="sub">目标类型</label>
          <select value={kind} onChange={(e) => setKind(e.target.value as "custom" | "instance")} style={{ width: "100%", marginTop: 4 }}>
            <option value="custom">外部 IP / 域名</option>
            {instances.length > 0 && <option value="instance">纳管云服务器实例</option>}
          </select>
        </div>
        {kind === "custom" ? (
          <div>
            <label className="sub">主机地址（公网 IP 或域名）</label>
            <input type="text" value={host} onChange={(e) => setHost(e.target.value)} placeholder="1.1.1.1 或 example.com" style={{ width: "100%", marginTop: 4 }} />
          </div>
        ) : (
          <div>
            <label className="sub">选择纳管实例</label>
            <select value={instId} onChange={(e) => setInstId(e.target.value)} style={{ width: "100%", marginTop: 4 }}>
              {instances.map((i) => (
                <option key={i.id} value={i.id}>{i.name} ({i.public_ips[0]?.address ?? "无公网 IP"})</option>
              ))}
            </select>
          </div>
        )}
        <div>
          <label className="sub">TCP 探测端口（选填；留空则仅用于 ICMP 探测）</label>
          <input type="number" min={1} max={65535} value={portStr} onChange={(e) => setPortStr(e.target.value)} placeholder="如 443 或 22" style={{ width: "100%", marginTop: 4 }} />
        </div>
      </div>
      <div className="actions">
        <button className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy || !name.trim()} onClick={save}>
          {busy && <Spinner />} 保存
        </button>
      </div>
    </Modal>
  );
}

function PlanModal({
  existing,
  initialPoint,
  initialTarget,
  points,
  targets,
  onSaved,
  onClose,
}: {
  existing?: T.ProbePlan;
  initialPoint?: T.ProbePoint;
  initialTarget?: T.NetworkTarget;
  points: T.ProbePoint[];
  targets: T.NetworkTarget[];
  onSaved: (p: T.ProbePlan) => void;
  onClose: () => void;
}) {
  const toast = useToast();
  const defaultPointKey = existing
    ? existing.point_kind === "server"
      ? "server"
      : existing.instance_id ?? "server"
    : initialPoint
      ? initialPoint.point_kind === "server"
        ? "server"
        : initialPoint.instance_id ?? "server"
      : "server";
  const defaultTargetId = existing?.target_id ?? initialTarget?.id ?? targets[0]?.id ?? "";
  const chosenTarget = targets.find((t) => t.id === defaultTargetId);

  const [pointKey, setPointKey] = useState(defaultPointKey);
  const [targetId, setTargetId] = useState(defaultTargetId);
  const [name, setName] = useState(
    existing?.name ?? (initialPoint && initialTarget ? `${initialPoint.name} → ${initialTarget.name}` : ""),
  );
  const [method, setMethod] = useState<"icmp" | "tcp">(existing?.method ?? (chosenTarget?.tcp_port ? "tcp" : "icmp"));
  const [family, setFamily] = useState<"ipv4" | "ipv6">(existing?.family ?? "ipv4");
  const [freq, setFreq] = useState<number>(existing?.frequency_seconds ?? 300);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setBusy(true);
    try {
      const isServer = pointKey === "server";
      const payload: T.ProbePlanInput = {
        revision: existing?.revision ?? 0,
        name: name.trim() || "延迟探测",
        point_kind: isServer ? "server" : "agent",
        instance_id: isServer ? null : pointKey,
        target_id: targetId,
        family,
        method,
        enabled: existing?.enabled ?? true,
        frequency_seconds: freq,
        count: existing?.count ?? 5,
        timeout_ms: existing?.timeout_ms ?? 2000,
        interval_ms: existing?.interval_ms ?? 1000,
      };
      const saved = existing ? await api.updateNetworkPlan(existing.id, payload) : await api.createNetworkPlan(payload);
      toast(existing ? "已更新测速组合" : "已启用测速组合");
      onSaved(saved);
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={existing ? "编辑测速组合" : "新建测速组合"} onClose={onClose}>
      <div style={{ display: "grid", gap: 12 }}>
        <div>
          <label className="sub">组合名称</label>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="如：新加坡 → 上海电信" style={{ width: "100%", marginTop: 4 }} />
        </div>
        <div className="g2">
          <div>
            <label className="sub">测速点</label>
            <select value={pointKey} onChange={(e) => setPointKey(e.target.value)} style={{ width: "100%", marginTop: 4 }}>
              <option value="server">面板服务端</option>
              {points.filter((p) => p.point_kind === "agent" && p.instance_id).map((p) => (
                <option key={p.id} value={p.instance_id!}>{p.name}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="sub">探测目标</label>
            <select value={targetId} onChange={(e) => setTargetId(e.target.value)} style={{ width: "100%", marginTop: 4 }}>
              {targets.map((t) => (
                <option key={t.id} value={t.id}>{t.name}</option>
              ))}
            </select>
          </div>
        </div>
        <div className="g3">
          <div>
            <label className="sub">探测方式</label>
            <select value={method} onChange={(e) => setMethod(e.target.value as "icmp" | "tcp")} style={{ width: "100%", marginTop: 4 }}>
              <option value="icmp">ICMP Ping</option>
              <option value="tcp">TCP 建连 (需目标配置端口)</option>
            </select>
          </div>
          <div>
            <label className="sub">地址族</label>
            <select value={family} onChange={(e) => setFamily(e.target.value as "ipv4" | "ipv6")} style={{ width: "100%", marginTop: 4 }}>
              <option value="ipv4">IPv4</option>
              <option value="ipv6">IPv6</option>
            </select>
          </div>
          <div>
            <label className="sub">探测间隔</label>
            <select value={freq} onChange={(e) => setFreq(Number(e.target.value))} style={{ width: "100%", marginTop: 4 }}>
              <option value={60}>每 1 分钟</option>
              <option value={300}>每 5 分钟</option>
              <option value={900}>每 15 分钟</option>
              <option value={3600}>每 1 小时</option>
            </select>
          </div>
        </div>
      </div>
      <div className="actions">
        <button className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy || !targetId} onClick={save}>
          {busy && <Spinner />} 保存
        </button>
      </div>
    </Modal>
  );
}

function TraceSnapshotModal({ plan, onClose }: { plan: T.ProbePlan; onClose: () => void }) {
  const toast = useToast();
  const schedRes = useResource(() => api.traceSchedule(plan.id), [plan.id]);
  const snapsRes = useResource(async () => {
    const toDate = new Date(Date.now() - 5_000);
    const fromDate = new Date(toDate.getTime() - 30 * 86400_000);
    const page = await api.traceSnapshots(plan.id, { from: fromDate.toISOString(), to: toDate.toISOString(), limit: 20 });
    return page.items;
  }, [plan.id]);

  const [enabled, setEnabled] = useState(false);
  const [notifyChanges, setNotifyChanges] = useState(true);
  const [timeOfDay, setTimeOfDay] = useState("03:00");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (schedRes.data) {
      setEnabled(schedRes.data.enabled);
      setNotifyChanges(schedRes.data.notify_changes);
      setTimeOfDay(schedRes.data.time_of_day);
    }
  }, [schedRes.data]);

  const save = async () => {
    if (!schedRes.data) return;
    setBusy(true);
    try {
      await api.saveTraceSchedule(plan.id, {
        revision: schedRes.data.revision,
        enabled,
        notify_changes: notifyChanges,
        time_of_day: timeOfDay,
        max_hops: schedRes.data.max_hops || 30,
        count: schedRes.data.count || 3,
        timeout_ms: schedRes.data.timeout_ms || 1000,
      });
      toast("已保存每日线路快照设置");
      schedRes.reload();
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={`每日线路快照：${plan.name}`} onClose={onClose} wide>
      <Async res={schedRes}>
        {(sched) => (
          <div style={{ display: "grid", gap: 14 }}>
            <div className="g3">
              <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                <Switch on={enabled} onChange={setEnabled} label="启用每日快照" />
                <span>启用每日自动快照</span>
              </div>
              <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                <Switch on={notifyChanges} onChange={setNotifyChanges} label="路径变化发送提醒" />
                <span>AS 路径变化时提醒</span>
              </div>
              <div>
                <label className="sub">每日执行时刻 ({sched.timezone})</label>
                <input type="time" value={timeOfDay} onChange={(e) => setTimeOfDay(e.target.value)} style={{ width: "100%", marginTop: 4 }} />
              </div>
            </div>
            <div style={{ display: "flex", justifyContent: "flex-end" }}>
              <button className="btn primary sm" disabled={busy} onClick={save}>
                {busy && <Spinner />} 保存快照计划
              </button>
            </div>
            <div style={{ borderTop: "1px solid var(--border)", paddingTop: 12 }}>
              <div className="sub" style={{ marginBottom: 8 }}>最近 30 天快照与 AS 路径对比</div>
              <Async res={snapsRes}>
                {(snaps) =>
                  snaps.length === 0 ? (
                    <div className="sub">暂无历史快照记录。</div>
                  ) : (
                    <div className="table-wrap">
                      <table className="tbl">
                        <thead>
                          <tr>
                            <th>计划时刻</th>
                            <th>执行状态</th>
                            <th>对比结论</th>
                            <th>观测 AS 序列</th>
                          </tr>
                        </thead>
                        <tbody>
                          {snaps.map((sn) => (
                            <tr key={sn.id}>
                              <td className="sub">{stamp(sn.due_at)}</td>
                              <td><Badge level={sn.status === "succeeded" ? "good" : "neutral"}>{sn.status}</Badge></td>
                              <td>
                                <Badge level={sn.comparison.status === "changed" ? "warning" : sn.comparison.status === "unchanged" ? "good" : "neutral"}>
                                  {sn.comparison.status === "changed" ? "路径变化" : sn.comparison.status === "unchanged" ? "未变化" : sn.comparison.status}
                                </Badge>
                              </td>
                              <td className="sub" style={{ fontFamily: "var(--mono)", fontSize: "var(--fs-xs)" }}>
                                {sn.comparison.asns.length ? sn.comparison.asns.map((a) => `AS${a}`).join(" → ") : (sn.comparison.reason ?? "—")}
                                {sn.comparison.status === "changed" && sn.comparison.previous_asns.length > 0 && (
                                  <div>上次：{sn.comparison.previous_asns.map((a) => `AS${a}`).join(" → ")}</div>
                                )}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )
                }
              </Async>
            </div>
          </div>
        )}
      </Async>
      <div className="actions">
        <CopyButton text={plan.id} label="复制组合 ID" />
        <button className="btn" onClick={onClose}>关闭</button>
      </div>
    </Modal>
  );
}
