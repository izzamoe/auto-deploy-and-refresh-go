// Decorative mini bar chart (the stat card already prints the number, so this is
// aria-hidden). Plain SVG; bars are anchored to the baseline with rounded tops.
export function MiniBars({ values, className }: { values: readonly number[]; className?: string }) {
  const max = Math.max(1, ...values);
  const n = Math.max(1, values.length);
  const slot = 100 / n;
  const barW = Math.max(1, slot * 0.6);
  return (
    <svg aria-hidden="true" focusable="false" viewBox="0 0 100 32" preserveAspectRatio="none" className={className} data-testid="mini-bars">
      {values.map((v, i) => {
        const h = v <= 0 ? 1 : Math.max(2, (v / max) * 30);
        return <rect key={i} x={i * slot + (slot - barW) / 2} y={32 - h} width={barW} height={h} rx={1} className={v > 0 ? "fill-primary-text" : "fill-track/50"} />;
      })}
    </svg>
  );
}
