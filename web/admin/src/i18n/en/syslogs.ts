// 'syslogs' messages (English). Loaded only when the English UI is needed: ../id/syslogs.ts
// registers this module's loader (002 T072, FR-029). Same keys as the id namespace (typed).
import type { idSyslogs } from "../id/syslogs";

export const enSyslogs: Record<keyof typeof idSyslogs, string> = {
  "syslogs.title": "System logs",
  "syslogs.subtitle": "The systemd journal of auto-deploy itself, for diagnosing the service.",
  "syslogs.service": "Service: {name}",
  "syslogs.loadError": "The system logs could not be loaded.",
  "syslogs.live": "Live",
  "syslogs.live.on": "New lines appear as they are written.",
  "syslogs.pause": "Pause",
  "syslogs.resume": "Resume",
  "syslogs.paused": "Paused: new lines are held.",
  "syslogs.newLines": "{n} new lines",
  "syslogs.newLines.one": "{n} new line",
  "syslogs.gap": "{n} lines were skipped (rate limit).",
  "syslogs.unavailable": "The live log is not available on this server.",
  "syslogs.region": "System log content",
};
