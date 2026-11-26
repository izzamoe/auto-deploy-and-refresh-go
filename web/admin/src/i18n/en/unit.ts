// 'unit' messages (English). Loaded only when the English UI is needed: ../id/unit.ts
// registers this module's loader (002 T072, FR-029). Same keys as the id namespace (typed).
import type { idUnit } from "../id/unit";

export const enUnit: Record<keyof typeof idUnit, string> = {
  "apps.unit": "Generate service unit",
  "apps.unit.title": "Generate systemd service unit",
  "apps.unit.body": "Applying this writes a unit file to /etc/systemd/system as root, then runs daemon-reload and enable. Review the content before confirming.",
  "apps.unit.apply": "Confirm and apply",
  "apps.unit.applying": "Applying…",
  "apps.unit.done": "Service unit created and enabled.",
};
