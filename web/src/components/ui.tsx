// 基础组件：状态徽标、开关、分段切换、弹窗、提示、各种状态占位。
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../api/client";
import { errorText } from "../api/labels";
import { isStale, type FreshKind } from "../lib/freshness";
import { ago, stamp } from "../lib/format";
import { useNow } from "../lib/useResource";

export type Level = "good" | "warning" | "serious" | "critical" | "neutral";

/* 状态图标：形状与颜色同时区分，色弱也能看懂 */
export function LevelIcon({ level }: { level: Level }) {
  switch (level) {
    case "good":
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <circle cx="6" cy="6" r="5" fill="currentColor" />
          <path d="M3.6 6.2 5.2 7.7 8.4 4.5" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "warning":
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <path d="M6 1 11.2 10.5H.8z" fill="currentColor" />
          <path d="M6 4.5v2.5M6 8.6v.01" stroke="#0b0b0b" strokeWidth="1.3" strokeLinecap="round" />
        </svg>
      );
    case "serious":
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <rect x="1.5" y="1.5" width="9" height="9" rx="2" transform="rotate(45 6 6)" fill="currentColor" />
          <path d="M6 3.8v2.6M6 8.2v.01" stroke="#fff" strokeWidth="1.3" strokeLinecap="round" />
        </svg>
      );
    case "critical":
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <circle cx="6" cy="6" r="5" fill="currentColor" />
          <path d="M4.2 4.2 7.8 7.8M7.8 4.2 4.2 7.8" stroke="#fff" strokeWidth="1.4" strokeLinecap="round" />
        </svg>
      );
    default:
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <circle cx="6" cy="6" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
        </svg>
      );
  }
}

export function Badge({ level, children, title }: { level: Level; children: ReactNode; title?: string }) {
  return (
    <span className={`status ${level}`} title={title}>
      <LevelIcon level={level} />
      {children}
    </span>
  );
}

export function Switch({ on, onChange, label, disabled }: { on: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return <button type="button" className={`switch ${on ? "on" : ""}`} role="switch" aria-checked={on} aria-label={label} disabled={disabled} onClick={() => onChange(!on)} />;
}

export function SegToggle<V extends string>({ value, onChange, options, label }: { value: V; onChange: (v: V) => void; options: [V, string][]; label: string }) {
  return (
    <div className="seg-toggle" role="group" aria-label={label}>
      {options.map(([v, text]) => (
        <button key={v} type="button" className={v === value ? "on" : ""} aria-pressed={v === value} onClick={() => onChange(v)}>
          {text}
        </button>
      ))}
    </div>
  );
}

export function Card({ title, sub, right, children, className = "" }: { title?: ReactNode; sub?: ReactNode; right?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${className}`}>
      {(title || right) && (
        <div className="card-head">
          {title && <h2>{title}</h2>}
          {sub && <span className="sub">{sub}</span>}
          <div className="spacer" />
          {right}
        </div>
      )}
      {children}
    </section>
  );
}

export const Spinner = () => <span className="spin" role="status" aria-label="处理中" />;

/* ---------- 状态占位 ---------- */
export function Skeleton({ lines = 3, height = 14 }: { lines?: number; height?: number }) {
  return (
    <div aria-busy="true" aria-label="正在加载" style={{ display: "grid", gap: 10 }}>
      {Array.from({ length: lines }, (_, i) => (
        <span key={i} className="skel" style={{ height, width: `${[92, 78, 60, 85][i % 4]}%` }} />
      ))}
    </div>
  );
}

export function EmptyState({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="state">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M3 13h5l1.5 3h5L16 13h5" />
        <path d="M5.5 5h13L21 13v5a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-5z" />
      </svg>
      <div className="t">{title}</div>
      {children && <div className="d">{children}</div>}
      {action && <div className="act">{action}</div>}
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: ApiError; onRetry?: () => void }) {
  return (
    <div className="state error" role="alert">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <circle cx="12" cy="12" r="9" />
        <path d="M12 7.5v5M12 16v.01" />
      </svg>
      <div className="t">没能取到数据</div>
      <div className="d">{errorText(error.code)}</div>
      {onRetry && (
        <div className="act">
          <button className="btn" onClick={onRetry}>
            再试一次
          </button>
        </div>
      )}
    </div>
  );
}

/** 一块内容的三种状态：加载中 / 出错 / 有数据（空数据由调用方在 children 里判断） */
export function Async<T>({ res, children, lines }: { res: { data: T | undefined; error: ApiError | undefined; loading: boolean; reload: () => void }; children: (data: T) => ReactNode; lines?: number }) {
  if (res.data !== undefined) return <>{children(res.data)}</>;
  if (res.error) return <ErrorState error={res.error} onRetry={res.reload} />;
  return <Skeleton lines={lines} />;
}

export function Banner({ level, children, action }: { level: "warning" | "critical" | "neutral"; children: ReactNode; action?: ReactNode }) {
  return (
    <div className={`banner ${level}`} role={level === "critical" ? "alert" : "status"}>
      <Badge level={level}>{""}</Badge>
      <div className="body">{children}</div>
      {action}
    </div>
  );
}

/** "截至 17:42"；超过时限没更新则变成醒目的"数据已过期" */
export function Fresh({ at, kind, prefix = "截至", name = "数据" }: { at: string | null | undefined; kind: FreshKind; prefix?: string; name?: string }) {
  const now = useNow(5000);
  if (!at) return <span className="chip stale">还没有{name}</span>;
  if (isStale(at, kind, now))
    return (
      <span className="chip stale" title={`最后更新于 ${stamp(at)}`}>
        <LevelIconInline /> {name}已过期 · 停在 {stamp(at)}
      </span>
    );
  return (
    <span className="chip" title={ago(at, now)}>
      {prefix} {stamp(at)}
    </span>
  );
}
const LevelIconInline = () => (
  <span style={{ width: 12, height: 12, display: "inline-flex", color: "var(--warning)" }}>
    <LevelIcon level="warning" />
  </span>
);

/* ---------- 弹窗 ---------- */
export function Modal({ title, children, onClose, wide }: { title: string; children: ReactNode; onClose: () => void; wide?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.focus();
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", key);
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", key);
      document.body.style.overflow = "";
      prev?.focus?.();
    };
  }, [onClose]);
  return (
    <div className="modal-bg" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? "wide" : ""}`} role="dialog" aria-modal="true" aria-label={title} tabIndex={-1} ref={ref}>
        <h2>{title}</h2>
        {children}
      </div>
    </div>
  );
}

/** 确认弹窗。传 typeToConfirm 时必须先输入指定文字，用于删除等不可恢复的操作。 */
export function Confirm({ title, body, okText, danger, typeToConfirm, onOk, onClose }: { title: string; body: ReactNode; okText: string; danger?: boolean; typeToConfirm?: string; onOk: () => Promise<void> | void; onClose: () => void }) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const ok = async () => {
    setBusy(true);
    try {
      await onOk();
      onClose();
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={title} onClose={onClose}>
      <div style={{ color: "var(--text-2)", fontSize: "var(--fs-md)" }}>{body}</div>
      {typeToConfirm && (
        <div style={{ marginTop: 12 }}>
          <label className="sub" htmlFor="confirm-typed">
            请输入 <b style={{ color: "var(--text)" }}>{typeToConfirm}</b> 确认
          </label>
          <input id="confirm-typed" type="text" value={typed} onChange={(e) => setTyped(e.target.value)} style={{ width: "100%", marginTop: 4 }} autoComplete="off" />
        </div>
      )}
      <div className="actions">
        <button className="btn" onClick={onClose}>
          取消
        </button>
        <button className={`btn ${danger ? "danger" : "primary"}`} disabled={busy || (!!typeToConfirm && typed !== typeToConfirm)} onClick={ok}>
          {busy && <Spinner />} {okText}
        </button>
      </div>
    </Modal>
  );
}

/* ---------- 轻提示 ---------- */
interface ToastItem {
  id: number;
  text: string;
  error: boolean;
}
const ToastCtx = createContext<(text: string, error?: boolean) => void>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const seq = useRef(0);
  const push = useCallback((text: string, error = false) => {
    const id = ++seq.current;
    setItems((x) => [...x.slice(-2), { id, text, error }]);
    setTimeout(() => setItems((x) => x.filter((t) => t.id !== id)), error ? 6000 : 3200);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.error ? "error" : ""}`}>
            {t.text}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

/** 把接口错误变成提示文字 */
export const errMsg = (e: unknown) => errorText(e instanceof ApiError ? e.code : e instanceof Error ? e.message : "internal_error");

export function CopyButton({ text, label = "复制", className = "btn sm" }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
    }
    setDone(true);
    setTimeout(() => setDone(false), 1800);
  };
  return (
    <button type="button" className={className} onClick={copy}>
      {done ? "已复制 ✓" : label}
    </button>
  );
}
