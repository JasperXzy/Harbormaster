import type { QuotaStatus } from "../api/types";
import { SCOPE } from "../api/labels";
import { GB, gb, bytes, pct, shortDate, time, stamp } from "../lib/format";
import { quotaView, quotaState, quotaMissingReasons } from "../lib/usage";
import { Badge, InfoTip, type Level } from "./ui";

export function quotaLevel(q: QuotaStatus): [Level, string] {
  const v = quotaView(q);
  if (!v.usable || v.value === null) return ["neutral", quotaState(q)];
  const ratio = q.quota_bytes ? v.value / q.quota_bytes : 0;
  const hit = [...q.thresholds_percent].sort((a, b) => b - a).find((t) => ratio * 100 >= t);
  if (ratio >= 1) return ["critical", "已超额"];
  if (hit !== undefined) return [hit >= 95 ? "critical" : "serious", `已用 >${hit}%`];
  if (q.forecast?.exceeds_on) return ["warning", "预计超额"];
  return ["good", "正常"];
}

export const quotaUnknown = (q: QuotaStatus) => !quotaView(q).usable;

export function QuotaUnknownNote({ quotas }: { quotas: QuotaStatus[] }) {
  const missing = quotas.filter(quotaUnknown);
  if (!missing.length) return null;
  return (
    <div className="sub" style={{ marginTop: 10, display: "flex", alignItems: "center", gap: 6 }}>
      <span>部分范围仅展示官方累计（待完整采集覆盖）</span>
      <InfoTip label="覆盖说明">
        {missing.map((q) => (
          <div key={q.scope}>
            <b>{SCOPE[q.scope]}：</b>
            {quotaView(q).stale ? "实时样本已过期，暂保留上次数值" : quotaMissingReasons(q)}（未知部分不按 0 填充）。
          </div>
        ))}
      </InfoTip>
    </div>
  );
}

export function QuotaLegend({ worstCase }: { worstCase?: boolean }) {
  return (
    <div className="legend">
      <span><i style={{ background: "var(--series-1)" }} />官方累计</span>
      <span><i style={{ background: "var(--realtime)" }} />实时增量</span>
      {worstCase && <span><i className="worst" />掉线保守推算</span>}
      <span><i className="proj" />月底预测</span>
      <span><i className="bar" />免费额度线</span>
    </div>
  );
}

export function QuotaMeter({ q }: { q: QuotaStatus }) {
  const v = quotaView(q);
  if (!v.usable || v.value === null) {
    return (
      <div className="meter">
        <div className="meter-top">
          <span className="name">{SCOPE[q.scope]}</span>
          <span className="val">
            {q.official_bytes === null ? "等待首次同步" : `${gb(q.official_bytes)}`} / {gb(q.quota_bytes, 0)}
          </span>
          <InfoTip label="数据来源说明">
            <div>当前展示阿里云官方累计值（每 5 分钟同步，账单存在约 1 小时延迟）。</div>
            <div style={{ marginTop: 4 }}>未启用实时合成原因：{quotaMissingReasons(q)}</div>
            {v.knownIncrement > 0 && <div style={{ marginTop: 4 }}>已收到部分 Agent 增量：{bytes(v.knownIncrement)}</div>}
          </InfoTip>
          <div className="spacer" />
          <Badge level="neutral">官方累计</Badge>
        </div>
        <div className="track unknown-track" role="img" aria-label={`${SCOPE[q.scope]}仅官方累计`}>
          {q.official_bytes !== null && q.quota_bytes > 0 && (
            <div
              className="used"
              style={{
                width: `${Math.min(100, (q.official_bytes / q.quota_bytes) * 100)}%`,
                background: q.scope === "overseas" ? "var(--series-1)" : "var(--series-2)",
              }}
            />
          )}
        </div>
      </div>
    );
  }

  const value = v.value;
  const worst = v.worstCase ?? 0;
  const color = q.scope === "overseas" ? "var(--series-1)" : "var(--series-2)";
  const proj = q.forecast?.cycle_end_bytes ?? value;
  const max = Math.max(q.quota_bytes, proj, value) * 1.04 || 1;
  const w = (n: number) => (Math.max(0, Math.min(n, max)) / max) * 100;
  const offW = w(q.official_bytes ?? 0);
  const realW = Math.max(0, w(value - worst) - offW);
  const worstW = Math.max(0, w(value) - offW - realW);
  const projW = Math.max(0, w(proj) - w(value));
  const ratio = q.quota_bytes ? value / q.quota_bytes : 0;

  return (
    <div className="meter">
      <div className="meter-top">
        <span className="name">{SCOPE[q.scope]}</span>
        <span className="val">
          <b>{gb(value)}</b> / {gb(q.quota_bytes, 0)}
        </span>
        <InfoTip label="计算口径详情">
          <div>
            <b>官方累计：</b>{gb(q.official_bytes)}
            {q.official_as_of ? `（截至 ${time(q.official_as_of)}）` : "（官方未返回精确截止秒数）"}
          </div>
          <div><b>实时采集增量：</b>{v.increment === null ? "—" : bytes(v.increment)}</div>
          {v.unverified && (
            <div style={{ marginTop: 4 }}>
              注：由于阿里云 CDT 接口不返回精确账单截止时刻，本地按最近 {Math.round(v.lagSeconds / 60)} 分钟采集窗口叠加估算。
            </div>
          )}
          <div style={{ marginTop: 4 }}>最后更新：{v.updatedAt ? stamp(v.updatedAt) : "—"}</div>
        </InfoTip>
        <div className="spacer" />
        <span className="pct" style={ratio >= 0.9 ? { color: "var(--critical)" } : ratio >= 0.8 ? { color: "var(--warning)" } : undefined}>
          {pct(ratio)}
        </span>
      </div>

      <div className="track" role="img" aria-label={`${SCOPE[q.scope]} ${gb(value)} / ${gb(q.quota_bytes, 0)}`}>
        <div className="used" style={{ width: `${offW}%`, background: color }} />
        {realW > 0.3 && <div className="rt" style={{ left: `${offW}%`, width: `${realW}%` }} />}
        {worstW > 0.3 && <div className="rt worst" style={{ left: `${offW + realW}%`, width: `${worstW}%` }} />}
        {projW > 0.3 && <div className="proj" style={{ left: `${offW + realW + worstW}%`, width: `${projW}%` }} />}
        <div className="tick" style={{ left: `calc(${(q.quota_bytes / max) * 100}% - 1px)` }} />
      </div>

      <div className="meter-foot">
        <span>
          官方 <b>{gb(q.official_bytes)}</b> + 实时 <b>{v.increment === null ? "—" : bytes(v.increment)}</b>
        </span>
        {q.forecast && (
          <span>
            月底预计 <b>{gb(q.forecast.cycle_end_bytes, q.forecast.cycle_end_bytes >= 100 * GB ? 0 : 1)}</b>
            {q.forecast.exceeds_on && (
              <span style={{ color: "var(--warning)", fontWeight: 600 }}> · 预计 {shortDate(q.forecast.exceeds_on)} 触顶</span>
            )}
          </span>
        )}
        {q.overage_cost && Number(q.overage_cost.estimated_cost ?? 0) > 0 && (
          <span style={{ color: "var(--critical)" }}>
            已超额约 <b>{q.overage_cost.currency === "USD" ? "$" : "¥"}{q.overage_cost.estimated_cost}</b>
          </span>
        )}
        {worst > 0 && <Badge level="warning">含掉线推算 {gb(worst)}</Badge>}
      </div>
    </div>
  );
}
