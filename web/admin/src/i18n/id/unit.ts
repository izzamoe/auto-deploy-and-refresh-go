// 'unit' messages (Indonesian). Importing this module registers them (side effect), so a
// component that uses these keys must import it: i18n.test.ts checks that.
import { registerMessages } from "../registry";

export const idUnit = {
  "apps.unit": "Buat service unit",
  "apps.unit.title": "Buat service unit systemd",
  "apps.unit.body": "Menerapkan ini menulis berkas unit ke /etc/systemd/system sebagai root, lalu menjalankan daemon-reload dan enable. Periksa isinya sebelum konfirmasi.",
  "apps.unit.apply": "Konfirmasi dan terapkan",
  "apps.unit.applying": "Menerapkan…",
  "apps.unit.done": "Unit service dibuat dan diaktifkan.",
} as const;

registerMessages("id", idUnit);
