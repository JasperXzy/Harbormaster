// 流量：官方与实时估算对比、超额单价与费用估算、每日用量、累计与预测、计费对象（今天/本月归因）、共享带宽每小时收发、对账记录。
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { RULE, SCOPE, SUBJECT_KIND } from "../api/labels";
import type { Account, BandwidthPackage, DailyTraffic, QuotaScope, QuotaStatus, TrafficPricing } from "../api/types";
import { TipRow } from "../components/charts/common";
import { DailyBars } from "../components/charts/DailyBars";
import { LineChart } from "../components/charts/LineChart";
import { QuotaMeter, QuotaUnknownNote } from "../components/QuotaMeter";
import { Async, Badge, Banner, Card, EmptyState, ErrorState, errMsg, Fresh, Modal, SegToggle, Skeleton, Spinner, useToast } from "../components/ui";
import { dailyValue, forecastNote, usageTimeline } from "../lib/daily";
import { useShared } from "../lib/data";
import { bytes, gb, gbValue, num, shortDate, stamp, time } from "../lib/format";
import { parseTiers, tiersToText } from "../lib/pricing";
import { Link } from "../lib/router";
import { useResource, type Resource } from "../lib/useResource";
import { quotaMissingReasons, quotaState, quotaView } from "../lib/usage";

type View = "chart" | "table";
const VIEW: [View, string][] = [["chart", "图表"], ["table", "表格"]];
const CURRENCY_SYM: Record<string, string> = { CNY: "¥", USD: "$" };

export function Traffic() {
  const { accounts, quotas, instances } = useShared();
  const [accountId, setAccountId] = useState("all");
  const [pricingOpen, setPricingOpen] = useState(false);
  const sims = useResource(api.quotaSimulations, [], { refreshMs: 30_000 });
  const accs = accounts.data;

  if (accounts.error) return <div className="card"><ErrorState error={accounts.error} onRetry={accounts.reload} /></div>;
  if (!accs) return <div className="card"><Skeleton lines={6} /></div>;
  if (!accs.length)
    return (
      <div className="card">
        <EmptyState title="还没有流量数据" action={<Link className="btn primary" to="/settings">去添加账号</Link>}>添加阿里云账号后，这里会显示每天用了多少流量、月底大概会用到多少。</EmptyState>
      </div>
    );
  const qs = (quotas.data ?? []).filter((q) => q.account_id === accountId);
  const enabledAccounts = accs.filter((a) => a.enabled);
  const all = accountId === "all";
  const instName = (id: string) => instances.data?.items.find((i) => i.id === id)?.name ?? id;

  return (
    <>
      {(sims.data ?? []).length > 0 && (
        <Banner level="warning" action={<Link className="btn sm" to="/automation">管理模拟</Link>}>
          <b>当前有 {sims.data!.length} 项模拟用量演练正在生效。</b> 流量页展示的仍是真实读取与估算，模拟值只作用于自动化保护规则验证。
        </Banner>
      )}

      <div className="filters">
        <label className="sub" htmlFor="traffic-account">账号</label>
        <select id="traffic-account" value={accountId} onChange={(e) => setAccountId(e.target.value)}>
          <option value="all">全部账号（合计）</option>
          {accs.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
        <div className="spacer" />
        <button className="btn sm" onClick={() => setPricingOpen(true)}>超额单价设置</button>
      </div>

      {all && <p className="note">汇总 {enabledAccounts.length} 个启用账号，停用账号不计入。免费额度按账号独立计算，合计额度不能跨账号使用。</p>}
      {all ? <AggregateSection /> : <>
        <QuotaSection res={{ ...quotas, data: quotas.data ? qs : undefined }} />
        <DailySection key={accountId} accountId={accountId} quotas={qs} />
      </>}
      {all ? enabledAccounts.map((a) => <Observations key={a.id} accountId={a.id} accountName={a.name} />) : <Observations key={accountId} accountId={accountId} />}
      <Reconciliations accountId={all ? undefined : accountId} accounts={accs} />
      <Subjects accountId={accountId} instName={instName} accounts={accs} />
      <Packages accountId={accountId} accounts={accs} />

      {pricingOpen && <PricingModal onClose={() => setPricingOpen(false)} onSaved={() => { setPricingOpen(false); quotas.reload(); }} />}
    </>
  );
}

function QuotaSection({ res, total = false }: { res: Pick<Resource<QuotaStatus[]>, "data" | "error" | "reload">; total?: boolean }) {
  return <Card title={total ? "本月用量 · 合计" : "本月用量"} sub={total ? "分别合计国内、海外用量；缺数据时保留已知小计" : "同时保留官方累计和采集估算；未核实的窗口会单独标注"}>
    {res.error ? <ErrorState error={res.error} onRetry={res.reload} /> : !res.data ? <Skeleton /> : !res.data.length ? <EmptyState title={total ? "没有启用的账号" : "这个账号还没有额度数据"} /> : <QuotaTable qs={res.data} />}
  </Card>;
}

function AggregateSection() {
  const quotas = useResource(() => api.quotas("all"), [], { refreshMs: 10_000 });
  return <><QuotaSection res={quotas} total /><DailySection accountId="all" quotas={quotas.data ?? []} /></>;
}

function overageText(oc: QuotaStatus["overage_cost"]): string | null {
  if (!oc) return null;
  const sym = CURRENCY_SYM[oc.currency] ?? oc.currency + " ";
  if (oc.estimated_cost !== null && Number(oc.estimated_cost) > 0) {
    return `已超额约 ${sym}${oc.estimated_cost}${oc.forecast_cost && Number(oc.forecast_cost) > Number(oc.estimated_cost) ? `（月底约 ${sym}${oc.forecast_cost}）` : ""}`;
  }
  if (oc.forecast_cost !== null && Number(oc.forecast_cost) > 0) {
    return `预计月底超额约 ${sym}${oc.forecast_cost}`;
  }
  return null;
}

function QuotaTable({ qs }: { qs: QuotaStatus[] }) {
  const order: QuotaScope[] = ["overseas", "mainland"];
  const rows = order.map((s) => qs.find((q) => q.scope === s)).filter((q): q is QuotaStatus => !!q);
  return (
    <>
      <div className="only-narrow">{rows.map((q) => <QuotaMeter key={q.scope} q={q} />)}</div>
      <div className="table-wrap only-wide">
        <table>
          <thead>
            <tr><th>范围</th><th className="num">实时估算</th><th className="num">免费额度</th><th className="num">预测月底 / 超额费</th><th>更新</th></tr>
          </thead>
          <tbody>
            {rows.map((q) => {
              const v = quotaView(q);
              const cost = overageText(q.overage_cost);
              return (
                <tr key={q.scope}>
                  <td><b>{SCOPE[q.scope]}</b></td>
                  <td className="num">
                    <b>{v.value === null ? "—" : gb(v.value, 2)}</b> <Badge level={v.unverified || !v.usable ? "warning" : "neutral"}>{quotaState(q)}</Badge>
                    <div className="sub">官方 {q.aggregation && q.official_bytes === null && q.aggregation.official_accounts > 0 ? `已知小计 ${gb(q.aggregation.known_official_bytes)}（${q.aggregation.official_accounts}/${q.aggregation.account_count} 账号）` : gb(q.official_bytes)}{q.official_as_of ? `（截至 ${time(q.official_as_of)}）` : "（截止时间未知）"}{v.usable && v.increment !== null && ` + 采集 ${bytes(v.increment)}`}</div>
                    {!v.usable && <div className="sub">{quotaMissingReasons(q)}{v.knownIncrement > 0 && `；已采集部分 ${bytes(v.knownIncrement)}`}</div>}
                    {q.aggregation && <div className="sub">{q.aggregation.estimated_accounts}/{q.aggregation.account_count} 个账号的估算可用{!v.usable && q.aggregation.estimated_accounts > 0 ? `；已知估算小计 ${gb(q.aggregation.known_estimated_bytes, 2)}` : ""}</div>}
                    {v.worstCase !== null && v.worstCase > 0 && <div className="sub">含掉线推算 {bytes(v.worstCase)}</div>}
                  </td>
                  <td className="num">{gb(q.quota_bytes, 0)}{v.usable && v.value !== null && q.quota_bytes > 0 && <div className="sub">{v.unverified ? "估算" : ""}已用 {num((v.value / q.quota_bytes) * 100, 1)}%</div>}</td>
                  <td className="num">
                    {q.forecast ? gb(q.forecast.cycle_end_bytes) : "—"}
                    <div className="sub">
                      {!q.forecast ? (q.aggregation ? (q.aggregation.forecast_accounts > 0 ? "部分账号预测不可用" : "等待各账号预测") : q.forecast_status === "baseline_missing" ? "累计基准尚不可用" : "等待首个完整日") : `${q.aggregation ? `${q.aggregation.forecast_accounts} 个账号预测合计` : `${q.forecast.sample_days ?? "—"} 个完整日`}${q.forecast.unverified ? " · 未核实" : ""}`}
                      {q.forecast?.exceeds_on && <div>{q.forecast.already_exceeded ? "累计已超本地额度" : `预计 ${shortDate(q.forecast.exceeds_on)} 超出`}</div>}
                      {cost && <div style={{ color: "var(--serious)" }}>{cost}</div>}
                      {q.aggregation && q.aggregation.forecast_accounts > 0 && !q.forecast && <div>已知预测小计 {gb(q.aggregation.known_forecast_bytes)}（{q.aggregation.forecast_accounts}/{q.aggregation.account_count} 账号）</div>}
                      {!!q.aggregation?.forecast_exceeding_accounts && <div>{q.aggregation.forecast_exceeding_accounts} 个账号预测超额，请单独查看</div>}
                    </div>
                  </td>
                  <td><Fresh at={v.updatedAt} kind="realtime" prefix="" />{q.official_status === "unavailable" && <div className="sub">沿用上次官方基准</div>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <QuotaUnknownNote quotas={rows} />
    </>
  );
}

function DailyCell({ day, scope }: { day: DailyTraffic["days"][number]; scope: QuotaScope }) {
  const v = dailyValue(day, scope);
  return <><b>{v.value === null ? "—" : v.value < 10 * 1024 ** 2 ? bytes(v.value) : gb(v.value, 2)}</b><div className="sub">{v.label}</div>{v.partial && v.from && v.until && <div className="sub">观测 {time(v.from)}–{time(v.until)}</div>}</>;
}

function DailySection({ accountId, quotas }: { accountId: string; quotas: QuotaStatus[] }) {
  const daily = useResource(() => api.dailyTraffic(accountId), [accountId], { refreshMs: 30_000 });
  const [view, setView] = useState<View>("chart");
  const [scope, setScope] = useState<QuotaScope>(() => (quotas.length === 1 ? quotas[0].scope : "overseas"));
  const activeScope: QuotaScope = quotas.length === 1 && quotas[0].scope !== scope ? quotas[0].scope : scope;
  const scopeOpts: [QuotaScope, string][] = (["overseas", "mainland"] as const)
    .filter((s) => !quotas.length || quotas.some((q) => q.scope === s))
    .map((s) => [s, s === "overseas" ? "海外" : "国内"]);
  return <div className="grid g2">
    <Card title="每天用了多少" sub="按计费对象汇总，保留缺口" right={<SegToggle label="显示方式" value={view} onChange={setView} options={VIEW} />}>
      <Async res={daily} lines={6}>{(d) => <>
        {!d.days.length ? <EmptyState title="这个计费周期还没有数据" /> : view === "chart" ? <>
          <div className="legend" style={{ marginBottom: 8 }}><span><i style={{ background: "var(--series-1)" }} />{SCOPE.overseas}</span><span><i style={{ background: "var(--series-2)" }} />{SCOPE.mainland}</span><span>浅色虚线边：仅已采集部分</span></div>
          <DailyBars ariaLabel="每日流量用量，浅色虚线边表示部分覆盖" unit="GB" nameA={SCOPE.overseas} nameB={SCOPE.mainland} totalSlots={cycleDays(d)} bars={d.days.map((day) => { const a = dailyValue(day, "overseas"), b = dailyValue(day, "mainland"); return { label: shortDate(day.date), a: a.value === null ? null : gbValue(a.value), b: b.value === null ? null : gbValue(b.value), partialA: a.partial, partialB: b.partial, provisional: day.provisional }; })} />
          {d.days.some((day) => ["mainland", "overseas"].some((sc) => dailyValue(day, sc as QuotaScope).value !== null)) ? <div className="inline" style={{ gap: 24, marginTop: 8 }}>{(["overseas", "mainland"] as const).map((sc) => <div key={sc}><span className="sub">{shortDate(d.days.at(-1)!.date)} · {SCOPE[sc]} </span><DailyCell day={d.days.at(-1)!} scope={sc} /></div>)}</div> : <p className="note">{d.days.some((day) => day.scopes?.overseas.status === "pending") ? "正在汇总已接收的采集数据。" : "尚无可归属的采集数据，未知日期保留为空。"}</p>}
        </> : <div className="table-wrap" style={{ maxHeight: 340, overflowY: "auto" }}><table><thead><tr><th>日期</th><th className="num">{SCOPE.overseas}</th><th className="num">{SCOPE.mainland}</th></tr></thead><tbody>{[...d.days].reverse().map((day) => <tr key={day.date}><td>{shortDate(day.date)}{day === d.days.at(-1) && <div className="sub">今天未结束</div>}</td><td className="num"><DailyCell day={day} scope="overseas" /></td><td className="num"><DailyCell day={day} scope="mainland" /></td></tr>)}</tbody></table></div>}
        <p className="note">按 {d.data_timezone} 划分日期。官方日数据优先；没有时使用采集估算。部分覆盖的值只表示已采集小计，不是全天总量；空缺不按 0 处理。跨边界样本按上界分配，可能重复计入相邻时段。{d.updated_at && `最近汇总：${stamp(d.updated_at)}。`}</p>
      </>}</Async>
    </Card>
    <Card title="累计与预测" sub="近 7 天中的完整日样本，今天不参加日均" right={scopeOpts.length > 1 ? <SegToggle label="范围" value={activeScope} onChange={setScope} options={scopeOpts} /> : undefined}>
      <Async res={daily} lines={6}>{(d) => { const q = quotas.find((x) => x.scope === activeScope); return !d.days.length || !q ? <EmptyState title="还没有足够的数据画图" /> : <Cumulative d={d} q={q} scope={activeScope} />; }}</Async>
    </Card>
  </div>;
}

function Observations({ accountId, accountName }: { accountId: string; accountName?: string }) {
  const [cursor, setCursor] = useState<string>();
  const [view, setView] = useState<View>("table");
  const res = useResource(() => api.trafficObservations(accountId, cursor), [accountId, cursor], { refreshMs: cursor ? 0 : 60_000 });
  return <details className="card fold">
    <summary><h2>{accountName ? `${accountName} · ` : ""}官方累计读取记录</h2><span className="sub">每次从阿里云读到的本月累计总数；不是每天的用量</span><div className="spacer" /><span className="sub">展开</span></summary>
    <div className="inline" style={{ justifyContent: "flex-end", margin: "12px 0 8px" }}><SegToggle label="读取记录显示方式" value={view} onChange={setView} options={[["table", "表格"], ["chart", "图表"]]} /></div>
    <Async res={res}>{(page) => {
      const points = [...page.items].reverse();
      return <>
        {!points.length ? <EmptyState title="还没有读取记录" /> : view === "chart" ? <LineChart
          ariaLabel="官方累计值的读取历史" unit="GB" labels={points.map((p) => stamp(p.fetched_at))}
          series={[
            { name: "海外累计", color: "var(--series-1)", data: points.map((p) => (p.overseas_bytes === null ? null : gbValue(p.overseas_bytes))) },
            { name: "国内累计", color: "var(--series-2)", data: points.map((p) => (p.mainland_bytes === null ? null : gbValue(p.mainland_bytes))) },
          ]}
        /> : <div className="table-wrap"><table><thead><tr><th>读取时间</th><th className="num">海外累计</th><th className="num">国内累计</th><th>结果</th></tr></thead><tbody>{page.items.map((p) => <tr key={p.id}><td>{stamp(p.fetched_at)}</td><td className="num">{gb(p.overseas_bytes)}</td><td className="num">{gb(p.mainland_bytes)}</td><td>{p.error ? <Badge level="warning">这次没取到</Badge> : <span className="sub">已读取</span>}</td></tr>)}</tbody></table></div>}
        {(cursor || page.next_cursor) && <div className="inline" style={{ marginTop: 12 }}>
          {cursor && <button className="btn" onClick={() => setCursor(undefined)}>回到最新记录</button>}
          {page.next_cursor && <button className="btn" onClick={() => setCursor(page.next_cursor!)}>查看更早记录</button>}
        </div>}
      </>;
    }}</Async>
  </details>;
}

function Reconciliations({ accountId, accounts }: { accountId?: string; accounts: Account[] }) {
  const [open, setOpen] = useState(false);
  const res = useResource(() => (open ? api.trafficReconciliations({ account_id: accountId, limit: 20 }) : Promise.resolve(null)), [open, accountId]);
  const accName = (id: string) => accounts.find((a) => a.id === id)?.name ?? id;
  return (
    <details className="card fold" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary><h2>官方与实时估算对账记录</h2><span className="sub">同一截止时刻下采集估算值与阿里云官方值的偏差比较</span><div className="spacer" /><span className="sub">展开</span></summary>
      {open && (
        <div style={{ marginTop: 12 }}>
          <Async res={res}>
            {(p) =>
              !p || !p.items.length ? (
                <EmptyState title="还没有可比较的对账记录">需要阿里云返回明确的官方统计截止时刻，且同一窗口内有完整采集覆盖时才会生成对比。</EmptyState>
              ) : (
                <div className="table-wrap">
                  <table>
                    <thead><tr><th>检查时间</th><th>账号 / 范围</th><th className="num">采集估算</th><th className="num">官方数值</th><th className="num">相对偏差</th><th>状态</th></tr></thead>
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
                              <Badge level="neutral">{r.status === "duplicate_cutoff" ? "截止时刻未推进" : "条件不足未对比"}</Badge>
                            ) : r.exceeds_ten_percent ? (
                              <Badge level="warning">偏差超 10%（连续 {r.bad_streak} 次）</Badge>
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
        </div>
      )}
    </details>
  );
}

const cycleDays = (d: DailyTraffic) => Math.max(d.days.length, Math.round((Date.parse(d.cycle_end) - Date.parse(d.cycle_start)) / 86400_000));

function Cumulative({ d, q, scope }: { d: DailyTraffic; q: QuotaStatus; scope: QuotaScope }) {
  const s = usageTimeline(d, q, scope), color = scope === "overseas" ? "var(--series-1)" : "var(--series-2)";
  const quota = gbValue(q.quota_bytes), f = q.forecast;
  const convert = (values: (number | null)[]) => values.map((v) => (v === null ? null : gbValue(v)));
  const hasValues = [...s.official, ...s.collected, ...s.realtime, ...s.projected].some((v) => v !== null);
  return <>
    {hasValues ? <>
      <div className="legend" style={{ marginBottom: 8 }}>
        {s.official.some((v) => v !== null) && <span><i className="line" style={{ background: "var(--muted)" }} />官方累计读取值</span>}
        {s.collected.some((v) => v !== null) && <span><i className="line" style={{ background: color }} />采集累计</span>}
        {s.realtime.some((v) => v !== null) && <span><i style={{ background: "var(--realtime)" }} />当前估算</span>}
        {f && <span><i className="dash" style={{ borderColor: color }} />月底预测</span>}
      </div>
      <LineChart ariaLabel={`${SCOPE[scope]}累计读取、实时估算与月底预测`} labels={s.labels} unit="GB" height={250} digits={2}
        series={[{ name: "官方累计读取值", color: "var(--muted)", data: convert(s.official), endLabel: false }, { name: "采集累计", color, data: convert(s.collected), endLabel: false }, { name: "当前估算", color: "var(--realtime)", data: convert(s.realtime), endLabel: false }, { name: "预测", color, data: convert(s.projected), dashed: true, endLabel: false }]}
        refLine={{ value: quota, label: `本地额度 ${num(quota, 0)} GB` }} />
    </> : <EmptyState title="正在积累累计数据" />}
    {f && <p><b>月底预计 {gb(f.cycle_end_bytes, 2)}</b>{f.exceeds_on && <span> · {f.already_exceeded ? "累计已超本地额度" : `预计 ${shortDate(f.exceeds_on)} 超出`}</span>}</p>}
    <p className="note">{forecastNote(q)}{f?.sample_dates?.length ? `样本日期：${f.sample_dates.map(shortDate).join("、")}。` : ""}</p>
    <p className="note">{q.aggregation ? "官方曲线合计各账号同一读取日期的月累计，读取时间和统计截止可能不同。" : "官方曲线按读取日期展示月累计，统计截止时间仍可能未知。"}缺少整月采集覆盖时不拼接采集累计；预测使用服务端返回的基准和预测点，仅用于展示。</p>
  </>;
}

function Subjects({ accountId, instName, accounts }: { accountId: string; instName: (id: string) => string; accounts: Account[] }) {
  const [period, setPeriod] = useState<"today" | "cycle">("today");
  const enabled = accounts.filter((a) => a.enabled).map((a) => a.id);
  const res = useResource(async () => {
    const rows = await api.billingSubjects(accountId === "all" ? undefined : accountId);
    return accountId === "all" ? rows.filter((s) => enabled.includes(s.account_id)) : rows;
  }, [accountId, enabled.join(",")], { refreshMs: 30_000 });
  const src = { agent: "", cloud_monitor: "数据来自云监控，晚 1–2 分钟", none: "没有数据" } as const;
  return (
    <Card
      title="流量是按什么计费的"
      sub={period === "today" ? "今天到现在，每个公网出口各算各的" : "本计费周期累计归因与在所属免费额度范围内的占比"}
      right={<SegToggle label="统计周期" value={period} onChange={setPeriod} options={[["today", "今天"], ["cycle", "本月归因"]]} />}
    >
      <Async res={res} lines={5}>
        {(items) =>
          !items.length ? <EmptyState title="这个账号下没有公网出口" /> : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>公网出口</th>
                    <th>怎么算</th>
                    <th className="num">收到</th>
                    <th className="num">发出</th>
                    <th className="num">计费流量</th>
                    {period === "cycle" && <th className="num">范围占比</th>}
                    <th>计入免费额度</th>
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
                          {accountId === "all" && <div className="sub">账号 {accounts.find((a) => a.id === s.account_id)?.name}</div>}
                          <div className="sub"><span className="mono">{s.public_ips.join("、")}</span>{s.kind.startsWith("cbwp") ? ` · 成员 ${s.instance_ids.map(instName).join("、")}` : ""}</div>
                        </td>
                        <td>{SUBJECT_KIND[s.kind]}<div className="sub">{RULE[s.metering_rule]}{s.data_source === "agent" ? "" : ` · ${src[s.data_source]}`}</div></td>
                        <td className="num">{rx === null ? <span className="sub">—</span> : gb(rx)}</td>
                        <td className="num">{tx === null ? <span className="sub">—</span> : gb(tx)}</td>
                        <td className="num">{metered === null ? <span className="sub">—</span> : <b>{gb(metered)}</b>}</td>
                        {period === "cycle" && <td className="num">{s.cycle_share_percent == null ? <span className="sub">—</span> : `${num(s.cycle_share_percent, 1)}%`}</td>}
                        <td>{s.counts_toward_cdt === null ? <Badge level="warning">待确认</Badge> : s.counts_toward_cdt && s.cdt_scope ? <Badge level="good">{SCOPE[s.cdt_scope]}</Badge> : <span className="sub">不计入</span>}</td>
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
  return <>{mine.map((p) => <PackageHourly key={p.id} p={p} accountName={accountId === "all" ? accounts.find((a) => a.id === p.account_id)?.name : undefined} />)}</>;
}

function PackageHourly({ p, accountName }: { p: BandwidthPackage; accountName?: string }) {
  const res = useResource(() => api.bandwidthPackageHourly(p.id), [p.id], { refreshMs: 60_000 });
  const [view, setView] = useState<View>("chart");
  return (
    <Card title={`共享带宽 ${p.name}${accountName ? `（${accountName}）` : ""} · 今天每小时收发`} right={<SegToggle label="显示方式" value={view} onChange={setView} options={VIEW} />}>
      <Async res={res} lines={6}>
        {(h) => {
          if (!h.periods.length) return <EmptyState title="今天还没有数据" />;
          const open = Date.parse(h.periods[h.periods.length - 1].start) + h.period_seconds * 1000 > Date.now();
          const done = open ? h.periods.slice(0, -1) : h.periods;
          const labels = done.map((x) => time(x.start));
          if (view === "chart" && done.length < 2) return <EmptyState title="已完成的小时数据还不够画图">至少需要两个已完成的小时记录。可以先切到表格查看已有数据。</EmptyState>;
          return view === "chart" ? (
            <>
              <div className="legend" style={{ marginBottom: 8 }}>
                <span><i className="line" style={{ background: "var(--series-1)" }} />发出</span>
                <span><i className="line" style={{ background: "var(--series-3)" }} />收到</span>
              </div>
              <LineChart
                ariaLabel={`${p.name} 每小时收发流量`} labels={labels} unit="GB" digits={2}
                series={[{ name: "发出", color: "var(--series-1)", data: done.map((x) => gbValue(x.tx_bytes)) }, { name: "收到", color: "var(--series-3)", data: done.map((x) => gbValue(x.rx_bytes)) }]}
                extraTip={(i) => <TipRow label="按这个计费" value={`${done[i].rx_bytes >= done[i].tx_bytes ? "收到" : "发出"} ${num(gbValue(done[i].metered_bytes), 2)} GB`} />}
              />
              {open && <p className="note">当前这一小时还没结束，没有画在图上；切到表格可以看到。</p>}
            </>
          ) : (
            <div className="table-wrap" style={{ maxHeight: 300, overflowY: "auto" }}>
              <table>
                <thead><tr><th>开始时间</th><th className="num">收到</th><th className="num">发出</th><th className="num">计费流量</th></tr></thead>
                <tbody>
                  {[...h.periods].reverse().map((x) => (
                    <tr key={x.start}><td>{time(x.start)}{open && x === h.periods[h.periods.length - 1] && <span className="sub"> · 还没结束</span>}</td><td className="num">{gb(x.rx_bytes, 2)}</td><td className="num">{gb(x.tx_bytes, 2)}</td><td className="num"><b>{gb(x.metered_bytes, 2)}</b></td></tr>
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

function PricingModal({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const res = useResource(api.trafficPricing, []);
  return (
    <Modal title="超额流量阶梯单价" onClose={onClose} wide>
      <p>用于估算超出免费额度后的流量费用（1 GB = 1024³ 字节）。格式为 <span className="mono">上限GB:单价</span>，最后一段用 <span className="mono">*:单价</span> 表示封顶后的单价。</p>
      <Async res={res}>
        {(p) => <PricingForm initial={p} onClose={onClose} onSaved={() => { toast("阶梯单价已保存"); onSaved(); }} />}
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
    if (!t1 || !t2 || !t3 || !t4) return setError("阶梯格式有误，请按 10240:0.50, *:0.40 填写，最后一段必须以 *: 开头。");
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
