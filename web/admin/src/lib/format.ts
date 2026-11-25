// Small, locale-neutral formatters (units stay B/KB/MB/GB: they are symbols, not prose).

function oneDecimal(v: number): string {
  return v < 10 ? v.toFixed(1) : String(Math.round(v));
}

const finite = (v: number | undefined): v is number => typeof v === "number" && Number.isFinite(v) && v >= 0;

export function formatBytes(bytes: number | undefined): string | undefined {
  if (!finite(bytes)) return undefined;
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  if (bytes < 1024 ** 2) return `${oneDecimal(bytes / 1024)} KB`;
  if (bytes < 1024 ** 3) return `${oneDecimal(bytes / 1024 ** 2)} MB`;
  return `${oneDecimal(bytes / 1024 ** 3)} GB`;
}

export function formatSpeed(bytesPerSecond: number | undefined): string | undefined {
  const b = formatBytes(bytesPerSecond);
  return b ? `${b}/s` : undefined;
}

export function formatDuration(ms: number | undefined): string | undefined {
  if (!finite(ms)) return undefined;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)} s`;
  return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}
