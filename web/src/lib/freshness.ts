// "数据过期"的判定：超过这些时长没有更新，就在界面上标出来。（契约 B19：时长待后端确认）
export const STALE_AFTER = {
  realtime: 60_000, // 实时估算：正常每 10 秒更新
  official: 2 * 3600_000, // CDT 官方：正常约 1 小时延迟
  resources: 30 * 60_000, // 资源扫描
} as const;

export type FreshKind = keyof typeof STALE_AFTER;

export const isStale = (iso: string | null | undefined, kind: FreshKind, now = Date.now()) =>
  !iso || now - new Date(iso).getTime() > STALE_AFTER[kind];
