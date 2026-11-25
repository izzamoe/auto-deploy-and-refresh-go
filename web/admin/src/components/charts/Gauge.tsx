// Half-donut gauge, plain SVG (no chart library: bundle budget Q-20). The arc colour is a
// design token (fill/stroke classes), so it follows the theme. The number sits in the
// parent; this draws only the track and the value arc.
interface GaugeProps {
  /** 0..1; null = no data (only the track is drawn). */
  value: number | null;
  /** Accessible name, e.g. "Tingkat sukses 75%". */
  label: string;
  className?: string;
}

const R = 80;
const CX = 100;
const CY = 100;
const STROKE = 16;

function arc(frac: number): string {
  const a = Math.PI * (1 - frac); // 180deg (left) .. 0deg (right)
  const x = CX + R * Math.cos(a);
  const y = CY - R * Math.sin(a);
  return `M ${CX - R} ${CY} A ${R} ${R} 0 0 1 ${x.toFixed(2)} ${y.toFixed(2)}`;
}

export function Gauge({ value, label, className }: GaugeProps) {
  const v = value === null ? 0 : Math.min(1, Math.max(0, value));
  return (
    <svg role="img" aria-label={label} viewBox="0 0 200 110" className={className}>
      <path d={`M ${CX - R} ${CY} A ${R} ${R} 0 0 1 ${CX + R} ${CY}`} fill="none" strokeWidth={STROKE} strokeLinecap="round" className="stroke-track" />
      {v > 0 && <path d={arc(v)} fill="none" strokeWidth={STROKE} strokeLinecap="round" className="stroke-primary-text" data-testid="gauge-arc" />}
    </svg>
  );
}
