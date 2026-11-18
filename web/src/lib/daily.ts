import type { DailyTraffic, QuotaScope, QuotaStatus } from "../api/types";
import { cumulativeBytes, quotaView } from "./usage.ts";

// Presentation only: byte aggregation and all forecast calculations live in Go.
export function dailyValue(day: DailyTraffic["days"][number], scope: QuotaScope) {
  const detail = day.scopes?.[scope];
  const source = detail?.source === "official" ? "官方日数据" : detail?.source === "mixed" ? "混合来源" : "采集估算";
  if (detail) return {
    value: detail.bytes ?? (detail.has_data ? detail.known_bytes : null),
    partial: detail.bytes === null, completeDay: detail.is_complete_day,
    source: detail.source, reasons: detail.reasons, from: detail.observed_from, until: detail.observed_until,
    label: detail.status === "pending" ? "正在汇总" : detail.has_data ? `${source} · ${detail.bytes===null?"部分覆盖":detail.is_complete_day?"完整日":"今天未结束"}` : "无覆盖数据",
  };
  const value = day[scope === "overseas" ? "overseas_bytes" : "mainland_bytes"];
  return { value, partial: value === null, completeDay: false, source: "unavailable", reasons: [] as string[], from: null, until: null, label: value === null ? "无覆盖数据" : "来源待核实" };
}

export function forecastNote(q: QuotaStatus) {
  const f=q.forecast;
  const a=q.aggregation;
  if(a) {
    if(!f) return `当前 ${a.forecast_accounts}/${a.account_count} 个账号有可用预测，完整合计尚不可用。各账号需积累完整日样本，今天不参加日均。`;
    return `将 ${a.account_count} 个账号的独立预测相加。${a.forecast_sample_days_min>0?`各账号使用 ${a.forecast_sample_days_min===a.forecast_sample_days_max?a.forecast_sample_days_min:`${a.forecast_sample_days_min}–${a.forecast_sample_days_max}`} 个完整日样本。`:""}各自基准时间可能不同，合计仅供展示；免费额度不能跨账号使用。`;
  }
  if (!f) return q.forecast_status === "baseline_missing" ? "已有完整日样本，正在等待可用的累计基准。" : "近 7 个已结束日期中还没有覆盖完整的样本。至少有 1 个完整日后显示预测，今天不参加日均。";
  return `按近 7 天中的 ${f.sample_days ?? "—"} 个完整日预测，日均 ${(f.daily_average_bytes / 1024 ** 3).toFixed(2)} GB。${f.unverified ? "这是未核实的展示估算。" : ""}${f.baseline_at_assumed ? "官方累计的统计时间采用本地窗口假设。" : ""}`;
}

export function usageTimeline(d: DailyTraffic,q: QuotaStatus,scope: QuotaScope,now=Date.now()) {
  const first=d.days[0]?.date;
  if (!first) return {labels:[] as string[],dates:[] as string[],official:[] as (number|null)[],collected:[] as (number|null)[],realtime:[] as (number|null)[],projected:[] as (number|null)[]};
  const count=Math.round((Date.parse(d.cycle_end)-Date.parse(d.cycle_start))/86400_000);
  const dates=Array.from({length:count},(_,i)=>new Date(Date.parse(first+"T00:00:00Z")+i*86400_000).toISOString().slice(0,10));
  const forecast=q.forecast?.points ?? [],anchor=forecast[0]?.date;
  const slots=dates.flatMap(date=>date===anchor?[{date,anchor:true},{date,anchor:false}]:[{date,anchor:false}]);
  const official=new Map((d.official_points?.[scope]??[]).map(p=>[p.date,p.bytes]));
  const sums=cumulativeBytes(d.days,scope);
  const cumulative=new Map(d.days.map((day,i)=>[day.date,sums[i]]));
  const projection=new Map(forecast.slice(1).map(p=>[p.date,p.bytes]));
  const view=quotaView(q,now),today=d.days[d.days.length-1].date;
  return {
    labels:slots.map(s=>s.date.slice(5)+(s.anchor?" 基准":"")),dates:slots.map(s=>s.date),
    official:slots.map(s=>s.date===anchor&&!s.anchor?null:official.get(s.date)??null),
    collected:slots.map(s=>s.anchor?null:cumulative.get(s.date)??null),
    realtime:slots.map(s=>s.date===today&&(s.date!==anchor||s.anchor)&&view.usable?view.value:null),
    projected:slots.map(s=>s.anchor?forecast[0]?.bytes??null:projection.get(s.date)??null),
  };
}
