// 通用折线图：多条线、十字准线、线尾标签、参考线（如免费额度）、标记点（如预计超额日）。
import { useState, type ReactNode } from "react";
import { num } from "../../lib/format";
import { niceAxis, Tip, TipRow, useWidth, YGrid, type TipState } from "./common";

export interface Series {
  name: string;
  color: string;
  data: (number | null)[];
  dashed?: boolean;
  endLabel?: boolean;
}

interface Props {
  labels: string[];
  series: Series[];
  unit: string;
  digits?: number;
  height?: number;
  ariaLabel: string;
  refLine?: { value: number; label: string };
  marker?: { index: number; value: number; label: string };
  extraTip?: (index: number) => ReactNode;
  tickEvery?: number;
}

export function LineChart({ labels, series, unit, digits = 1, height = 230, ariaLabel, refLine, marker, extraTip, tickEvery }: Props) {
  const [ref, W] = useWidth<HTMLDivElement>();
  const [tip, setTip] = useState<TipState | null>(null);
  const hasEnd = series.some((s) => s.endLabel !== false);
  const L = 40, R = hasEnd ? 58 : 14, T = 14, B = 26;
  const iw = Math.max(0, W - L - R), ih = height - T - B, n = labels.length;
  const vals = series.flatMap((s) => s.data).filter((v): v is number => v != null);
  const axis = niceAxis(Math.max(0.001, ...vals, refLine?.value ?? 0) * 1.08, 4);
  const x = (i: number) => L + (n > 1 ? (i / (n - 1)) * iw : iw / 2);
  const y = (v: number) => T + ih - (v / axis.max) * ih;
  const every = tickEvery ?? Math.max(1, Math.ceil(n / (W < 480 ? 4 : 6)));

  const ends = series
    .filter((s) => s.endLabel !== false)
    .map((s) => {
      let li = -1;
      s.data.forEach((v, i) => v != null && (li = i));
      return { s, li, y: li >= 0 ? y(s.data[li] as number) : 0 };
    })
    .filter((e) => e.li >= 0)
    .sort((a, b) => a.y - b.y);
  const labelY = ends.map((e) => e.y);
  for (let i = 1; i < labelY.length; i++) if (labelY[i] - labelY[i - 1] < 13) labelY[i] = labelY[i - 1] + 13;

  return (
    <div className="chart" ref={ref}>
      {W > 0 && (
        <svg width={W} height={height} viewBox={`0 0 ${W} ${height}`} role="img" aria-label={ariaLabel}>
          <YGrid left={L} right={W - R} y={y} axis={axis} />
          {labels.map((lb, i) =>
            i % every === 0 ? (
              <text key={i} className="ticklabel" x={x(i)} y={height - 7} textAnchor="middle">
                {lb}
              </text>
            ) : null,
          )}
          {refLine && (
            <>
              <line className="refline" x1={L} x2={W - R} y1={y(refLine.value)} y2={y(refLine.value)} />
              <text className="reflabel" x={L + 4} y={y(refLine.value) - 6}>
                {refLine.label}
              </text>
            </>
          )}
          {series.map((s) => {
            let d = "", pen = false;
            s.data.forEach((v, i) => {
              if (v == null) return void (pen = false);
              d += `${pen ? "L" : "M"}${x(i)},${y(v)} `;
              pen = true;
            });
            return <g key={s.name}><path d={d} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" strokeDasharray={s.dashed ? "5 4" : undefined} opacity={s.dashed ? 0.75 : 1} />{s.data.map((v,i)=>v!==null&&s.data[i-1]==null&&s.data[i+1]==null?<circle key={i} cx={x(i)} cy={y(v)} r={3} fill={s.color} />:null)}</g>;
          })}
          {ends.map((e, k) => (
            <g key={e.s.name}>
              <circle cx={x(e.li)} cy={e.y} r={3.5} fill={e.s.color} stroke="var(--surface)" strokeWidth={2} />
              <text className="endlabel" x={x(e.li) + 8} y={labelY[k] + 4}>
                {e.s.name}
              </text>
            </g>
          ))}
          {marker && (
            <>
              <circle cx={x(marker.index)} cy={y(marker.value)} r={5} fill="var(--critical)" stroke="var(--surface)" strokeWidth={2} />
              <text className="reflabel" x={x(marker.index) - 8} y={y(marker.value) + 18} textAnchor="end">
                {marker.label}
              </text>
            </>
          )}
          {tip && <line x1={x(tip.index)} x2={x(tip.index)} y1={T} y2={T + ih} stroke="var(--axis)" />}
          <rect
            x={L} y={T} width={iw} height={ih} fill="transparent"
            onPointerMove={(e) => {
              const rc = e.currentTarget.getBoundingClientRect();
              const i = Math.max(0, Math.min(n - 1, Math.round(((e.clientX - rc.left) / rc.width) * (n - 1))));
              setTip({ x: e.clientX - rc.left + L, y: e.clientY - rc.top + T, index: i });
            }}
            onPointerLeave={() => setTip(null)}
          />
        </svg>
      )}
      <Tip tip={tip} width={W}>
        {tip && (
          <>
            <div className="h">{labels[tip.index]}</div>
            {series.map((s) => {
              const v = s.data[tip.index];
              return v == null ? null : <TipRow key={s.name} color={s.color} label={s.name} value={`${num(v, digits)} ${unit}`} />;
            })}
            {extraTip?.(tip.index)}
          </>
        )}
      </Tip>
    </div>
  );
}
