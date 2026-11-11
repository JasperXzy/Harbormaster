// 图表共用：按容器实际宽度绘制（手机上文字不会被缩小）、坐标轴刻度、悬浮提示。
import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { num } from "../../lib/format";

export function useWidth<E extends HTMLElement>(): [React.RefObject<E | null>, number] {
  const ref = useRef<E>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

export function niceAxis(raw: number, target = 5) {
  const safe = raw > 0 ? raw : 1;
  const rough = safe / target;
  const p = Math.pow(10, Math.floor(Math.log10(rough)));
  const step = [1, 2, 2.5, 5, 10].map((k) => k * p).find((s) => s >= rough) ?? 10 * p;
  const n = Math.ceil(safe / step);
  return { max: step * n, step, n };
}

export function YGrid({ left, right, y, axis }: { left: number; right: number; y: (v: number) => number; axis: { step: number; n: number } }) {
  const digits = axis.step < 1 ? (axis.step < 0.1 ? 2 : 1) : 0;
  return (
    <>
      {Array.from({ length: axis.n + 1 }, (_, k) => {
        const v = axis.step * k;
        const yy = y(v);
        return (
          <g key={k}>
            <line className={k ? "gridline" : "baseline"} x1={left} x2={right} y1={yy} y2={yy} />
            <text className="ticklabel" x={left - 6} y={yy + 4} textAnchor="end">
              {num(v, digits)}
            </text>
          </g>
        );
      })}
    </>
  );
}

export interface TipState {
  x: number;
  y: number;
  index: number;
}

export function Tip({ tip, width, children }: { tip: TipState | null; width: number; children: ReactNode }) {
  if (!tip) return null;
  const tipW = 190;
  const left = tip.x + 14 + tipW > width ? Math.max(0, tip.x - tipW - 10) : tip.x + 14;
  return (
    <div className="tip" style={{ left, top: Math.max(0, tip.y - 10), width: tipW }}>
      {children}
    </div>
  );
}

export function TipRow({ color, label, value }: { color?: string; label: string; value: string }) {
  return (
    <div className="r">
      <span>
        {color && <i style={{ background: color }} />}
        {label}
      </span>
      <b>{value}</b>
    </div>
  );
}
