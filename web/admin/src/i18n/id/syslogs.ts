// 'syslogs' messages (Indonesian). Importing this module registers them (side effect), so a
// component that uses these keys must import it: namespaces.test.ts checks that.
import { registerMessages } from "../registry";

export const idSyslogs = {
  "syslogs.title": "Log sistem",
  "syslogs.subtitle": "Journal systemd dari auto-deploy sendiri, untuk mendiagnosis layanan.",
  "syslogs.service": "Service: {name}",
  "syslogs.loadError": "Log sistem tidak dapat dimuat.",
  "syslogs.autoRefresh": "Muat ulang otomatis (10 detik)",
  "syslogs.region": "Isi log sistem",
} as const;

registerMessages("id", idSyslogs);
