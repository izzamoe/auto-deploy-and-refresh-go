# scripts/perf — pengukuran fitur 002 (realtime event-driven)

Harness ini dipindah dari `reference/spike/` (T002) agar pengukuran sebelum/sesudah memakai alat
yang sama dan ikut repo. Semua perintah dijalankan dari root repo. Server dijalankan lokal di
`127.0.0.1` (port bebas) dengan DB baru; tidak ada server live yang dihubungi.

## Direktori kerja (`PERF_RUN_DIR`)

Setiap run membuat subdirektori `run-<jenis>-<ts>` di bawah `$PERF_RUN_DIR` (default
`reference/executor/`, diabaikan git). Direktori itu berisi DB, `server.log`, dan
`initial-admin-password` (password awal yang dibuat server) — karena itu JANGAN mengarahkannya ke
pohon kerja yang di-commit.

## Alat

| Skrip | Pakai | Keluaran |
|---|---|---|
| `poll-harness.mjs` | `node scripts/perf/poll-harness.mjs <binary> <N app> <detik> <out.json>` | request `/admin/api/v1/*` per rute: `initial`, `idle` (+`idlePerMin`), `focus`, `reconnect`, rincian per endpoint, frame WS saat diam; `base` = `git rev-parse --short HEAD` |
| `assert-poll.py` | `python3 -I scripts/perf/assert-poll.py <poll.json> --expect baseline\|target` | exit 1 + rute yang melanggar |
| `transfer-harness.mjs` | `node scripts/perf/transfer-harness.mjs <binary> <out.json>` | byte per jenis (js/rsc/css/font/api) per fase (`load`, `afterload`, navigasi), FCP/LCP/TBT kasar |
| `ws-load.mjs` | `[PPROF_ADDR=127.0.0.1:6061] node scripts/perf/ws-load.mjs <binary> <out.json> [2\|3]` | RSS, thread OS, CPU ms/30 s, frame/30 s, goroutine (bila `PPROF_ADDR`) untuk 0/1/10/50 koneksi |
| `measure-bundle.py` | `python3 -I scripts/perf/measure-bundle.py web/admin/out [--json] [--write-baseline <json>] [--check <json> [--target-module B] [--target-all B]]` | JS awal per rute (raw, gzip-9, brotli-11); `--check` exit 1 bila rute naik atau target terlampaui |
| `analyze-parts.py` | `python3 -I scripts/perf/analyze-parts.py <export.jsonl>` | atribusi ukuran per paket/direktori dari `next analyze export` |
| `tests/perf.spec.ts` | `PERF_OUT=<json> npx playwright test --project=perf` | FCP dasbor (anggaran 1500 ms), JS per rute, FCP/LCP median 5 muat dingin per rute ke JSON |

## Arti kolom

- `poll-harness`: `initial` = request sampai render pertama + `networkidle` + 1,5 s;
  `idle`/`idlePerMin` = request selama jendela diam (semua tab dibekukan serentak);
  `focus` = request dalam 3 s setelah satu `visibilitychange` (setelah 6 s > `staleTime`);
  `reconnect` = request dalam 5 s setelah `setOffline(true)` 5 s -> `false`.
- `assert-poll --expect baseline`: idle/menit 2,0 (dasbor 4,0) ±0,5 dan `/apps/` initial = N+3
  (`research.md` §2). `--expect target`: idle, focus, reconnect = 0 dan `/apps/` initial ≤ 3
  (SC-001, SC-002, SC-003, SC-005).
- `measure-bundle`: `js_gz` = gzip semua `<script src>` (SC-007b); `gz_mod` = tanpa skrip
  `noModule` yang tidak diunduh peramban modern (SC-007a, "efektif").
- `ws-load`: `rssKiB`/`threads` dari `/proc/<pid>/status`, CPU dari `/proc/<pid>/stat`
  (resolusi 10 ms), `goroutines` dari `/debug/pprof/goroutine?debug=1`.

## Keterbatasan (research.md §12)

- Butir 5: fokus disimulasikan dengan `window.dispatchEvent(new Event("visibilitychange"))` saat
  `visibilityState` tetap `visible` — sama dengan pendengar TanStack, tetapi bukan perpindahan
  tab nyata. Satu request interval bisa jatuh di jendela 3 s (peluang ≈ 10 %).
- Butir 6: reconnect memakai `context.setOffline` (memutus WS dan memicu `online` TanStack);
  efek `invalidateAll` dan `refetchOnReconnect` tidak dipisahkan.
- Angka CPU/latensi/RSS dari mesin pengembang x86_64, bukan aarch64 produksi.
