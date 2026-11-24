// Modern Cloud Console Primitives: Tinted Badges, Custom Select, Slide-over Drawers, Viewport-Safe InfoTip Popovers, SubTabs, KPI Cards, Modals & Toasts.
import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ApiError } from "../api/client";
import { errorText } from "../api/labels";
import { isStale, type FreshKind } from "../lib/freshness";
import { ago, stamp } from "../lib/format";
import { useNow } from "../lib/useResource";
import { CheckIcon, ChevronDown, CloseIcon, CopyIcon, InfoIcon } from "./icons";

export type Level = "good" | "warning" | "serious" | "critical" | "neutral";

export function LevelIcon({ level }: { level: Level }) {
  switch (level) {
    case "good":
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <circle cx="6" cy="6" r="5" fill="currentColor" opacity="0.22" />
          <circle cx="6" cy="6" r="3.2" fill="currentColor" />
        </svg>
      );
    case "warning":
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <path d="M6 1.2 11.2 10.4H.8z" fill="currentColor" />
        </svg>
      );
    case "serious":
      return (
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <rect x="2" y="2" width="8" height="8" rx="1.8" transform="rotate(45 6 6)" fill="currentColor" />
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
          <circle cx="6" cy="6" r="3.5" fill="currentColor" opacity="0.55" />
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

export function SubTabs<V extends string>({
  value,
  onChange,
  tabs,
  right,
}: {
  value: V;
  onChange: (v: V) => void;
  tabs: { key: V; label: string; badge?: number | string }[];
  right?: ReactNode;
}) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 12, borderBottom: "1px solid var(--border)", flexWrap: "wrap" }}>
      <div className="subtabs" role="tablist" style={{ borderBottom: 0 }}>
        {tabs.map((t) => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={t.key === value}
            className={`subtab ${t.key === value ? "on" : ""}`}
            onClick={() => onChange(t.key)}
          >
            <span>{t.label}</span>
            {t.badge !== undefined && t.badge !== 0 && <span className="subtab-badge">{t.badge}</span>}
          </button>
        ))}
      </div>
      {right && (
        <>
          <div className="spacer" />
          <div style={{ paddingBottom: 6 }}>{right}</div>
        </>
      )}
    </div>
  );
}

export function KpiCard({ label, value, unit, foot, icon }: { label: string; value: ReactNode; unit?: string; foot?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="kpi-card">
      <div className="kpi-top">
        <span>{label}</span>
        {icon && <span className="kpi-icon">{icon}</span>}
      </div>
      <div className="kpi-val">
        {value}
        {unit && <small>{unit}</small>}
      </div>
      {foot && <div className="kpi-foot">{foot}</div>}
    </div>
  );
}

export interface SelectOption<V extends string = string> {
  value: V;
  label: string;
  disabled?: boolean;
}

export function Select<V extends string = string>({
  value,
  onChange,
  options,
  id,
  ariaLabel,
  size = "md",
  disabled = false,
  placeholder = "请选择",
  className = "",
  style,
}: {
  value: V;
  onChange: (value: V) => void;
  options: SelectOption<V>[];
  id?: string;
  ariaLabel?: string;
  size?: "sm" | "md";
  disabled?: boolean;
  placeholder?: string;
  className?: string;
  style?: CSSProperties;
}) {
  const [open, setOpen] = useState(false);
  const [activeIdx, setActiveIdx] = useState(-1);
  const [pos, setPos] = useState<{ top: number; left: number; minWidth: number; maxHeight: number; ready: boolean }>({
    top: 0,
    left: 0,
    minWidth: 140,
    maxHeight: 280,
    ready: false,
  });
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const selected = options.find((o) => o.value === value);

  const updatePos = useCallback(() => {
    const btn = btnRef.current;
    const menu = menuRef.current;
    if (!btn) return;
    const r = btn.getBoundingClientRect();
    const pad = 10;
    const gap = 6;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const minW = Math.max(r.width, size === "sm" ? 136 : 156);
    const mw = menu ? Math.max(menu.offsetWidth, minW) : minW;
    const mh = menu ? menu.scrollHeight : 220;
    const spaceBelow = vh - r.bottom - pad - gap;
    const spaceAbove = r.top - pad - gap;
    const openUp = spaceBelow < Math.min(mh, 220) && spaceAbove > spaceBelow;
    const maxHeight = Math.max(120, Math.min(280, openUp ? spaceAbove : spaceBelow));
    const renderedH = Math.min(mh, maxHeight);
    const top = openUp ? Math.max(pad, r.top - gap - renderedH) : Math.min(vh - pad - renderedH, r.bottom + gap);
    const left = Math.max(pad, Math.min(r.left, vw - pad - mw));
    setPos({ top, left, minWidth: minW, maxHeight, ready: true });
  }, [size]);

  useLayoutEffect(() => {
    if (!open) {
      setPos((p) => (p.ready ? { ...p, ready: false } : p));
      return;
    }
    updatePos();
    const onReflow = () => updatePos();
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (btnRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        btnRef.current?.focus();
      }
    };
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("resize", onReflow);
      window.removeEventListener("scroll", onReflow, true);
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, updatePos]);

  const moveActive = (delta: number) => {
    if (!options.length) return;
    let idx = activeIdx;
    for (let step = 0; step < options.length; step++) {
      idx = (idx + delta + options.length) % options.length;
      if (!options[idx].disabled) {
        setActiveIdx(idx);
        return;
      }
    }
  };

  const toggleOpen = () => {
    if (disabled) return;
    setOpen((was) => {
      if (!was) {
        const cur = options.findIndex((o) => o.value === value && !o.disabled);
        setActiveIdx(cur >= 0 ? cur : options.findIndex((o) => !o.disabled));
      }
      return !was;
    });
  };

  const onButtonKeyDown = (e: React.KeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) {
        const cur = options.findIndex((o) => o.value === value && !o.disabled);
        setActiveIdx(cur >= 0 ? cur : options.findIndex((o) => !o.disabled));
        setOpen(true);
      } else {
        moveActive(e.key === "ArrowDown" ? 1 : -1);
      }
    } else if ((e.key === "Enter" || e.key === " ") && open) {
      e.preventDefault();
      const opt = options[activeIdx];
      if (opt && !opt.disabled) {
        onChange(opt.value);
        setOpen(false);
      }
    }
  };

  return (
    <>
      <button
        ref={btnRef}
        id={id}
        type="button"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        disabled={disabled}
        className={`select-btn ${size === "sm" ? "sm" : ""} ${open ? "open" : ""} ${className}`}
        style={style}
        onClick={(e) => {
          e.stopPropagation();
          toggleOpen();
        }}
        onKeyDown={onButtonKeyDown}
      >
        <span className="select-label">{selected?.label ?? placeholder}</span>
        <ChevronDown size={size === "sm" ? 13 : 14} className="select-caret" />
      </button>
      {open &&
        createPortal(
          <div
            ref={menuRef}
            className="select-menu"
            role="listbox"
            aria-label={ariaLabel}
            style={{
              position: "fixed",
              top: pos.top,
              left: pos.left,
              minWidth: pos.minWidth,
              maxHeight: pos.maxHeight,
              visibility: pos.ready ? "visible" : "hidden",
            }}
            onClick={(e) => e.stopPropagation()}
          >
            {options.map((opt, idx) => {
              const isSel = opt.value === value;
              const isAct = idx === activeIdx;
              return (
                <button
                  key={`${opt.value}-${idx}`}
                  type="button"
                  role="option"
                  aria-selected={isSel}
                  disabled={opt.disabled}
                  className={`select-opt ${isSel ? "selected" : ""} ${isAct ? "active" : ""}`}
                  onMouseEnter={() => !opt.disabled && setActiveIdx(idx)}
                  onClick={() => {
                    if (opt.disabled) return;
                    onChange(opt.value);
                    setOpen(false);
                    btnRef.current?.focus();
                  }}
                >
                  <span className="select-opt-text">{opt.label}</span>
                  {isSel && <CheckIcon size={13} className="select-opt-check" />}
                </button>
              );
            })}
          </div>,
          document.body,
        )}
    </>
  );
}

export function InfoTip({ children, label = "说明" }: { children: ReactNode; label?: string }) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number; ready: boolean }>({ top: 0, left: 0, ready: false });
  const btnRef = useRef<HTMLButtonElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const closeTimer = useRef<number | null>(null);

  const cancelClose = () => {
    if (closeTimer.current !== null) {
      window.clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
  };
  const scheduleClose = () => {
    cancelClose();
    closeTimer.current = window.setTimeout(() => setOpen(false), 120);
  };

  const updatePos = useCallback(() => {
    const btn = btnRef.current;
    const pop = popRef.current;
    if (!btn || !pop) return;
    const r = btn.getBoundingClientRect();
    const pr = pop.getBoundingClientRect();
    const pad = 12;
    const gap = 8;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const aboveTop = r.top - pr.height - gap;
    const belowTop = r.bottom + gap;
    const top = aboveTop >= pad ? aboveTop : Math.min(vh - pr.height - pad, belowTop);
    const idealLeft = r.left + r.width / 2 - pr.width / 2;
    const left = Math.max(pad, Math.min(idealLeft, vw - pr.width - pad));
    setPos({ top: Math.max(pad, top), left, ready: true });
  }, []);

  useLayoutEffect(() => {
    if (!open) {
      setPos((p) => (p.ready ? { ...p, ready: false } : p));
      return;
    }
    updatePos();
    const onReflow = () => updatePos();
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (btnRef.current?.contains(target) || popRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("resize", onReflow);
      window.removeEventListener("scroll", onReflow, true);
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, updatePos]);

  useEffect(() => () => cancelClose(), []);

  return (
    <span
      className="infotip-wrap"
      onMouseEnter={() => {
        cancelClose();
        setOpen(true);
      }}
      onMouseLeave={scheduleClose}
    >
      <button
        ref={btnRef}
        type="button"
        className="infotip-btn"
        aria-label={label}
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation();
          cancelClose();
          setOpen((v) => !v);
        }}
      >
        <InfoIcon size={14} />
      </button>
      {open &&
        createPortal(
          <div
            ref={popRef}
            className="infotip-pop"
            role="tooltip"
            style={{
              position: "fixed",
              top: pos.top,
              left: pos.left,
              visibility: pos.ready ? "visible" : "hidden",
            }}
            onMouseEnter={cancelClose}
            onMouseLeave={scheduleClose}
            onClick={(e) => e.stopPropagation()}
          >
            {children}
          </div>,
          document.body,
        )}
    </span>
  );
}

export function Card({ title, right, children, className = "" }: { title?: ReactNode; sub?: ReactNode; right?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${className}`}>
      {(title || right) && (
        <div className="card-head">
          <div className="card-head-titles">
            {title && <h2>{title}</h2>}
          </div>
          <div className="spacer" />
          {right}
        </div>
      )}
      {children}
    </section>
  );
}

export const Spinner = () => <span className="spin" role="status" aria-label="处理中" />;

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
      <div className="t">读取失败</div>
      <div className="d">{errorText(error.code)}</div>
      {onRetry && (
        <div className="act">
          <button className="btn sm" onClick={onRetry}>重试</button>
        </div>
      )}
    </div>
  );
}

export function Async<T>({ res, children, lines }: { res: { data: T | undefined; error: ApiError | undefined; loading: boolean; reload: () => void }; children: (data: T) => ReactNode; lines?: number }) {
  if (res.data !== undefined) return <>{children(res.data)}</>;
  if (res.error) return <ErrorState error={res.error} onRetry={res.reload} />;
  return <Skeleton lines={lines} />;
}

export function Banner({ level, children, action }: { level: "warning" | "critical" | "neutral"; children: ReactNode; action?: ReactNode }) {
  return (
    <div className={`banner ${level}`} role={level === "critical" ? "alert" : "status"}>
      <Badge level={level}>{level === "critical" ? "异常" : level === "warning" ? "提示" : "说明"}</Badge>
      <div className="body">{children}</div>
      {action}
    </div>
  );
}

export function Fresh({ at, kind, prefix = "更新于", name = "数据", quiet }: { at: string | null | undefined; kind: FreshKind; prefix?: string; name?: string; quiet?: boolean }) {
  const now = useNow(5000);
  if (!at) return quiet ? null : <span className="chip">暂无{name}</span>;
  if (isStale(at, kind, now)) {
    return (
      <span className="chip stale" title={`最后更新于 ${stamp(at)}`}>
        <span className="dot warn" /> {name}延迟 · {stamp(at)}
      </span>
    );
  }
  if (quiet) return null;
  return (
    <span className="chip" title={ago(at, now)}>
      {prefix} {stamp(at)}
    </span>
  );
}

/* ---------- Slide-over Drawer ---------- */
export function Drawer({
  title,
  sub,
  right,
  footer,
  wide,
  children,
  onClose,
}: {
  title: ReactNode;
  sub?: ReactNode;
  right?: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  children: ReactNode;
  onClose: () => void;
}) {
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
    <div className="drawer-bg" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`drawer ${wide ? "wide" : ""}`} role="dialog" aria-modal="true" tabIndex={-1} ref={ref}>
        <div className="drawer-head">
          <div style={{ minWidth: 0, flex: 1 }}>
            <h2>{title}</h2>
            {sub && <div className="sub" style={{ marginTop: 3 }}>{sub}</div>}
          </div>
          <div className="inline" style={{ flexWrap: "nowrap" }}>
            {right}
            <button type="button" className="btn icon" onClick={onClose} aria-label="关闭抽屉">
              <CloseIcon size={16} />
            </button>
          </div>
        </div>
        <div className="drawer-body">{children}</div>
        {footer && <div className="drawer-foot">{footer}</div>}
      </div>
    </div>
  );
}

/* ---------- Centered Modal ---------- */
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
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 12 }}>
          <h2 style={{ margin: 0 }}>{title}</h2>
          <button type="button" className="btn icon" style={{ width: 28, height: 28 }} onClick={onClose} aria-label="关闭">
            <CloseIcon size={15} />
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

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
        <button className="btn" onClick={onClose}>取消</button>
        <button className={`btn ${danger ? "danger" : "primary"}`} disabled={busy || (!!typeToConfirm && typed !== typeToConfirm)} onClick={ok}>
          {busy && <Spinner />} {okText}
        </button>
      </div>
    </Modal>
  );
}

/* ---------- Toast ---------- */
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

export const errMsg = (e: unknown) => errorText(e instanceof ApiError ? e.code : e instanceof Error ? e.message : "internal_error");

export function CopyButton({ text, label = "复制", className = "btn sm", iconOnly = false }: { text: string; label?: string; className?: string; iconOnly?: boolean }) {
  const [done, setDone] = useState(false);
  const copy = async (e: React.MouseEvent) => {
    e.stopPropagation();
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
  if (iconOnly) {
    return (
      <button type="button" className="infotip-btn" title={done ? "已复制" : label} aria-label={label} onClick={copy}>
        {done ? <CheckIcon size={13} /> : <CopyIcon size={13} />}
      </button>
    );
  }
  return (
    <button type="button" className={className} onClick={copy}>
      {done ? <><CheckIcon size={13} /> 已复制</> : label}
    </button>
  );
}
