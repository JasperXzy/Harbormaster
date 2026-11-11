// 单位换算与时间显示。接口给整数字节与 UTC 时间，换算都在这里。

// 待后端确认（契约 B1）：1 GB 按 1024^3 还是 10^9。只改这一个常量即可。
export const GB = 1024 ** 3;

let timezone = "Asia/Shanghai";
export const setTimezone = (tz: string) => {
  timezone = tz;
};
export const getTimezone = () => timezone;

const nf = (d: number) => new Intl.NumberFormat("zh-CN", { minimumFractionDigits: d, maximumFractionDigits: d });
export const num = (n: number, d = 1) => nf(d).format(n);

/** 字节 -> "168.4 GB"（固定用 GB，方便和额度对比） */
export const gb = (bytes: number | null, d = 1) => bytes === null ? "待确认" : `${num(bytes / GB, d)} GB`;
export const gbValue = (bytes: number) => bytes / GB;

/** 字节 -> 自动选单位，用于小数值 */
export function bytes(n: number): string {
  if (n >= GB) return gb(n, n >= 100 * GB ? 0 : 1);
  if (n >= 1024 ** 2) return `${num(n / 1024 ** 2, 0)} MB`;
  if (n >= 1024) return `${num(n / 1024, 0)} KB`;
  return `${n} B`;
}

/** 字节每秒 -> 带宽的习惯写法（Mbps，按 1000 进位） */
export function rate(bytesPerSecond: number): string {
  const bps = bytesPerSecond * 8;
  if (bps >= 1e9) return `${num(bps / 1e9, 2)} Gbps`;
  if (bps >= 1e6) return `${num(bps / 1e6, bps >= 1e8 ? 0 : 1)} Mbps`;
  if (bps >= 1e3) return `${num(bps / 1e3, 0)} Kbps`;
  return `${num(bps, 0)} bps`;
}

export const pct = (ratio: number, d = 0) => `${num(ratio * 100, d)}%`;

function parts(iso: string) {
  const f = new Intl.DateTimeFormat("en-CA", { timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  const o: Record<string, string> = {};
  for (const p of f.formatToParts(new Date(iso))) o[p.type] = p.value;
  return o;
}
/** "17:42" */
export const time = (iso: string) => {
  const p = parts(iso);
  return `${p.hour}:${p.minute}`;
};
/** "11-11" */
export const day = (iso: string) => {
  const p = parts(iso);
  return `${p.month}-${p.day}`;
};
/** "2026-11-11" */
export const ymd = (iso: string) => {
  const p = parts(iso);
  return `${p.year}-${p.month}-${p.day}`;
};
/** 今天只显示时间，其余显示 "11-10 22:05" */
export const stamp = (iso: string) => (ymd(iso) === ymd(new Date().toISOString()) ? time(iso) : `${day(iso)} ${time(iso)}`);
/** "YYYY-MM-DD" -> "11-13" */
export const shortDate = (d: string) => d.slice(5);

export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 5) return "刚刚";
  if (s < 60) return `${s} 秒前`;
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
  return `${Math.floor(s / 86400)} 天前`;
}

export function duration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)} 秒`;
  const s = Math.round(seconds);
  if (s < 3600) return s % 60 === 0 ? `${s / 60} 分钟` : `${Math.floor(s / 60)} 分 ${s % 60} 秒`;
  const m = Math.floor((s % 3600) / 60);
  return m === 0 ? `${Math.floor(s / 3600)} 小时` : `${Math.floor(s / 3600)} 小时 ${m} 分`;
}
