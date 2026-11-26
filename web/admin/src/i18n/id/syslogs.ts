// 'syslogs' messages (Indonesian). Importing this module registers them (side effect), so a
// component that uses these keys must import it: namespaces.test.ts checks that.
import { registerMessages } from "../registry";

export const idSyslogs = {
  "syslogs.title": "Log sistem",
  "syslogs.subtitle": "Journal systemd dari auto-deploy sendiri, untuk mendiagnosis layanan.",
  "syslogs.service": "Service: {name}",
  "syslogs.loadError": "Log sistem tidak dapat dimuat.",
  "syslogs.autoRefresh": "Muat ulang otomatis (10 detik)",
  "syslogs.live": "Langsung",
  "syslogs.live.on": "Baris baru tampil saat ditulis.",
  "syslogs.pause": "Jeda",
  "syslogs.resume": "Lanjutkan",
  "syslogs.paused": "Dijeda: baris baru ditahan.",
  "syslogs.newLines": "{n} baris baru",
  "syslogs.newLines.one": "{n} baris baru",
  "syslogs.gap": "{n} baris terlewati (batas laju).",
  "syslogs.behind": "Sebagian log terlewat; memuat ulang baris terbaru.",
  "syslogs.unavailable": "Log langsung tidak tersedia di server ini.",
  "syslogs.region": "Isi log sistem",
} as const;

registerMessages("id", idSyslogs);
