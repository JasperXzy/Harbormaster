import type { QuotaStatus } from "../api/types";
import { SCOPE } from "../api/labels";
import { GB, gb, bytes, pct, shortDate, time, stamp } from "../lib/format";
import { quotaView, quotaState, quotaMissingReasons } from "../lib/usage";
import { Badge, type Level } from "./ui";

export function quotaLevel(q: QuotaStatus): [Level, string] {
  const v = quotaView(q);
  if (!v.usable || v.value === null) return ["warning", quotaState(q)];
  const ratio = q.quota_bytes ? v.value / q.quota_bytes : 0;
  const hit = [...q.thresholds_percent].sort((a, b) => b - a).find((t) => ratio * 100 >= t);
  if (ratio >= 1) return ["critical", `${v.unverified ? "估算" : ""}已超出本地额度`];
  if (hit !== undefined) return [hit >= 95 ? "critical" : "serious", `${v.unverified ? "估算" : ""}已用超过 ${hit}%`];
  if (q.forecast?.exceeds_on) return ["warning", "预计会超额"];
  return v.unverified ? ["warning", "未核实估算"] : ["good", "正常"];
}
export const quotaUnknown = (q: QuotaStatus) => !quotaView(q).usable;

export function QuotaUnknownNote({ quotas }: { quotas: QuotaStatus[] }) {
  const unverified = quotas.filter((q) => quotaView(q).unverified && quotaView(q).value !== null);
  const missing = quotas.filter(quotaUnknown);
  const inferred = quotas.some((q) => q.official_quality === "inferred");
  const lag = unverified[0] ? quotaView(unverified[0]).lagSeconds : 0;
  return <>
    {unverified.length > 0 && <p className="note">{unverified.some((q) => quotaView(q).assumedWindow) ? `未核实估算按最近官方累计，加上从抓取前 ${Math.round(lag / 60)} 分钟开始的采集增量计算。这个窗口是本地假设，可能重复或漏计。` : "估算仍有尚未核实的计费归属或成员信息。"}只用于展示和演练，未开启真实停机保护。{inferred && "官方累计的单位及计费口径仍待核实。"}</p>}
    {missing.length > 0 && <p className="note">{missing.map((q) => `${SCOPE[q.scope]}：${quotaView(q).stale ? "估算已过期，保留上次结果" : quotaMissingReasons(q)}`).join("；")}。未知部分不按零处理。</p>}
  </>;
}
export function QuotaLegend({ worstCase }: { worstCase?: boolean }) {
  return <div className="legend">
    <span><i style={{ background: "var(--series-1)" }} />官方累计</span>
    <span><i style={{ background: "var(--realtime)" }} />采集增量</span>
    {worstCase && <span><i className="worst" />掉线推算</span>}
    <span><i className="proj" />预测</span><span><i className="bar" />本地额度</span>
  </div>;
}
export function QuotaMeter({ q }: { q: QuotaStatus }) {
  const v = quotaView(q);
  if (!v.usable || v.value === null) return <div className="meter">
    <div className="meter-top"><span className="name">{SCOPE[q.scope]}</span><span className="val">{q.official_bytes === null ? "官方数据尚未取得" : `官方 ${gb(q.official_bytes)}`} / {gb(q.quota_bytes, 0)}</span><div className="spacer" /><Badge level="warning">{quotaState(q)}</Badge></div>
    <div className="track unknown-track" role="img" aria-label={`${SCOPE[q.scope]}覆盖不完整，暂不显示完整用量比例`} />
    <div className="meter-foot"><span>{v.stale && v.value !== null ? `上次估算 ${gb(v.value)}` : quotaMissingReasons(q)}</span>{v.knownIncrement > 0 && <span>已采集部分 {bytes(v.knownIncrement)}，不代表完整增量</span>}</div>
  </div>;
  const value = v.value;
  const worst = v.worstCase ?? 0;
  const color = q.scope === "overseas" ? "var(--series-1)" : "var(--series-2)";
  const proj = q.forecast?.cycle_end_bytes ?? value;
  const max = Math.max(q.quota_bytes, proj, value) * 1.04 || 1;
  const w = (n: number) => Math.max(0, Math.min(n, max)) / max * 100;
  const offW = w(q.official_bytes ?? 0), realW = Math.max(0, w(value - worst) - offW), worstW = Math.max(0, w(value) - offW - realW);
  const projW = Math.max(0, w(proj) - w(value));
  return <div className="meter">
    <div className="meter-top"><span className="name">{SCOPE[q.scope]}</span><span className="val">{gb(value)} / {gb(q.quota_bytes, 0)}</span><Badge level={v.unverified ? "warning" : "neutral"}>{quotaState(q)}</Badge><div className="spacer" /><span className="pct">{v.unverified ? "估算 " : ""}{pct(q.quota_bytes ? value / q.quota_bytes : 0)}</span></div>
    <div className="track" role="img" aria-label={`${SCOPE[q.scope]}${quotaState(q)} ${gb(value)}，本地额度 ${gb(q.quota_bytes, 0)}`}>
      <div className="used" style={{ width: `${offW}%`, background: color }} />
      {realW > 0.3 && <div className="rt" style={{ left: `${offW}%`, width: `${realW}%` }} />}
      {worstW > 0.3 && <div className="rt worst" style={{ left: `${offW + realW}%`, width: `${worstW}%` }} />}
      {projW > 0.3 && <div className="proj" style={{ left: `${offW + realW + worstW}%`, width: `${projW}%` }} />}
      <div className="tick" style={{ left: `calc(${q.quota_bytes / max * 100}% - 1px)` }} />
    </div>
    <div className="meter-foot"><span>官方 <b>{gb(q.official_bytes)}</b>{q.official_as_of ? `（截至 ${time(q.official_as_of)}）` : "（截止时间未知）"} + 采集 <b>{v.increment === null ? "—" : bytes(v.increment)}</b></span>
      <span>{q.forecast ? <>月底约 <b>{gb(q.forecast.cycle_end_bytes, q.forecast.cycle_end_bytes >= 100 * GB ? 0 : 1)}</b>{q.forecast.exceeds_on ? `，预计 ${shortDate(q.forecast.exceeds_on)} 超出` : ""}</> : "日样本不足，暂不预测"}</span>
    </div>
    <div className="sub" style={{ marginTop: 5 }}>估算更新于 {v.updatedAt ? stamp(v.updatedAt) : "—"}</div>
    {worst > 0 && <div style={{ marginTop: 6 }}><Badge level="warning">其中 {gb(worst)} 为掉线推算，回补后替换为实际采集数据</Badge></div>}
  </div>;
}
