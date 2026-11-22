// 流量与费用工作区：统一承载 CDT 免费额度与趋势预测、账户余额与月账单（含就地低余额告警阈值）、到期续费与闲置资源巡检、以及官方读取与对账核验明细。
import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { billTypeName, CHARGE, errorText, regionName, RENEWAL, RULE, SCOPE, SITE, SUBJECT_KIND } from "../api/labels";
import type { Account, AccountFinance, AlertRule, BandwidthPackage, DailyTraffic, IdleCandidate, Instance, QuotaScope, QuotaStatus, TrafficPricing } from "../api/types";
import { TipRow } from "../components/charts/common";
import { DailyBars } from "../components/charts/DailyBars";
import { LineChart } from "../components/charts/LineChart";
import { QuotaMeter, QuotaUnknownNote } from "../components/QuotaMeter";
import { Async, Badge, Banner, Card, EmptyState, ErrorState, errMsg, Fresh, InfoTip, Modal, SegToggle, Skeleton, Spinner, SubTabs, Switch, useToast } from "../components/ui";
import { dailyValue, forecastNote, usageTimeline } from "../lib/daily";
import { useShared } from "../lib/data";
import { bytes, gb, gbValue, num, shortDate, stamp, time, ymd } from "../lib/format";
import { parseTiers, tiersToText } from "../lib/pricing";
import { Link, useQueryTab } from "../lib/router";
import { useNow, useResource, type Resource } from "../lib/useResource";
import { quotaView } from "../lib/usage";

type View = "chart" | "table";
const VIEW: [View, string][] = [["chart", "图表"], ["table", "表格"]];
const CURRENCY_SYM: Record<string, string> = { CNY: "¥", USD: "$" };
const money = (amount: string, currency: string) => `${CURRENCY_SYM[currency] ?? currency + " "}${amount}`;
const cycleDays = (d: DailyTraffic) => Math.max(d.days.length, Math.round((Date.parse(d.cycle_end) - Date.parse(d.cycle_start)) / 86400_000));

export function Traffic() {
  const { accounts, quotas, instances } = useShared();
  const [tab, setTab] = useQueryTab("quota", ["quota", "billing", "idle", "audit"] as const);
  const [accountId, setAccountId] = useState("all");
  const [pricingOpen, setPricingOpen] = useState(false);
  const sims = useResource(api.quotaSimulations, [], { refreshMs: 30_000 });
  const accs = accounts.data;

  if (accounts.error) return <div className="card"><ErrorState error={accounts.error} onRetry={accounts.reload} /></div>;
  if (!accs) return <div className="card"><Skeleton lines={6} /></div>;
  if (!accs.length) {
    return (
      <div className="card">
        <EmptyState title="尚未添加阿里云账号" action={<Link className="btn primary" to="/settings">添加云账号</Link>}>
          添加云账号后，此处将自动汇聚 CDT 免费流量配额、每日趋势、账户余额、月账单及闲置计费巡检。
        </EmptyState>
      </div>
    );
  }

  const enabledAccounts = accs.filter((a) => a.enabled);
  const qs = (quotas.data ?? []).filter((q) => q.account_id === accountId);
  const all = accountId === "all";
  const instName = (id: string) => instances.data?.items.find((i) => i.id === id)?.name ?? id;

  return (
    <>
      {(sims.data ?? []).length > 0 && (
        <Banner level="warning" action={<Link className="btn sm" to="/automation">管理演练</Link>}>
          <b>当前有 {sims.data!.length} 项模拟用量演练生效中。</b> 本页展示的均为真实读取与估算数据，模拟值仅用于验证自动关机策略。
        </Banner>
      )}

      <SubTabs
        value={tab}
        onChange={setTab}
        tabs={[
          { key: "quota", label: "流量配额与趋势" },
          { key: "billing", label: "账户余额与账单" },
          { key: "idle", label: "到期续费与闲置巡检" },
          { key: "audit", label: "读取与对账明细" },
        ]}
        right={
          tab === "quota" ? (
            <button className="btn sm" onClick={() => setPricingOpen(true)}>超额单价设置</button>
          ) : undefined
        }
      />

      {tab === "quota" && (
        <>
          <div className="toolbar">
            <span className="sub" style={{ fontWeight: 500 }}>查看维度</span>
            {accs.length <= 5 ? (
              <div className="checks">
                <button type="button" className={`check ${all ? "on" : ""}`} onClick={() => setAccountId("all")}>
                  全部启用账号 ({enabledAccounts.length})
                </button>
                {accs.map((a) => (
                  <button key={a.id} type="button" className={`check ${accountId === a.id ? "on" : ""}`} onClick={() => setAccountId(a.id)}>
                    {a.name}{!a.enabled ? " (已停用)" : ""}
                  </button>
                ))}
              </div>
            ) : (
              <select value={accountId} onChange={(e) => setAccountId(e.target.value)} aria-label="选择账号">
                <option value="all">全部启用账号合计 ({enabledAccounts.length})</option>
                {accs.map((a) => (
                  <option key={a.id} value={a.id}>{a.name}</option>
                ))}
              </select>
            )}
            {all && (
              <InfoTip label="合计口径说明">
                汇总全部已启用账号的当月用量，停用账号不计入。注意阿里云 CDT 免费额度按账号独立核算，各账号额度不可跨账号混用。
              </InfoTip>
            )}
          </div>

          {all ? (
            <AggregateQuotaAndTrends />
          ) : (
            <>
              <QuotaVisualCard res={{ ...quotas, data: quotas.data ? qs : undefined }} />
              <DailySection key={accountId} accountId={accountId} quotas={qs} />
            </>
          )}

          <Subjects accountId={accountId} instName={instName} accounts={accs} />
          <Packages accountId={accountId} accounts={accs} />
        </>
      )}

      {tab === "billing" && <BillingSubWorkspace accounts={accs} instances={instances.data?.items} />}

      {tab === "idle" && <IdleAndRenewalSubWorkspace accounts={accs} instances={instances.data?.items} />}

      {tab === "audit" && (
        <>
          <div className="toolbar">
            <span className="sub" style={{ fontWeight: 500 }}>核对账号</span>
            <select value={accountId} onChange={(e) => setAccountId(e.target.value)} aria-label="选择核对账号">
              <option value="all">全部启用账号</option>
              {accs.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
            <div className="spacer" />
            <span className="sub">对比阿里云 CDT 官方累计接口与本地 hm-agent 采集增量的一致性</span>
          </div>
          {all
            ? enabledAccounts.map((a) => <ObservationsCard key={a.id} accountId={a.id} accountName={a.name} />)
            : <ObservationsCard key={accountId} accountId={accountId} />}
          <ReconciliationsCard accountId={all ? undefined : accountId} accounts={accs} />
        </>
      )}

      {pricingOpen && (
        <PricingModal
          onClose={() => setPricingOpen(false)}
          onSaved={() => {
            setPricingOpen(false);
            quotas.reload();
          }}
        />
      )}
    </>
  );
}

/* ---------- Tab 1: Quota & Trends ---------- */
function AggregateQuotaAndTrends() {
  const quotas = useResource(() => api.quotas("all"), [], { refreshMs: 10_000 });
  return (
    <>
      <QuotaVisualCard res={quotas} total />
      <DailySection accountId="all" quotas={quotas.data ?? []} />
    </>
  );
}

function overageText(oc: QuotaStatus["overage_cost"]): string | null {
  if (!oc) return null;
  const sym = CURRENCY_SYM[oc.currency] ?? oc.currency + " ";
  if (oc.estimated_cost !== null && Number(oc.estimated_cost) > 0) {
    return `当前超额约 ${sym}${oc.estimated_cost}${oc.forecast_cost && Number(oc.forecast_cost) > Number(oc.estimated_cost) ? ` · 月底预计 ${sym}${oc.forecast_cost}` : ""}`;
  }
  if (oc.forecast_cost !== null && Number(oc.forecast_cost) > 0) {
    return `月底预计超额 ${sym}${oc.forecast_cost}`;
  }
  return null;
}

function QuotaVisualCard({ res, total = false }: { res: Pick<Resource<QuotaStatus[]>, "data" | "error" | "reload">; total?: boolean }) {
  const order: QuotaScope[] = ["overseas", "mainland"];
  const rows = res.data ? order.map((s) => res.data!.find((q) => q.scope === s)).filter((q): q is QuotaStatus => !!q) : [];

  return (
    <Card
      title={total ? "当月流量池概览 · 合计" : "当月流量池概览"}
      sub="实心段为官方累计基准，亮青段为最近采集增量，斜纹段为月底趋势预测"
      right={rows[0] ? <Fresh at={quotaView(rows[0]).updatedAt} kind="realtime" quiet /> : undefined}
    >
      {res.error ? (
        <ErrorState error={res.error} onRetry={res.reload} />
      ) : !res.data ? (
        <Skeleton lines={4} />
      ) : !rows.length ? (
        <EmptyState title={total ? "暂无启用的云账号" : "该账号暂无已启用跟踪的流量池"} />
      ) : (
        <>
          <div className={rows.length > 1 ? "grid g2" : "stack"} style={{ gap: 22 }}>
            {rows.map((q) => {
              const cost = overageText(q.overage_cost);
              return (
                <div key={q.scope} style={{ padding: "4px 2px" }}>
                  <QuotaMeter q={q} />
                  {cost && <div className="sub" style={{ marginTop: 6, color: "var(--serious)", fontWeight: 600 }}>{cost}</div>}
                </div>
              );
            })}
          </div>
          <QuotaUnknownNote quotas={rows} />
        </>
      )}
    </Card>
  );
}

function DailyCell({ day, scope }: { day: DailyTraffic["days"][number]; scope: QuotaScope }) {
  const v = dailyValue(day, scope);
  return (
    <>
      <b>{v.value === null ? "—" : v.value < 10 * 1024 ** 2 ? bytes(v.value) : gb(v.value, 2)}</b>
      <div className="sub">{v.label}</div>
      {v.partial && v.from && v.until && <div className="sub">{time(v.from)}–{time(v.until)}</div>}
    </>
  );
}

function DailySection({ accountId, quotas }: { accountId: string; quotas: QuotaStatus[] }) {
  const daily = useResource(() => api.dailyTraffic(accountId), [accountId], { refreshMs: 30_000 });
  const [view, setView] = useState<View>("chart");
  const [scope, setScope] = useState<QuotaScope>(() => (quotas.length === 1 ? quotas[0].scope : "overseas"));
  const activeScope: QuotaScope = quotas.length === 1 && quotas[0].scope !== scope ? quotas[0].scope : scope;
  const scopeOpts: [QuotaScope, string][] = (["overseas", "mainland"] as const)
    .filter((s) => !quotas.length || quotas.some((q) => q.scope === s))
    .map((s) => [s, s === "overseas" ? "海外" : "国内"]);

  return (
    <div className="grid g2">
      <Card
        title={
          <span className="inline" style={{ gap: 6 }}>
            <span>每日流量构成</span>
            {daily.data && (
              <InfoTip label="每日统计说明">
                按 {daily.data.data_timezone} 划分自然日。优先采用官方日数据，未出账时段使用采集估算；虚线边框代表部分时段覆盖，空缺不按 0 处理。
                {daily.data.updated_at ? ` 最近更新：${stamp(daily.data.updated_at)}。` : ""}
              </InfoTip>
            )}
          </span>
        }
        sub="本计费周期逐日海外与国内流量"
        right={<SegToggle label="显示方式" value={view} onChange={setView} options={VIEW} />}
      >
        <Async res={daily} lines={6}>
          {(d) => (
            <>
              {!d.days.length ? (
                <EmptyState title="当前计费周期尚无日流量数据" />
              ) : view === "chart" ? (
                <>
                  <div className="legend" style={{ marginBottom: 8 }}>
                    <span><i style={{ background: "var(--series-1)" }} />{SCOPE.overseas}</span>
                    <span><i style={{ background: "var(--series-2)" }} />{SCOPE.mainland}</span>
                    <span className="sub">虚线框：部分时段覆盖</span>
                  </div>
                  <DailyBars
                    ariaLabel="每日流量用量"
                    unit="GB"
                    nameA={SCOPE.overseas}
                    nameB={SCOPE.mainland}
                    totalSlots={cycleDays(d)}
                    bars={d.days.map((day) => {
                      const a = dailyValue(day, "overseas");
                      const b = dailyValue(day, "mainland");
                      return {
                        label: shortDate(day.date),
                        a: a.value === null ? null : gbValue(a.value),
                        b: b.value === null ? null : gbValue(b.value),
                        partialA: a.partial,
                        partialB: b.partial,
                        provisional: day.provisional,
                      };
                    })}
                  />
                  {d.days.some((day) => (["mainland", "overseas"] as const).some((sc) => dailyValue(day, sc).value !== null)) && (
                    <div className="inline" style={{ gap: 20, marginTop: 10 }}>
                      {(["overseas", "mainland"] as const)
                        .filter((sc) => !quotas.length || quotas.some((q) => q.scope === sc))
                        .map((sc) => {
                          const today = dailyValue(d.days.at(-1)!, sc);
                          return (
                            <span key={sc} className="sub">
                              今日 ({shortDate(d.days.at(-1)!.date)}) {SCOPE[sc]}：
                              <b style={{ color: "var(--text)" }}>
                                {today.value === null ? "—" : today.value < 10 * 1024 ** 2 ? bytes(today.value) : gb(today.value, 2)}
                              </b>
                            </span>
                          );
                        })}
                    </div>
                  )}
                </>
              ) : (
                <div className="table-wrap" style={{ maxHeight: 320, overflowY: "auto" }}>
                  <table>
                    <thead>
                      <tr>
                        <th>日期</th>
                        <th className="num">{SCOPE.overseas}</th>
                        <th className="num">{SCOPE.mainland}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {[...d.days].reverse().map((day) => (
                        <tr key={day.date}>
                          <td>
                            {shortDate(day.date)}
                            {day === d.days.at(-1) && <div className="sub">今日进行中</div>}
                          </td>
                          <td className="num"><DailyCell day={day} scope="overseas" /></td>
                          <td className="num"><DailyCell day={day} scope="mainland" /></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </Async>
      </Card>

      <Card
        title={
          <span className="inline" style={{ gap: 6 }}>
            <span>累计曲线与月底预测</span>
            <InfoTip label="预测算法说明">
              基于近 7 天中的完整自然日样本推算月底累计值（今日未结束不参与日均计算），仅供趋势参考。
            </InfoTip>
          </span>
        }
        sub="官方基准线、实时估算与免费额度上限对比"
        right={scopeOpts.length > 1 ? <SegToggle label="范围" value={activeScope} onChange={setScope} options={scopeOpts} /> : undefined}
      >
        <Async res={daily} lines={6}>
          {(d) => {
            const q = quotas.find((x) => x.scope === activeScope);
            return !d.days.length || !q ? <EmptyState title="暂无足够历史数据生成累计曲线" /> : <Cumulative d={d} q={q} scope={activeScope} />;
          }}
        </Async>
      </Card>
    </div>
  );
}

function Cumulative({ d, q, scope }: { d: DailyTraffic; q: QuotaStatus; scope: QuotaScope }) {
  const s = usageTimeline(d, q, scope);
  const color = scope === "overseas" ? "var(--series-1)" : "var(--series-2)";
  const quota = gbValue(q.quota_bytes);
  const f = q.forecast;
  const convert = (values: (number | null)[]) => values.map((v) => (v === null ? null : gbValue(v)));
  const hasValues = [...s.official, ...s.collected, ...s.realtime, ...s.projected].some((v) => v !== null);

  return (
    <>
      {hasValues ? (
        <>
          <div className="legend" style={{ marginBottom: 8 }}>
            {s.official.some((v) => v !== null) && <span><i className="line" style={{ background: "var(--muted)" }} />官方累计</span>}
            {s.collected.some((v) => v !== null) && <span><i className="line" style={{ background: color }} />采集累计</span>}
            {s.realtime.some((v) => v !== null) && <span><i style={{ background: "var(--realtime)" }} />当前估算</span>}
            {f && <span><i className="dash" style={{ borderColor: color }} />月底预测</span>}
          </div>
          <LineChart
            ariaLabel={`${SCOPE[scope]}累计读取、实时估算与月底预测`}
            labels={s.labels}
            unit="GB"
            height={240}
            digits={2}
            series={[
              { name: "官方累计", color: "var(--muted)", data: convert(s.official), endLabel: false },
              { name: "采集累计", color, data: convert(s.collected), endLabel: false },
              { name: "当前估算", color: "var(--realtime)", data: convert(s.realtime), endLabel: false },
              { name: "月底预测", color, data: convert(s.projected), dashed: true, endLabel: false },
            ]}
            refLine={{ value: quota, label: `额度 ${num(quota, 0)} GB` }}
          />
        </>
      ) : (
        <EmptyState title="正在积累累计曲线数据" />
      )}
      <div className="inline" style={{ marginTop: 10, justifyContent: "space-between" }}>
        <span className="sub">
          {f ? (
            <>
              月底预计 <b style={{ color: "var(--text)" }}>{gb(f.cycle_end_bytes, 2)}</b>
              {f.exceeds_on && (
                <span style={{ color: "var(--serious)" }}>
                  {" "}· {f.already_exceeded ? "已超出免费额度" : `预计 ${shortDate(f.exceeds_on)} 触及上限`}
                </span>
              )}
            </>
          ) : (
            forecastNote(q)
          )}
        </span>
        {f && (
          <InfoTip label="样本明细">
            {forecastNote(q)}
            {f.sample_dates?.length ? ` 样本日：${f.sample_dates.map(shortDate).join("、")}。` : ""}
          </InfoTip>
        )}
      </div>
    </>
  );
}

function Subjects({ accountId, instName, accounts }: { accountId: string; instName: (id: string) => string; accounts: Account[] }) {
  const [period, setPeriod] = useState<"today" | "cycle">("today");
  const enabled = accounts.filter((a) => a.enabled).map((a) => a.id);
  const res = useResource(
    async () => {
      const rows = await api.billingSubjects(accountId === "all" ? undefined : accountId);
      return accountId === "all" ? rows.filter((s) => enabled.includes(s.account_id)) : rows;
    },
    [accountId, enabled.join(",")],
    { refreshMs: 30_000 },
  );
  const src = { agent: "hm-agent", cloud_monitor: "云监控", none: "暂无数据" } as const;

  return (
    <Card
      title="公网出口流量归因"
      sub={period === "today" ? "今日零点至今各公网计费对象收发明细" : "本计费周期累计流量与所属额度池占比"}
      right={<SegToggle label="统计周期" value={period} onChange={setPeriod} options={[["today", "今日流量"], ["cycle", "本月归因"]]} />}
    >
      <Async res={res} lines={4}>
        {(items) =>
          !items.length ? (
            <EmptyState title="当前筛选下无公网计费出口" />
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>计费对象与地址</th>
                    <th>计费规则与数据源</th>
                    <th className="num">入方向</th>
                    <th className="num">出方向</th>
                    <th className="num">计费流量</th>
                    {period === "cycle" && <th className="num">池内占比</th>}
                    <th>CDT 额度池</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((s) => {
                    const rx = period === "today" ? s.today_rx_bytes : (s.cycle_rx_bytes ?? null);
                    const tx = period === "today" ? s.today_tx_bytes : (s.cycle_tx_bytes ?? null);
                    const metered = period === "today" ? s.today_metered_bytes : (s.cycle_metered_bytes ?? null);
                    return (
                      <tr key={s.id}>
                        <td>
                          <b>{s.name}</b>
                          <div className="sub">
                            {accountId === "all" ? `${accounts.find((a) => a.id === s.account_id)?.name ?? ""} · ` : ""}
                            <span className="mono">{s.public_ips.join("、") || "—"}</span>
                            {s.kind.startsWith("cbwp") ? ` · 成员 ${s.instance_ids.map(instName).join("、")}` : ""}
                          </div>
                        </td>
                        <td>
                          <span>{SUBJECT_KIND[s.kind]}</span>
                          <div className="sub">{RULE[s.metering_rule]} · {src[s.data_source]}</div>
                        </td>
                        <td className="num">{rx === null ? <span className="sub">—</span> : gb(rx)}</td>
                        <td className="num">{tx === null ? <span className="sub">—</span> : gb(tx)}</td>
                        <td className="num">{metered === null ? <span className="sub">—</span> : <b>{gb(metered)}</b>}</td>
                        {period === "cycle" && <td className="num">{s.cycle_share_percent == null ? <span className="sub">—</span> : `${num(s.cycle_share_percent, 1)}%`}</td>}
                        <td>
                          {s.counts_toward_cdt === null ? (
                            <Badge level="warning">待确认</Badge>
                          ) : s.counts_toward_cdt && s.cdt_scope ? (
                            <Badge level="good">{SCOPE[s.cdt_scope]}</Badge>
                          ) : (
                            <span className="sub">不计入</span>
                          )}
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

function Packages({ accountId, accounts }: { accountId: string; accounts: Account[] }) {
  const res = useResource(api.bandwidthPackages, []);
  const mine = (res.data ?? []).filter((p) => (accountId === "all" ? accounts.some((a) => a.id === p.account_id && a.enabled) : p.account_id === accountId));
  if (!mine.length) return null;
  return (
    <>
      {mine.map((p) => (
        <PackageHourly key={p.id} p={p} accountName={accountId === "all" ? accounts.find((a) => a.id === p.account_id)?.name : undefined} />
      ))}
    </>
  );
}

function PackageHourly({ p, accountName }: { p: BandwidthPackage; accountName?: string }) {
  const res = useResource(() => api.bandwidthPackageHourly(p.id), [p.id], { refreshMs: 60_000 });
  const [view, setView] = useState<View>("chart");
  return (
    <Card title={`共享带宽 ${p.name}${accountName ? `（${accountName}）` : ""} · 今日每小时收发`} right={<SegToggle label="显示方式" value={view} onChange={setView} options={VIEW} />}>
      <Async res={res} lines={5}>
        {(h) => {
          if (!h.periods.length) return <EmptyState title="今日尚无整点收发数据" />;
          const open = Date.parse(h.periods[h.periods.length - 1].start) + h.period_seconds * 1000 > Date.now();
          const done = open ? h.periods.slice(0, -1) : h.periods;
          const labels = done.map((x) => time(x.start));
          if (view === "chart" && done.length < 2) {
            return <EmptyState title="已完成整点不足 2 个，暂无法绘制曲线">可先切换至表格查看当前小时数据。</EmptyState>;
          }
          return view === "chart" ? (
            <>
              <div className="legend" style={{ marginBottom: 8 }}>
                <span><i className="line" style={{ background: "var(--series-1)" }} />出方向</span>
                <span><i className="line" style={{ background: "var(--series-3)" }} />入方向</span>
              </div>
              <LineChart
                ariaLabel={`${p.name} 每小时收发流量`}
                labels={labels}
                unit="GB"
                digits={2}
                series={[
                  { name: "出方向", color: "var(--series-1)", data: done.map((x) => gbValue(x.tx_bytes)) },
                  { name: "入方向", color: "var(--series-3)", data: done.map((x) => gbValue(x.rx_bytes)) },
                ]}
                extraTip={(i) => <TipRow label="计费方向" value={`${done[i].rx_bytes >= done[i].tx_bytes ? "入方向" : "出方向"} ${num(gbValue(done[i].metered_bytes), 2)} GB`} />}
              />
            </>
          ) : (
            <div className="table-wrap" style={{ maxHeight: 300, overflowY: "auto" }}>
              <table>
                <thead>
                  <tr><th>时段</th><th className="num">入方向</th><th className="num">出方向</th><th className="num">计费流量</th></tr>
                </thead>
                <tbody>
                  {[...h.periods].reverse().map((x) => (
                    <tr key={x.start}>
                      <td>{time(x.start)}{open && x === h.periods[h.periods.length - 1] && <span className="sub"> · 进行中</span>}</td>
                      <td className="num">{gb(x.rx_bytes, 2)}</td>
                      <td className="num">{gb(x.tx_bytes, 2)}</td>
                      <td className="num"><b>{gb(x.metered_bytes, 2)}</b></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          );
        }}
      </Async>
    </Card>
  );
}

/* ---------- Tab 2: Account Balances & Monthly Bills ---------- */
function BillingSubWorkspace({ accounts, instances }: { accounts: Account[]; instances: Instance[] | undefined }) {
  const rules = useResource(() => api.alertRules(false), []);
  const balanceRule = rules.data?.find((r) => r.key === "balance_low");
  const toast = useToast();

  const toggleGlobalBalanceRule = async (enabled: boolean) => {
    if (!balanceRule) return;
    try {
      const saved = await api.updateAlertRule("balance_low", { enabled, params: balanceRule.params });
      rules.mutate((l) => l.map((x) => (x.key === saved.key ? saved : x)));
      toast(enabled ? "已开启低余额告警规则" : "已关闭低余额告警规则");
    } catch (e) {
      toast(errMsg(e), true);
    }
  };

  return (
    <>
      {balanceRule && (
        <div className="toolbar">
          <Switch on={balanceRule.enabled} onChange={toggleGlobalBalanceRule} label="启用账户低余额告警" />
          <span><b>低余额告警推送</b></span>
          <span className="sub">在下方各账号卡片直接填写提醒阈值，低于设定金额时将自动通过已配置的通知渠道告警</span>
          <div className="spacer" />
          <Link className="btn sm ghost" to="/automation?tab=channels">通知渠道配置 →</Link>
        </div>
      )}

      <div className={accounts.length > 1 ? "grid g2" : "stack"}>
        {accounts.map((a) => (
          <FinanceCard
            key={a.id}
            account={a}
            balanceRule={balanceRule}
            onSavedRule={(nr) => rules.mutate((l) => l.map((x) => (x.key === nr.key ? nr : x)))}
          />
        ))}
      </div>

      <InstanceMonthlyCostCard instances={instances} accounts={accounts} />
    </>
  );
}

function FinanceCard({ account, balanceRule, onSavedRule }: { account: Account; balanceRule?: AlertRule; onSavedRule: (r: AlertRule) => void }) {
  const res = useResource(() => api.finance(account.id), [account.id], { refreshMs: 5 * 60_000 });
  return (
    <Card
      title={account.name}
      sub={SITE[account.site]}
      right={res.data?.updated_at ? <span className="sub">同步于 {stamp(res.data.updated_at)}</span> : undefined}
    >
      <Async res={res} lines={5}>
        {(f) => <FinanceBody f={f} account={account} balanceRule={balanceRule} onSavedRule={onSavedRule} />}
      </Async>
    </Card>
  );
}

function FinanceBody({ f, account, balanceRule, onSavedRule }: { f: AccountFinance; account: Account; balanceRule?: AlertRule; onSavedRule: (r: AlertRule) => void }) {
  const toast = useToast();
  const bal = f.balance;
  const bill = f.bill;
  const b = bal.data as { currency: string; available_amount: string; available_cash_amount: string } | null;
  const items = (bill.data as { items: import("../api/types").BillOverviewItem[] } | null)?.items;
  const currency = b?.currency ?? (account.site === "international" ? "USD" : "CNY");

  const existing = balanceRule?.params.balance_thresholds?.find((t) => t.account_id === account.id);
  const [threshold, setThreshold] = useState(existing?.amount ?? "");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setThreshold(existing?.amount ?? "");
  }, [existing?.amount]);

  const saveThreshold = async () => {
    if (!balanceRule) return;
    const trimmed = threshold.trim();
    if (trimmed && !/^\d+(\.\d{1,4})?$/.test(trimmed)) {
      toast("请填写有效的提醒金额（如 50 或 100.00），留空取消该账号提醒", true);
      return;
    }
    const others = (balanceRule.params.balance_thresholds ?? []).filter((t) => t.account_id !== account.id);
    const next = trimmed ? [...others, { account_id: account.id, currency, amount: trimmed }] : others;
    setBusy(true);
    try {
      const saved = await api.updateAlertRule("balance_low", { enabled: true, params: { balance_thresholds: next } });
      onSavedRule(saved);
      toast(trimmed ? `已设置低于 ${money(trimmed, currency)} 时告警` : "已清除该账号的低余额阈值");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };

  const dirty = threshold.trim() !== (existing?.amount ?? "");

  return (
    <div className="stack">
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 12, flexWrap: "wrap", paddingBottom: 12, borderBottom: "1px solid var(--border)" }}>
        <div>
          <div className="sub">可用现金余额</div>
          {b ? (
            <>
              <div className="kpi-val" style={{ marginTop: 4 }}>{money(b.available_cash_amount, b.currency)}</div>
              {b.available_amount !== b.available_cash_amount && (
                <div className="sub" style={{ marginTop: 2 }}>含信控总额度 {money(b.available_amount, b.currency)}</div>
              )}
            </>
          ) : (
            <div className="sub" style={{ marginTop: 6 }}>
              {bal.status === "failing" ? <span className="field-error">读取失败：{errorText(bal.last_error?.code ?? "internal_error")}</span> : "等待同步"}
            </div>
          )}
        </div>

        {balanceRule && (
          <div className="inline" style={{ gap: 6 }}>
            <label htmlFor={`bal-th-${account.id}`} className="sub">告警阈值 ({currency})</label>
            <input
              id={`bal-th-${account.id}`}
              type="text"
              inputMode="decimal"
              placeholder="未设置"
              value={threshold}
              onChange={(e) => setThreshold(e.target.value)}
              style={{ width: 96, padding: "5px 9px" }}
            />
            {dirty && (
              <button className="btn sm primary" disabled={busy} onClick={saveThreshold}>
                {busy && <Spinner />} 保存
              </button>
            )}
          </div>
        )}
      </div>

      <div>
        <div className="sub" style={{ fontWeight: 600, marginBottom: 8 }}>当月账单概览（{f.billing_cycle}）</div>
        {!items ? (
          <div className="sub">
            {bill.status === "failing" ? <span className="field-error">读取失败：{errorText(bill.last_error?.code ?? "internal_error")}</span> : "等待同步"}
          </div>
        ) : !items.length ? (
          <div className="sub">当月暂无产生费用的账单项目。</div>
        ) : (
          <div className="table-wrap">
            <table>
              <tbody>
                {items.map((it, k) => (
                  <tr key={k}>
                    <td>
                      <b>{it.product_name}</b>
                      <span className="sub"> · {billTypeName(it.bill_type)}</span>
                    </td>
                    <td className="num">
                      <b>{money(it.pretax_amount, it.currency)}</b>
                      {Number(it.outstanding_amount) > 0 && <div className="sub" style={{ color: "var(--serious)" }}>待支付 {money(it.outstanding_amount, it.currency)}</div>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}

function InstanceMonthlyCostCard({ instances, accounts }: { instances: Instance[] | undefined; accounts: Account[] }) {
  const [selectedId, setSelectedId] = useState<string>("");
  useEffect(() => {
    if (!selectedId && instances?.length) setSelectedId(instances[0].id);
  }, [instances, selectedId]);

  const finRes = useResource(
    () => (selectedId ? api.instanceFinance(selectedId) : Promise.resolve(null)),
    [selectedId],
  );

  if (!instances?.length) return null;
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;

  return (
    <Card
      title="单实例当月费用归集"
      sub="来自阿里云账单中心按实例 ID 归集的原始扣费明细"
      right={
        <select value={selectedId} onChange={(e) => setSelectedId(e.target.value)} aria-label="选择实例查看当月账单">
          {instances.map((i) => (
            <option key={i.id} value={i.id}>
              {i.name} · {accName(i.account_id)}
            </option>
          ))}
        </select>
      }
    >
      <Async res={finRes} lines={3}>
        {(fin) => {
          if (!fin) return null;
          const rows = fin.bill.data?.items ?? [];
          if (fin.bill.status === "failing") {
            return <div className="field-error">读取失败：{errorText(fin.bill.last_error?.code ?? "internal_error")}</div>;
          }
          if (!rows.length) {
            return <EmptyState title={`${fin.billing_cycle} 账期暂无独立扣费项`}>包年包月往期已结清或处于免费额度内的按量项目不产生当月扣费记录。</EmptyState>;
          }
          return (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>计费产品</th>
                    <th>账单类型</th>
                    <th className="num">目录原价</th>
                    <th className="num">折后应付</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((it, idx) => (
                    <tr key={idx}>
                      <td><b>{it.product_name || it.product_code}</b></td>
                      <td className="sub">{billTypeName(it.bill_type)}</td>
                      <td className="num sub">{money(it.pretax_amount, it.currency)}</td>
                      <td className="num"><b>{money(it.after_discount_amount, it.currency)}</b></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          );
        }}
      </Async>
    </Card>
  );
}

/* ---------- Tab 3: Renewals & Idle Resources ---------- */
const IDLE_REASON_TEXT: Record<string, string> = {
  stopped_subscription: "包年包月实例已关机闲置，仍在计费",
  stopped_keep_charging: "按量实例普通停机（未启用节省停机），仍收计算与磁盘费",
  stopped_not_charge_exempt: "实例已停机但未进入免计费状态",
  unattached_cloud_disk: "未挂载的独立云盘仍在持续计费",
  disk_on_stopped_instances: "挂载于停机实例上的云盘仍在持续计费",
  unbound_eip: "未绑定实例的弹性公网 IP（可能产生闲置占用费）",
};

const IDLE_KIND_TEXT: Record<string, string> = {
  ecs: "ECS 实例",
  swas: "轻量服务器",
  disk: "云盘",
  eip: "弹性公网 IP",
};

function IdleAndRenewalSubWorkspace({ accounts, instances }: { accounts: Account[]; instances: Instance[] | undefined }) {
  const now = useNow(60_000);
  const idleRes = useResource(() => api.idleResources({ limit: 100 }), [], { refreshMs: 120_000 });
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;
  const subRows = (instances ?? []).filter((i) => i.expires_at).sort((a, b) => a.expires_at!.localeCompare(b.expires_at!));

  return (
    <div className="stack">
      <Card title="包年包月到期与续费状态" sub="自动追踪预付费 ECS 与轻量应用服务器的到期倒计时">
        {!instances ? (
          <Skeleton lines={3} />
        ) : !subRows.length ? (
          <EmptyState title="当前无包年包月实例">按量付费与抢占式实例无固定到期日。</EmptyState>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>实例名称</th>
                  <th>所属账号与地域</th>
                  <th>到期日期</th>
                  <th>剩余时间</th>
                  <th>续费策略</th>
                </tr>
              </thead>
              <tbody>
                {subRows.map((i) => {
                  const diffMs = Date.parse(i.expires_at!) - now;
                  const expired = diffMs <= 0;
                  const days = Math.ceil(diffMs / 86400_000);
                  const r = i.renewal ? RENEWAL[i.renewal.status] : RENEWAL.unknown;
                  const soon = !expired && days <= 7 && i.renewal?.status !== "auto_renewal";
                  return (
                    <tr key={i.id}>
                      <td><b>{i.name}</b></td>
                      <td className="sub">{accName(i.account_id)} · {regionName(i.region_id)}</td>
                      <td className="mono">{ymd(i.expires_at!)}</td>
                      <td>
                        {expired ? (
                          <Badge level="critical">已过期</Badge>
                        ) : soon ? (
                          <Badge level="critical">剩余 {days} 天</Badge>
                        ) : days <= 15 ? (
                          <Badge level="warning">剩余 {days} 天</Badge>
                        ) : (
                          <span className="num">剩余 {days} 天</span>
                        )}
                      </td>
                      <td><Badge level={r[0]}>{r[1]}</Badge></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title="闲置计费资源巡检" sub="识别未绑定 EIP、未挂载云盘及普通停机未省钱的按量实例">
        <Async res={idleRes} lines={4}>
          {(page) =>
            !page.items.length ? (
              <EmptyState title="未发现闲置浪费资源">各账号下的弹性公网 IP、云盘与停机实例均处于正常使用或免计费状态。</EmptyState>
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>资源名称 / ID</th>
                      <th>类型与地域</th>
                      <th>计费模式</th>
                      <th>闲置原因</th>
                      <th>巡检时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    {page.items.map((c: IdleCandidate) => (
                      <tr key={c.id}>
                        <td>
                          <b>{c.name}</b>
                          <div className="sub">
                            {accName(c.account_id)} · <span className="mono">{c.cloud_id}</span>
                            {c.public_ip ? ` · ${c.public_ip}` : ""}
                            {c.size_gib ? ` · ${c.size_gib} GiB` : ""}
                          </div>
                        </td>
                        <td>
                          <Badge level="warning">{IDLE_KIND_TEXT[c.kind] ?? c.kind}</Badge>
                          <div className="sub">{regionName(c.region_id)}</div>
                        </td>
                        <td className="sub">{CHARGE[c.charge_type as Instance["charge_type"]] ?? c.charge_type}</td>
                        <td className="wrap">{IDLE_REASON_TEXT[c.reason] ?? c.reason}</td>
                        <td className="sub">{stamp(c.observed_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Async>
      </Card>
    </div>
  );
}

/* ---------- Tab 4: Official Observations & Reconciliations ---------- */
function ObservationsCard({ accountId, accountName }: { accountId: string; accountName?: string }) {
  const [cursor, setCursor] = useState<string>();
  const [view, setView] = useState<View>("table");
  const res = useResource(() => api.trafficObservations(accountId, cursor), [accountId, cursor], { refreshMs: cursor ? 0 : 60_000 });

  return (
    <Card
      title={`${accountName ? `${accountName} · ` : ""}阿里云 CDT 官方累计读取记录`}
      sub="每次向阿里云 CDT 接口拉取到的当月累计流量原始值"
      right={<SegToggle label="显示方式" value={view} onChange={setView} options={[["table", "表格"], ["chart", "曲线"]]} />}
    >
      <Async res={res} lines={5}>
        {(page) => {
          const points = [...page.items].reverse();
          return (
            <>
              {!points.length ? (
                <EmptyState title="暂无官方读取记录" />
              ) : view === "chart" ? (
                <LineChart
                  ariaLabel="官方累计值读取历史"
                  unit="GB"
                  labels={points.map((p) => stamp(p.fetched_at))}
                  series={[
                    { name: "海外累计", color: "var(--series-1)", data: points.map((p) => (p.overseas_bytes === null ? null : gbValue(p.overseas_bytes))) },
                    { name: "国内累计", color: "var(--series-2)", data: points.map((p) => (p.mainland_bytes === null ? null : gbValue(p.mainland_bytes))) },
                  ]}
                />
              ) : (
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>读取时间</th>
                        <th className="num">海外累计</th>
                        <th className="num">国内累计</th>
                        <th>接口状态</th>
                      </tr>
                    </thead>
                    <tbody>
                      {page.items.map((p) => (
                        <tr key={p.id}>
                          <td>{stamp(p.fetched_at)}</td>
                          <td className="num">{gb(p.overseas_bytes)}</td>
                          <td className="num">{gb(p.mainland_bytes)}</td>
                          <td>{p.error ? <Badge level="warning">读取失败</Badge> : <Badge level="good">正常</Badge>}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {(cursor || page.next_cursor) && (
                <div className="inline" style={{ marginTop: 12 }}>
                  {cursor && <button className="btn sm" onClick={() => setCursor(undefined)}>返回最新</button>}
                  {page.next_cursor && <button className="btn sm" onClick={() => setCursor(page.next_cursor!)}>查看更早</button>}
                </div>
              )}
            </>
          );
        }}
      </Async>
    </Card>
  );
}

function ReconciliationsCard({ accountId, accounts }: { accountId?: string; accounts: Account[] }) {
  const res = useResource(() => api.trafficReconciliations({ account_id: accountId, limit: 30 }), [accountId]);
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;

  return (
    <Card title="官方累计与采集估算对账记录" sub="在同一统计截止时刻下验证本地采集增量与阿里云账单流量的一致性">
      <Async res={res} lines={4}>
        {(p) =>
          !p.items.length ? (
            <EmptyState title="暂无可比对的对账记录">当阿里云返回明确截止时刻且该时段采集完整覆盖时自动生成核对结果。</EmptyState>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>核对时间</th>
                    <th>账号与范围</th>
                    <th className="num">本地估算值</th>
                    <th className="num">官方出账值</th>
                    <th className="num">相对偏差</th>
                    <th>结论</th>
                  </tr>
                </thead>
                <tbody>
                  {p.items.map((r) => (
                    <tr key={r.id}>
                      <td>{stamp(r.checked_at)}</td>
                      <td><b>{accName(r.account_id)}</b> · {SCOPE[r.scope]}</td>
                      <td className="num">{gb(r.predicted_bytes, 2)}</td>
                      <td className="num">{gb(r.official_bytes, 2)}</td>
                      <td className="num">{r.relative_error === null ? "—" : `${num(r.relative_error * 100, 2)}%`}</td>
                      <td>
                        {r.status !== "comparable" ? (
                          <Badge level="neutral">{r.status === "duplicate_cutoff" ? "截止未推进" : "条件不足"}</Badge>
                        ) : r.exceeds_ten_percent ? (
                          <Badge level="warning">偏差 &gt;10%（连续 {r.bad_streak} 次）</Badge>
                        ) : (
                          <Badge level="good">吻合正常</Badge>
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
    </Card>
  );
}

/* ---------- Pricing Modal ---------- */
function PricingModal({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const res = useResource(api.trafficPricing, []);
  return (
    <Modal title="超额流量阶梯单价设置" onClose={onClose} wide>
      <p>
        用于预估超出免费额度后的按量流量费用（1 GB = 1024³ 字节）。格式为 <span className="mono">上限GB:单价</span>，末段以 <span className="mono">*:单价</span> 结尾。
      </p>
      <Async res={res}>
        {(p) => (
          <PricingForm
            initial={p}
            onClose={onClose}
            onSaved={() => {
              toast("阶梯单价已保存");
              onSaved();
            }}
          />
        )}
      </Async>
    </Modal>
  );
}

function PricingForm({ initial, onClose, onSaved }: { initial: TrafficPricing; onClose: () => void; onSaved: () => void }) {
  const [cnMain, setCnMain] = useState(tiersToText(initial.china.mainland_tiers));
  const [cnOver, setCnOver] = useState(tiersToText(initial.china.overseas_tiers));
  const [intlMain, setIntlMain] = useState(tiersToText(initial.international.mainland_tiers));
  const [intlOver, setIntlOver] = useState(tiersToText(initial.international.overseas_tiers));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const t1 = parseTiers(cnMain), t2 = parseTiers(cnOver), t3 = parseTiers(intlMain), t4 = parseTiers(intlOver);
    if (!t1 || !t2 || !t3 || !t4) return setError("阶梯格式有误，请按 10240:0.50, *:0.40 填写，最后一段须以 *: 开头。");
    setBusy(true);
    setError(undefined);
    try {
      await api.updateTrafficPricing({
        revision: initial.revision,
        china: { currency: initial.china.currency, mainland_tiers: t1, overseas_tiers: t2 },
        international: { currency: initial.international.currency, mainland_tiers: t3, overseas_tiers: t4 },
      });
      onSaved();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      <div className="grid g2">
        <div>
          <label className="lbl" htmlFor="pr-cn-m">国内站 · 中国内地（{initial.china.currency}/GB）</label>
          <input id="pr-cn-m" type="text" value={cnMain} onChange={(e) => setCnMain(e.target.value)} />
        </div>
        <div>
          <label className="lbl" htmlFor="pr-cn-o">国内站 · 非中国内地（{initial.china.currency}/GB）</label>
          <input id="pr-cn-o" type="text" value={cnOver} onChange={(e) => setCnOver(e.target.value)} />
        </div>
        <div>
          <label className="lbl" htmlFor="pr-in-m">国际站 · 中国内地（{initial.international.currency}/GB）</label>
          <input id="pr-in-m" type="text" value={intlMain} onChange={(e) => setIntlMain(e.target.value)} />
        </div>
        <div>
          <label className="lbl" htmlFor="pr-in-o">国际站 · 非中国内地（{initial.international.currency}/GB）</label>
          <input id="pr-in-o" type="text" value={intlOver} onChange={(e) => setIntlOver(e.target.value)} />
        </div>
      </div>
      {error && <div className="field-error" role="alert">{error}</div>}
      <div className="actions">
        <button type="button" className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy}>{busy && <Spinner />} 保存单价</button>
      </div>
    </form>
  );
}
