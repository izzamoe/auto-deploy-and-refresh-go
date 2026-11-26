// 'syslogs' messages (Indonesian). Importing this module registers them (side effect), so a
// component that uses these keys must import it: namespaces.test.ts checks that.
import { registerEnLoader, registerMessages } from "../registry";

export const idSyslogs = {
  "syslogs.title": "Log sistem",
  "syslogs.subtitle": "Journal systemd dari auto-deploy sendiri, untuk mendiagnosis layanan.",
  "syslogs.service": "Service: {name}",
  "syslogs.loadError": "Log sistem tidak dapat dimuat.",
  "syslogs.live": "Langsung",
  "syslogs.live.on": "Baris baru tampil saat ditulis.",
  "syslogs.pause": "Jeda",
  "syslogs.resume": "Lanjutkan",
  "syslogs.paused": "Dijeda: baris baru ditahan.",
  "syslogs.newLines": "{n} baris baru",
  "syslogs.newLines.one": "{n} baris baru",
  "syslogs.gap": "{n} baris terlewati (batas laju).",
  "syslogs.unavailable": "Log langsung tidak tersedia di server ini.",
  "syslogs.region": "Isi log sistem",
} as const;

registerMessages("id", idSyslogs);
// English only when the English UI is needed (002 T072, FR-029).
registerEnLoader("syslogs", () => import("../en/syslogs").then((m) => m.enSyslogs), idSyslogs);
