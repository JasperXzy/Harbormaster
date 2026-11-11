// 额度条：官方已用 + 实时增量（未出账）+ 预测到月底，竖线标出免费额度。
import type { QuotaStatus } from "../api/types";
import { SCOPE } from "../api/labels";
import { gb, pct, shortDate, time, stamp } from "../lib/format";
import { Badge, type Level } from "./ui";

export function quotaLevel(q: QuotaStatus): [Level, string] {
  if (q.effective_bytes === null) return ["warning", "用量待确认"];
  const ratio = q.quota_bytes ? q.effective_bytes / q.quota_bytes : 0;
  const hit = [...q.thresholds_percent].sort((a, b) => b - a).find((t) => ratio * 100 >= t);
  if (ratio >= 1) return ["critical", "已超出免费额度"];
  if (hit !== undefined) return [hit >= 95 ? "critical" : "serious", `已用超过 ${hit}%`];
  if (q.forecast?.exceeds_on) return ["warning", "预计会超额"];
  return ["good", "正常"];
}

/** 这条额度的用量还不完整，不能显示比例、余量和预测 */
export const quotaUnknown = (q: QuotaStatus) =>
  q.official_bytes === null || q.estimated_bytes === null || q.effective_bytes === null || q.realtime_increment_bytes === null || q.worst_case_bytes === null;

/** 未知状态的解释。一张卡片里只放一次，不在每条额度条下重复。 */
export function QuotaUnknownNote({ quotas }: { quotas: QuotaStatus[] }) {
  const unknown = quotas.filter(quotaUnknown);
  if (!unknown.length) return null;
  const cutoff = unknown.some((q) => q.official_status === "cutoff_unknown");
  const inferred = unknown.some((q) => q.official_quality === "inferred");
  const fetched = unknown.map((q) => q.fetched_at).filter((x): x is string => !!x).sort().at(-1);
  return (
    <p className="note">
      {cutoff ? "阿里云给了本月的累计用量，但没有说明统计到几点，所以暂时没法把之后的实时流量加上去。" : "这部分数据还不完整。"}
      弄清之前，这里不显示已用比例、剩余额度和月底预测。
      {inferred && "数字的单位也还在核实。"}
      {fetched && `最近一次读取：${stamp(fetched)}。`}
    </p>
  );
}

export function QuotaLegend({ worstCase }: { worstCase?: boolean }) {
  return (
    <div className="legend">
      <span>
        <i style={{ background: "var(--series-1)" }} />
        官方已用
      </span>
      <span>
        <i style={{ background: "var(--realtime)" }} />
        实时增量（官方还没算进去）
      </span>
      {worstCase && (
        <span>
          <i className="worst" />
          掉线期间的推算
        </span>
      )}
      <span>
        <i className="proj" />
        预测到月底
      </span>
      <span>
        <i className="bar" />
        免费额度
      </span>
    </div>
  );
}

export function QuotaMeter({ q }: { q: QuotaStatus }) {
  if (quotaUnknown(q)) {
    return <div className="meter">
      <div className="meter-top">
        <span className="name">{SCOPE[q.scope]}</span>
        <span className="val">{q.official_bytes === null ? "还没有数据" : `官方 ${gb(q.official_bytes)}`} / {gb(q.quota_bytes, 0)}</span>
        <div className="spacer" />
        <span className="sub">实时用量待确认</span>
      </div>
      <div className="track unknown-track" role="img" aria-label="数据不完整，暂不显示使用比例" />
      {q.official_status === "unavailable" && <div style={{ marginTop: 6 }}><Badge level="warning">官方数据暂时取不到，显示的是上一次的数值</Badge></div>}
    </div>;
  }
  // 上面已排除未知状态；这里收窄类型
  if (q.official_bytes === null || q.estimated_bytes === null || q.effective_bytes === null || q.realtime_increment_bytes === null || q.worst_case_bytes === null) return null;
  const color = q.scope === "overseas" ? "var(--series-1)" : "var(--series-2)";
  const proj = q.forecast?.cycle_end_bytes ?? q.estimated_bytes;
  const max = Math.max(q.quota_bytes, proj, q.estimated_bytes) * 1.04 || 1;
  const w = (v: number) => (Math.max(0, Math.min(v, max)) / max) * 100;
  const offW = w(q.official_bytes);
  const realW = w(q.estimated_bytes - q.worst_case_bytes) - offW;
  const worstW = w(q.estimated_bytes) - offW - realW;
  const projW = Math.max(0, w(proj) - w(q.estimated_bytes));
  const tick = (q.quota_bytes / max) * 100;
  const ratio = q.quota_bytes ? q.effective_bytes / q.quota_bytes : 0;
  const name = SCOPE[q.scope];
  const label = `${name}：官方 ${gb(q.official_bytes)}，实时估算 ${gb(q.estimated_bytes)}，免费额度 ${gb(q.quota_bytes, 0)}${q.forecast ? `，预测月底 ${gb(q.forecast.cycle_end_bytes)}` : ""}`;
  return (
    <div className="meter">
      <div className="meter-top">
        <span className="name">{name}</span>
        <span className="val">
          {gb(q.effective_bytes)} / {gb(q.quota_bytes, 0)}
        </span>
        <div className="spacer" />
        <span className="pct">{pct(ratio)}</span>
      </div>
      <div className="track" role="img" aria-label={label}>
        <div className="used" style={{ width: `${offW}%`, background: color }} />
        {realW > 0.3 && <div className="rt" style={{ left: `calc(${offW}% + 2px)`, width: `calc(${realW}% - 2px)` }} />}
        {worstW > 0.3 && <div className="rt worst" style={{ left: `calc(${offW + realW}% + 2px)`, width: `calc(${worstW}% - 2px)` }} />}
        {projW > 0.3 && <div className="proj" style={{ left: `calc(${offW + realW + worstW}% + 2px)`, width: `calc(${projW}% - 2px)` }} />}
        <div className="tick" style={{ left: `calc(${tick}% - 1px)` }} />
      </div>
      <div className="meter-foot">
        <span>
          官方 <b>{gb(q.official_bytes)}</b>
          {q.official_as_of ? `（截至 ${time(q.official_as_of)}）` : "（还没有数据）"}
        </span>
        <span>
          实时增量 <b>+{gb(q.realtime_increment_bytes)}</b>
        </span>
        {q.forecast ? (
          <>
            <span>
              预测月底 <b>{gb(q.forecast.cycle_end_bytes)}</b>
            </span>
            <span>{q.forecast.exceeds_on ? <>预计 <b>{shortDate(q.forecast.exceeds_on)}</b> 超出免费额度</> : <>预计月底<b>不会超额</b></>}</span>
          </>
        ) : (
          <span>数据还不够，暂时无法预测</span>
        )}
      </div>
      {q.official_status === "unavailable" && (
        <div style={{ marginTop: 6 }}>
          <Badge level="warning">官方数据暂时取不到，正沿用上一次的数值</Badge>
        </div>
      )}
      {q.worst_case_bytes > 0 && (
        <div style={{ marginTop: 6 }}>
          <Badge level="critical">其中 {gb(q.worst_case_bytes)} 是采集程序掉线期间按最坏情况推算的</Badge>
        </div>
      )}
    </div>
  );
}
