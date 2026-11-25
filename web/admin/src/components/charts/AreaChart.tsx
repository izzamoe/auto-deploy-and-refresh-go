"use client";

// Area chart in plain SVG (no chart library: bundle budget Q-20). Overlapping translucent
// areas with a 2 px line each. Colour never carries identity alone: a legend names the
// series, hovering/focusing shows a tooltip, and a screen-reader table holds every value.
import { useState } from "react";
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

const W = 600;
const H = 200;
const PAD = { l: 28, r: 8, t: 10, b: 22 };

const TONE: Record<AreaSeries["tone"], { fill: string; stroke: string; swatch: string }> = {
  success: { fill: "fill-primary/20", stroke: "stroke-primary", swatch: "bg-primary" },
  danger: { fill: "fill-destructive/20", stroke: "stroke-destructive", swatch: "bg-destructive" },
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
  const [hover, setHover] = useState<number | null>(null);
  const max = niceMax(Math.max(0, ...series.flatMap((s) => s.values)));
  const innerW = W - PAD.l - PAD.r;
  const innerH = H - PAD.t - PAD.b;
  const span = Math.max(1, n - 1);
  const x = (i: number) => PAD.l + (n === 1 ? innerW / 2 : (i / span) * innerW);
  const y = (v: number) => PAD.t + innerH - (v / max) * innerH;
  const ticks = [0, max / 2, max];
  const labelEvery = Math.max(1, Math.ceil(n / 7));

  function onMove(e: PointerEvent<SVGSVGElement>) {
    const box = e.currentTarget.getBoundingClientRect();
    if (box.width === 0 || n === 0) return;
    const px = ((e.clientX - box.left) / box.width) * W;
    const i = Math.round(((px - PAD.l) / innerW) * (n - 1));
    setHover(Math.min(n - 1, Math.max(0, i)));
  }

  return (
    <div className={cn("relative", className)}>
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
        className="h-auto w-full touch-pan-y"
        onPointerMove={onMove}
        onPointerLeave={() => setHover(null)}
      >
        {ticks.map((t) => (
          <g key={t}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(t)} y2={y(t)} className="stroke-border" strokeWidth={1} />
            <text x={PAD.l - 6} y={y(t) + 4} textAnchor="end" className="fill-muted-foreground text-[11px]">
              {Math.round(t)}
            </text>
          </g>
        ))}
        {labels.map((l, i) =>
          i % labelEvery === 0 || i === n - 1 ? (
            <text key={i} x={x(i)} y={H - 6} textAnchor="middle" className="fill-muted-foreground text-[11px]">
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
              <circle key={s.key} cx={x(hover)} cy={y(s.values[hover] ?? 0)} r={4} strokeWidth={2} className={cn("stroke-card", s.tone === "success" ? "fill-primary" : "fill-destructive")} />
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
