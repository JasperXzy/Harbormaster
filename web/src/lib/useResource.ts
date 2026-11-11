// 取数钩子：统一提供 加载中 / 出错 / 数据 三种状态，以及重新加载。
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "../api/client";

export interface Resource<T> {
  data: T | undefined;
  error: ApiError | undefined;
  loading: boolean; // 第一次加载，还没有任何数据
  refreshing: boolean; // 已有数据，正在后台重新取
  reload: () => void;
  mutate: (fn: (prev: T) => T) => void;
}

export function useResource<T>(fetcher: () => Promise<T>, deps: unknown[] = [], opts: { refreshMs?: number } = {}): Resource<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(true);
  const seq = useRef(0);
  const fetchRef = useRef(fetcher);
  fetchRef.current = fetcher;

  const load = useCallback(() => {
    const id = ++seq.current;
    setBusy(true);
    fetchRef.current().then(
      (d) => {
        if (id !== seq.current) return;
        setData(d);
        setError(undefined);
        setBusy(false);
      },
      (e) => {
        if (id !== seq.current) return;
        setError(e instanceof ApiError ? e : new ApiError(0, "internal_error", String(e)));
        setBusy(false);
      },
    );
  }, []);

  useEffect(() => {
    setData(undefined);
    setError(undefined);
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  useEffect(() => {
    if (!opts.refreshMs) return;
    const t = setInterval(() => {
      if (!document.hidden) load();
    }, opts.refreshMs);
    return () => clearInterval(t);
  }, [opts.refreshMs, load]);

  const mutate = useCallback((fn: (prev: T) => T) => setData((prev) => (prev === undefined ? prev : fn(prev))), []);

  return { data, error: data === undefined ? error : undefined, loading: busy && data === undefined, refreshing: busy && data !== undefined, reload: load, mutate };
}

/** 每秒重绘一次，用于"x 秒前"与过期判断 */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs]);
  return now;
}
