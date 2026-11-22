// 轻量路由：支持 pathname 与 ?tab=... 查询参数响应式同步，兼容旧书签平滑跳转。
import { useSyncExternalStore, type AnchorHTMLAttributes, type MouseEvent } from "react";

const subs = new Set<() => void>();
const notify = () => subs.forEach((fn) => fn());
window.addEventListener("popstate", notify);

export function navigate(to: string, replace = false) {
  if (to === location.pathname + location.search) return;
  if (replace) history.replaceState(null, "", to);
  else history.pushState(null, "", to);
  notify();
  if (!replace) window.scrollTo(0, 0);
}

export function usePath(): string {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb);
      return () => subs.delete(cb);
    },
    () => location.pathname,
  );
}

export function useSearch(): string {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb);
      return () => subs.delete(cb);
    },
    () => location.search,
  );
}

export function useQueryTab<T extends string>(defaultTab: T, valid: readonly T[]): [T, (next: T) => void] {
  const search = useSearch();
  const raw = new URLSearchParams(search).get("tab") as T | null;
  const current = raw && valid.includes(raw) ? raw : defaultTab;
  const setTab = (next: T) => {
    const params = new URLSearchParams(location.search);
    if (next === defaultTab) params.delete("tab");
    else params.set("tab", next);
    const qs = params.toString();
    navigate(location.pathname + (qs ? `?${qs}` : ""), true);
  };
  return [current, setTab];
}

export function Link({ to, onClick, ...rest }: { to: string } & AnchorHTMLAttributes<HTMLAnchorElement>) {
  const click = (e: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(e);
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    navigate(to);
  };
  return <a href={to} onClick={click} {...rest} />;
}
