// 每日用量：两个系列的堆叠柱。较大的系列贴着基线，较小的叠在上面（中间留 2px 缝）。
import { useState } from "react";
import { num } from "../../lib/format";
import { niceAxis, Tip, TipRow, useWidth, YGrid, type TipState } from "./common";

export interface DailyBar {
  label: string; // "11-11"
  a: number | null;
  b: number | null;
  provisional?: boolean;
}

export function DailyBars({ bars, totalSlots, nameA, nameB, unit, ariaLabel, height = 250 }: { bars: DailyBar[]; totalSlots: number; nameA: string; nameB: string; unit: string; ariaLabel: string; height?: number }) {
  const [ref, W] = useWidth<HTMLDivElement>();
  const [tip, setTip] = useState<TipState | null>(null);
  const L = 40, R = 10, T = 12, B = 28;
  const iw = Math.max(0, W - L - R), ih = height - T - B;
  const peak = Math.max(0, ...bars.map((d) => d.a === null || d.b === null ? 0 : d.a + d.b));
  const axis = niceAxis((peak || 1) * 1.05); // 没有任何已知数值时用 0–1 的占位刻度，避免出现一排 0.00
  const y = (v: number) => T + ih - (v / axis.max) * ih;
  const slots = Math.max(totalSlots, bars.length);
  const bw = iw / slots, barW = Math.max(3, Math.min(28, bw * 0.62));
  const every = W < 480 ? 7 : 5;
  const cur = tip ? bars[tip.index] : null;
  return (
    <div className="chart" ref={ref}>
      {W > 0 && (
        <svg width={W} height={height} viewBox={`0 0 ${W} ${height}`} role="img" aria-label={ariaLabel}>
          <YGrid left={L} right={W - R} y={y} axis={axis} />
          {bars.map((d, i) => {
            const x = L + i * bw + (bw - barW) / 2;
            if (d.a === null || d.b === null) return <g key={d.label}><title>{d.label}：数据未完整获取</title><rect x={x} y={T + ih - 8} width={barW} height={8} fill="none" stroke="var(--muted)" strokeDasharray="2 2" /></g>;
            const first = d.a >= d.b ? { c: "var(--series-1)", v: d.a } : { c: "var(--series-2)", v: d.b };
            const second = d.a >= d.b ? { c: "var(--series-2)", v: d.b } : { c: "var(--series-1)", v: d.a };
            const y1 = y(first.v), y2 = y(first.v + second.v), h2 = Math.max(0, y1 - y2 - 2);
            const r = Math.min(3, barW / 2);
            return (
              <g key={d.label} opacity={d.provisional ? 0.8 : 1}>
                <rect x={x} y={y1} width={barW} height={Math.max(0, T + ih - y1)} fill={first.c} />
                {h2 > 0.5 && <path d={`M${x},${y1 - 2} v${-(h2 - r)} q0,${-r} ${r},${-r} h${barW - 2 * r} q${r},0 ${r},${r} v${h2 - r} z`} fill={second.c} />}
                {tip?.index === i && <rect x={L + i * bw} y={T} width={bw} height={ih} fill="rgba(127,127,127,0.1)" />}
              </g>
            );
          })}
          {Array.from({ length: slots }, (_, i) =>
            (i % every === 0 && (i === 0 || i < slots - 1 - every / 2)) || i === slots - 1 ? (
              <text key={i} className="ticklabel" x={L + i * bw + bw / 2} y={height - 8} textAnchor={i === slots - 1 ? "end" : "middle"}>
                {bars[i]?.label ?? bars[0]?.label.slice(0, 3) + String(i + 1).padStart(2, "0")}
              </text>
            ) : null,
          )}
          <rect
            x={L} y={T} width={iw} height={ih} fill="transparent"
            onPointerMove={(e) => {
              const rc = e.currentTarget.getBoundingClientRect();
              const i = Math.floor((e.clientX - rc.left) / bw);
              if (i >= 0 && i < bars.length) setTip({ x: e.clientX - rc.left + L, y: e.clientY - rc.top + T, index: i });
              else setTip(null);
            }}
            onPointerLeave={() => setTip(null)}
          />
        </svg>
      )}
      <Tip tip={tip} width={W}>
        {cur && (
          <>
            <div className="h">
              {cur.label}
              {cur.provisional ? " · 含实时增量" : ""}
            </div>
            <TipRow color="var(--series-1)" label={nameA} value={cur.a === null ? "待确认" : `${num(cur.a)} ${unit}`} />
            <TipRow color="var(--series-2)" label={nameB} value={cur.b === null ? "待确认" : `${num(cur.b)} ${unit}`} />
          </>
        )}
      </Tip>
    </div>
  );
}
