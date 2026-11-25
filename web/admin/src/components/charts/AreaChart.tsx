"use client";

// Area chart in plain SVG (no chart library: bundle budget Q-20). Overlapping translucent
// areas with a 2 px line each. Colour never carries identity alone: a legend names the
// series, hovering/focusing shows a tooltip, and a screen-reader table holds every value.
import { useEffect, useRef, useState } from "react";
import type { PointerEvent } from "react";
import { cn } from "@/lib/utils";

export interface AreaSeries {
  key: string;
  label: string;
  values: readonly number[];
  /** Token-based colour; success = brand green, danger = red. */
  tone: "success" | "danger";
}

interface AreaChartProps {
  /** One label per x point (already formatted dates). */
  labels: readonly string[];
  series: readonly AreaSeries[];
  ariaLabel: string;
  tableCaption: string;
  dateHeader: string;
  /** Extra columns that appear in the table only (e.g. canceled). */
  extraColumns?: readonly { key: string; label: string; values: readonly number[] }[];
  className?: string;
}

// The viewBox follows the real pixel width of the chart (see `useWidth`), so axis text stays
// 12 px on a 360 px phone as well as on a desktop; no scaled-down 6 px labels.
const DEFAULT_W = 600;
const H = 220;
const PAD = { l: 34, r: 14, t: 10, b: 30 };
const MIN_LABEL_GAP = 64; // px between two x labels

function useWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(DEFAULT_W);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const read = () => {
      const w = Math.round(el.clientWidth);
      if (w > 0) setWidth(w);
    };
    read();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(read);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}

const TONE: Record<AreaSeries["tone"], { fill: string; stroke: string; swatch: string }> = {
  success: { fill: "fill-primary-text/20", stroke: "stroke-primary-text", swatch: "bg-primary-text" },
  danger: { fill: "fill-danger-chart/20", stroke: "stroke-danger-chart", swatch: "bg-danger-chart" },
};

function niceMax(v: number): number {
  if (v <= 4) return 4;
  const pow = 10 ** Math.floor(Math.log10(v));
  const f = v / pow;
  const nice = f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return nice * pow;
}

export function AreaChart({ labels, series, ariaLabel, tableCaption, dateHeader, extraColumns = [], className }: AreaChartProps) {
  const n = labels.length;
  const [boxRef, W] = useWidth();
  const [hover, setHover] = useState<number | null>(null);
  const max = niceMax(Math.max(0, ...series.flatMap((s) => s.values)));
  const innerW = W - PAD.l - PAD.r;
  const innerH = H - PAD.t - PAD.b;
  const span = Math.max(1, n - 1);
  const x = (i: number) => PAD.l + (n === 1 ? innerW / 2 : (i / span) * innerW);
  const y = (v: number) => PAD.t + innerH - (v / max) * innerH;
  const ticks = [0, max / 2, max];
  // Labels are counted back from the last point so the newest date is always shown and the
  // spacing is even; how many fit depends on the real width.
  const fit = Math.max(2, Math.floor(innerW / MIN_LABEL_GAP) + 1);
  const labelEvery = Math.max(1, Math.ceil((n - 1) / (fit - 1)));

  function onMove(e: PointerEvent<SVGSVGElement>) {
    const box = e.currentTarget.getBoundingClientRect();
    if (box.width === 0 || n === 0) return;
    const px = ((e.clientX - box.left) / box.width) * W;
    const i = Math.round(((px - PAD.l) / innerW) * (n - 1));
    setHover(Math.min(n - 1, Math.max(0, i)));
  }

  return (
    <div ref={boxRef} className={cn("relative", className)}>
      <ul className="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-sm" data-testid="chart-legend">
        {series.map((s) => (
          <li key={s.key} className="flex items-center gap-2">
            <span aria-hidden="true" className={cn("inline-block size-2.5 rounded-full", TONE[s.tone].swatch)} />
            <span>{s.label}</span>
          </li>
        ))}
      </ul>

      <svg
        role="img"
        aria-label={ariaLabel}
        viewBox={`0 0 ${W} ${H}`}
        width={W}
        height={H}
        className="block h-auto w-full touch-pan-y"
        onPointerMove={onMove}
        onPointerLeave={() => setHover(null)}
      >
        {ticks.map((t) => (
          <g key={t}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(t)} y2={y(t)} className="stroke-border" strokeWidth={1} />
            <text x={PAD.l - 6} y={y(t) + 4} textAnchor="end" className="fill-muted-foreground text-xs">
              {Math.round(t)}
            </text>
          </g>
        ))}
        {labels.map((l, i) =>
          (n - 1 - i) % labelEvery === 0 ? (
            <text key={i} x={x(i)} y={H - 8} textAnchor={i === n - 1 ? "end" : i === 0 ? "start" : "middle"} className="fill-muted-foreground text-xs">
              {l}
            </text>
          ) : null,
        )}
        {n > 0 &&
          series.map((s) => {
            const pts = s.values.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`);
            const base = y(0).toFixed(1);
            const area = `M ${x(0).toFixed(1)},${base} L ${pts.join(" L ")} L ${x(n - 1).toFixed(1)},${base} Z`;
            return (
              <g key={s.key}>
                <path d={area} className={TONE[s.tone].fill} />
                <polyline points={pts.join(" ")} fill="none" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" className={TONE[s.tone].stroke} />
              </g>
            );
          })}
        {hover !== null && (
          <g data-testid="chart-crosshair">
            <line x1={x(hover)} x2={x(hover)} y1={PAD.t} y2={PAD.t + innerH} className="stroke-muted-foreground" strokeWidth={1} strokeDasharray="3 3" />
            {series.map((s) => (
              <circle key={s.key} cx={x(hover)} cy={y(s.values[hover] ?? 0)} r={4} strokeWidth={2} className={cn("stroke-card", s.tone === "success" ? "fill-primary-text" : "fill-danger-chart")} />
            ))}
          </g>
        )}
      </svg>

      {hover !== null && (
        <div
          role="tooltip"
          data-testid="chart-tooltip"
          className="pointer-events-none absolute right-2 top-8 rounded-xl bg-nav px-3 py-2 text-xs text-nav-foreground shadow-card"
        >
          <p className="font-medium">{labels[hover]}</p>
          {series.map((s) => (
            <p key={s.key}>
              {s.label}: {s.values[hover] ?? 0}
            </p>
          ))}
        </div>
      )}

      {/* The same data as a table, for screen readers (FR-108). */}
      <table className="sr-only" data-testid="chart-table">
        <caption>{tableCaption}</caption>
        <thead>
          <tr>
            <th scope="col">{dateHeader}</th>
            {series.map((s) => (
              <th key={s.key} scope="col">
                {s.label}
              </th>
            ))}
            {extraColumns.map((c) => (
              <th key={c.key} scope="col">
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {labels.map((l, i) => (
            <tr key={i}>
              <th scope="row">{l}</th>
              {series.map((s) => (
                <td key={s.key}>{s.values[i] ?? 0}</td>
              ))}
              {extraColumns.map((c) => (
                <td key={c.key}>{c.values[i] ?? 0}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
