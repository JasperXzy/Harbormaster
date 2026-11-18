// 流量：官方与实时估算对比、每日用量、累计与预测、计费对象、共享带宽每小时收发。
import { useState } from "react";
import { api } from "../api/client";
import { RULE, SCOPE, SUBJECT_KIND } from "../api/labels";
import type { BandwidthPackage, DailyTraffic, QuotaScope, QuotaStatus } from "../api/types";
import { DailyBars } from "../components/charts/DailyBars";
import { LineChart } from "../components/charts/LineChart";
import { QuotaMeter, QuotaUnknownNote } from "../components/QuotaMeter";
import { TipRow } from "../components/charts/common";
import { Async, Badge, Card, EmptyState, ErrorState, Fresh, SegToggle, Skeleton } from "../components/ui";
import { useShared } from "../lib/data";
import { gb, gbValue, bytes, num, shortDate, time, stamp } from "../lib/format";
import { Link } from "../lib/router";
import { useResource } from "../lib/useResource";
import { quotaView, quotaState, quotaMissingReasons, cumulativeBytes } from "../lib/usage";

type View = "chart" | "table";
const VIEW: [View, string][] = [["chart", "图表"], ["table", "表格"]];

export function Traffic() {
  const { accounts, quotas, instances } = useShared();
  const [picked, setPicked] = useState<string>();
  const accs = accounts.data;
  const accountId = picked ?? accs?.[0]?.id;

  if (accounts.error) return <div className="card"><ErrorState error={accounts.error} onRetry={accounts.reload} /></div>;
  if (!accs) return <div className="card"><Skeleton lines={6} /></div>;
  if (!accs.length || !accountId)
    return (
      <div className="card">
        <EmptyState title="还没有流量数据" action={<Link className="btn primary" to="/settings">去添加账号</Link>}>添加阿里云账号后，这里会显示每天用了多少流量、月底大概会用到多少。</EmptyState>
      </div>
    );
  const qs = (quotas.data ?? []).filter((q) => q.account_id === accountId);
  const instName = (id: string) => instances.data?.items.find((i) => i.id === id)?.name ?? id;

  return (
    <>
      <div className="filters">
        <label className="sub" htmlFor="traffic-account">账号</label>
        <select id="traffic-account" value={accountId} onChange={(e) => setPicked(e.target.value)}>
          {accs.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
      </div>

      <Card title="本月用量" sub="同时保留官方累计和采集估算；未核实的窗口会单独标注">
        {quotas.error ? <ErrorState error={quotas.error} onRetry={quotas.reload} /> : !quotas.data ? <Skeleton /> : !qs.length ? <EmptyState title="这个账号还没有额度数据" /> : <QuotaTable qs={qs} />}
      </Card>

      <DailySection key={accountId} accountId={accountId} quotas={qs} />
      <Observations key={`observations-${accountId}`} accountId={accountId} />

      <Subjects accountId={accountId} instName={instName} />
      <Packages accountId={accountId} />
    </>
  );
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
          <tr><th>范围</th><th className="num">实时估算</th><th className="num">免费额度</th><th className="num">预测月底</th><th>更新</th></tr>
        </thead>
        <tbody>
          {rows.map((q) => { const v = quotaView(q); return (
            <tr key={q.scope}>
              <td><b>{SCOPE[q.scope]}</b></td>
              <td className="num">
                <b>{v.value === null ? "—" : gb(v.value, 2)}</b> <Badge level={v.unverified || !v.usable ? "warning" : "neutral"}>{quotaState(q)}</Badge>
                <div className="sub">官方 {gb(q.official_bytes)}{q.official_as_of ? `（截至 ${time(q.official_as_of)}）` : "（截止时间未知）"}{v.usable && v.increment !== null && ` + 采集 ${bytes(v.increment)}`}</div>
                {!v.usable && <div className="sub">{quotaMissingReasons(q)}{v.knownIncrement > 0 && `；已采集部分 ${bytes(v.knownIncrement)}`}</div>}
                {v.worstCase !== null && v.worstCase > 0 && <div className="sub">含掉线推算 {bytes(v.worstCase)}</div>}
              </td>
              <td className="num">{gb(q.quota_bytes, 0)}{v.usable && v.value !== null && q.quota_bytes > 0 && <div className="sub">{v.unverified ? "估算" : ""}已用 {num(v.value / q.quota_bytes * 100, 1)}%</div>}</td>
              <td className="num">{q.forecast ? gb(q.forecast.cycle_end_bytes) : "—"}<div className="sub">{!q.forecast ? "完整日样本尚不足" : q.forecast.exceeds_on ? `预计 ${shortDate(q.forecast.exceeds_on)} 超出` : "按当前趋势预测"}</div></td>
              <td><Fresh at={v.updatedAt} kind="realtime" prefix="" />{q.official_status === "unavailable" && <div className="sub">沿用上次官方基准</div>}</td>
            </tr>
          ); })}
        </tbody>
      </table>
    </div>
    <QuotaUnknownNote quotas={rows} />
    </>
  );
}

const addDays = (date: string, n: number) => new Date(Date.parse(date + "T00:00:00Z") + n * 86400_000).toISOString().slice(0, 10);

function DailySection({ accountId, quotas }: { accountId: string; quotas: QuotaStatus[] }) {
  const daily = useResource(() => api.dailyTraffic(accountId), [accountId], { refreshMs: 60_000 });
  const [view, setView] = useState<View>("chart");
  const [scope, setScope] = useState<QuotaScope>("overseas");
  return (
    <div className="grid g2">
      <Card title="每天用了多少" right={<SegToggle label="显示方式" value={view} onChange={setView} options={VIEW} />}>
        <Async res={daily} lines={6}>
          {(d) =>
            !d.days.length ? <EmptyState title="这个计费周期还没有数据" /> : d.days.every((x) => x.overseas_bytes === null && x.mainland_bytes === null) ? (
              <EmptyState title="还拆不出每天的用量">阿里云目前只给了本月累计的总数，没法可靠地分到每一天。总数的变化可以在下面的“官方累计读取记录”里看。</EmptyState>
            ) : view === "chart" ? (
              <>
                <div className="legend" style={{ marginBottom: 8 }}>
                  <span><i style={{ background: "var(--series-1)" }} />{SCOPE.overseas}</span>
                  <span><i style={{ background: "var(--series-2)" }} />{SCOPE.mainland}</span>
                </div>
                <DailyBars
                  ariaLabel="每日流量用量" unit="GB" nameA={SCOPE.overseas} nameB={SCOPE.mainland} totalSlots={cycleDays(d)}
                  bars={d.days.map((x) => ({ label: shortDate(x.date), a: x.overseas_bytes === null ? null : gbValue(x.overseas_bytes), b: x.mainland_bytes === null ? null : gbValue(x.mainland_bytes), provisional: x.provisional }))}
                />
                {d.days.some((x) => x.overseas_bytes === null || x.mainland_bytes === null) && <p className="note">有几天的数据没取到，图上用虚线框留空，不当作 0。</p>}
              </>
            ) : (
              <div className="table-wrap" style={{ maxHeight: 300, overflowY: "auto" }}>
                <table>
                  <thead><tr><th>日期</th><th className="num">{SCOPE.overseas}</th><th className="num">{SCOPE.mainland}</th></tr></thead>
                  <tbody>
                    {[...d.days].reverse().map((x) => (
                      <tr key={x.date}><td>{shortDate(x.date)}{x.provisional && <span className="sub"> · 含实时增量</span>}</td><td className="num">{gb(x.overseas_bytes)}</td><td className="num">{gb(x.mainland_bytes)}</td></tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Async>
      </Card>
      <Card title="累计与预测" sub="虚线按最近 7 天的日均推算" right={<SegToggle label="范围" value={scope} onChange={setScope} options={[["overseas", "海外"], ["mainland", "国内"]]} />}>
        <Async res={daily} lines={6}>
          {(d) => {
            const q = quotas.find((x) => x.scope === scope);
            if (!d.days.length || !q) return <EmptyState title="还没有足够的数据画图" />;
            if (cumulativeBytes(d.days, scope)[0] === null) return <EmptyState title="暂时画不出累计曲线">每天的用量还没取全，累计和月底预测都算不出来。</EmptyState>;
            return <Cumulative d={d} q={q} scope={scope} />;
          }}
        </Async>
      </Card>
    </div>
  );
}

function Observations({ accountId }: { accountId: string }) {
  const [cursor, setCursor] = useState<string>();
  const [view, setView] = useState<View>("table"); // 两条线几乎是平的，表格更有用
  const res = useResource(() => api.trafficObservations(accountId, cursor), [accountId, cursor], { refreshMs: cursor ? 0 : 60_000 });
  return <details className="card fold">
    <summary><h2>官方累计读取记录</h2><span className="sub">每次从阿里云读到的本月累计总数；不是每天的用量</span><div className="spacer" /><span className="sub">展开</span></summary>
    <div className="inline" style={{ justifyContent: "flex-end", margin: "12px 0 8px" }}><SegToggle label="读取记录显示方式" value={view} onChange={setView} options={[["table", "表格"], ["chart", "图表"]]} /></div>
    <Async res={res}>{(page) => {
      const points = [...page.items].reverse();
      return <>
        {!points.length ? <EmptyState title="还没有读取记录" /> : view === "chart" ? <LineChart
          ariaLabel="官方累计值的读取历史" unit="GB" labels={points.map((p) => stamp(p.fetched_at))}
          series={[
            { name: "海外累计", color: "var(--series-1)", data: points.map((p) => p.overseas_bytes === null ? null : gbValue(p.overseas_bytes)) },
            { name: "国内累计", color: "var(--series-2)", data: points.map((p) => p.mainland_bytes === null ? null : gbValue(p.mainland_bytes)) },
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

const cycleDays = (d: DailyTraffic) => Math.max(d.days.length, Math.round((Date.parse(d.cycle_end) - Date.parse(d.cycle_start)) / 86400_000));

function Cumulative({ d, q, scope }: { d: DailyTraffic; q: QuotaStatus; scope: QuotaScope }) {
  const total = cycleDays(d), n = d.days.length;
  const labels = Array.from({ length: total }, (_, i) => shortDate(addDays(d.days[0].date, i)));
  const cum = cumulativeBytes(d.days, scope).map((v) => v === null ? null : gbValue(v));
  const last = cum[n - 1];
  const dailyAvg = q.forecast ? gbValue(q.forecast.daily_average_bytes) : 0;
  const actual = labels.map((_, i) => (i < n ? cum[i] : null));
  const proj = labels.map((_, i) => (q.forecast && last !== null && i >= n - 1 ? last + dailyAvg * (i - n + 1) : null));
  const quota = gbValue(q.quota_bytes);
  const exIdx = q.forecast?.exceeds_on ? labels.indexOf(shortDate(q.forecast.exceeds_on)) : -1;
  const color = scope === "overseas" ? "var(--series-1)" : "var(--series-2)";
  return (
    <>
      {cum.some((v) => v === null) && <p className="note">有几天的数据没取到，从那天起不画累计线，也不做预测。</p>}
      <div className="legend" style={{ marginBottom: 8 }}>
        <span><i className="line" style={{ background: color }} />已经用掉的</span>
        {q.forecast && <span><i className="dash" style={{ borderColor: color }} />预测</span>}
      </div>
      <LineChart
        ariaLabel={`${SCOPE[scope]}累计用量与预测`} labels={labels} unit="GB" height={250}
        series={[{ name: "已用", color, data: actual, endLabel: false }, { name: "预测", color, data: proj, dashed: true, endLabel: false }]}
        refLine={{ value: quota, label: `免费额度 ${num(quota, 0)} GB` }}
        marker={exIdx >= 0 ? { index: exIdx, value: quota, label: `预计 ${labels[exIdx]} 超出` } : undefined}
        extraTip={(i) => { const v = actual[i] ?? proj[i]; return v == null ? null : <TipRow label="占免费额度" value={`${num((v / quota) * 100, 0)}%`} />; }}
      />
    </>
  );
}

function Subjects({ accountId, instName }: { accountId: string; instName: (id: string) => string }) {
  const res = useResource(() => api.billingSubjects(accountId), [accountId], { refreshMs: 30_000 });
  const src = { agent: "", cloud_monitor: "数据来自云监控，晚 1–2 分钟", none: "没有数据" } as const;
  return (
    <Card title="流量是按什么计费的" sub="今天到现在，每个公网出口各算各的">
      <Async res={res} lines={5}>
        {(items) =>
          !items.length ? <EmptyState title="这个账号下没有公网出口" /> : (
            <div className="table-wrap">
              <table>
                <thead><tr><th>公网出口</th><th>怎么算</th><th className="num">收到</th><th className="num">发出</th><th className="num">计费流量</th><th>计入免费额度</th></tr></thead>
                <tbody>
                  {items.map((s) => (
                    <tr key={s.id}>
                      <td><b>{s.name}</b><div className="sub"><span className="mono">{s.public_ips.join("、")}</span>{s.kind.startsWith("cbwp") ? ` · 成员 ${s.instance_ids.map(instName).join("、")}` : ""}</div></td>
                      <td>{SUBJECT_KIND[s.kind]}<div className="sub">{RULE[s.metering_rule]}{s.data_source === "agent" ? "" : ` · ${src[s.data_source]}`}</div></td>
                      <td className="num">{s.today_rx_bytes === null ? <span className="sub">—</span> : gb(s.today_rx_bytes)}</td>
                      <td className="num">{s.today_tx_bytes === null ? <span className="sub">—</span> : gb(s.today_tx_bytes)}</td>
                      <td className="num">{s.today_metered_bytes === null ? <span className="sub">—</span> : <b>{gb(s.today_metered_bytes)}</b>}</td>
                      <td>{s.counts_toward_cdt === null ? <Badge level="warning">待确认</Badge> : s.counts_toward_cdt && s.cdt_scope ? <Badge level="good">{SCOPE[s.cdt_scope]}</Badge> : <span className="sub">不计入</span>}</td>
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

function Packages({ accountId }: { accountId: string }) {
  const res = useResource(api.bandwidthPackages, []);
  const mine = (res.data ?? []).filter((p) => p.account_id === accountId);
  if (!mine.length) return null;
  return <>{mine.map((p) => <PackageHourly key={p.id} p={p} />)}</>;
}

function PackageHourly({ p }: { p: BandwidthPackage }) {
  const res = useResource(() => api.bandwidthPackageHourly(p.id), [p.id], { refreshMs: 60_000 });
  const [view, setView] = useState<View>("chart");
  return (
    <Card title={`共享带宽 ${p.name} · 今天每小时收发`} right={<SegToggle label="显示方式" value={view} onChange={setView} options={VIEW} />}>
      <Async res={res} lines={6}>
        {(h) => {
          if (!h.periods.length) return <EmptyState title="今天还没有数据" />;
          // 当前这个周期还没结束，数值偏小，画在图上会像“突然掉下来”，所以图里不画，表格里照常列出
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
