// T104 (NFR-001/NFR-002): First Contentful Paint of the signed-in dashboard on the local build < 1500 ms,
// and the initial JS transferred per route (also printed; the 250 kB gzip budget is measured on disk by
// reference/executor/r7_measure.mjs). Project `perf`: run it explicitly with `--project=perf`; `make e2e`
// and the CI never select it.
//
// 002 (T002, RV2-16): a third test records FCP and LCP of every route (median of ROUNDS cold
// loads) and writes them as JSON, so the baseline (T003 `perf-before.json`) and the later
// comparison (T076) use the same tool. Output file: $PERF_OUT, else
// $PERF_RUN_DIR/perf-<timestamp>.json (default PERF_RUN_DIR = reference/executor, ignored by git).
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';
import { STATIC_ROUTES, adminURL, ready, setPrefs } from './helpers';

const FCP_BUDGET_MS = 1500;
const ROUNDS = 5;

function median(values: number[]): number {
  const s = [...values].sort((a, b) => a - b);
  const mid = Math.floor(s.length / 2);
  return s.length % 2 ? s[mid] : Math.round((s[mid - 1] + s[mid]) / 2);
}

/** FCP from the paint timeline and the last buffered largest-contentful-paint entry. */
async function paintTimings(page: Page): Promise<{ fcp: number; lcp: number }> {
  return page.evaluate(
    () =>
      new Promise<{ fcp: number; lcp: number }>((resolve) => {
        const fcp = performance.getEntriesByName('first-contentful-paint')[0]?.startTime ?? -1;
        let lcp = -1;
        try {
          new PerformanceObserver((list) => {
            for (const e of list.getEntries()) lcp = e.startTime;
          }).observe({ type: 'largest-contentful-paint', buffered: true });
        } catch {
          // LCP unsupported: stays -1
        }
        setTimeout(() => resolve({ fcp: Math.round(fcp), lcp: Math.round(lcp) }), 300);
      }),
  );
}

function perfOutputPath(): string {
  if (process.env.PERF_OUT) return path.resolve(process.env.PERF_OUT);
  const dir = process.env.PERF_RUN_DIR
    ? path.resolve(process.env.PERF_RUN_DIR)
    : path.resolve(__dirname, '..', 'reference', 'executor');
  return path.join(dir, `perf-${Date.now()}.json`);
}

test('dashboard: first contentful paint is under 1500 ms (slowest of 5 cold loads)', async ({ browser }) => {
  const samples: number[] = [];
  for (let i = 0; i < ROUNDS; i++) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 } }); // fresh context = cold HTTP cache
    await setPrefs(context, 'id', 'light');
    const page = await context.newPage();
    await page.goto(adminURL('/'), { waitUntil: 'load' });
    await ready(page);
    const fcp = await page.evaluate(() => performance.getEntriesByName('first-contentful-paint')[0]?.startTime ?? -1);
    expect(fcp, 'the browser reported no first-contentful-paint entry').toBeGreaterThan(0);
    samples.push(Math.round(fcp));
    await context.close();
  }
  console.log(`FCP dashboard (ms): ${samples.join(', ')}; max ${Math.max(...samples)}; budget ${FCP_BUDGET_MS}`);
  expect(Math.max(...samples)).toBeLessThan(FCP_BUDGET_MS);
});

test('initial JS transferred per route is recorded', async ({ browser }) => {
  const rows: string[] = [];
  for (const route of STATIC_ROUTES) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    await setPrefs(context, 'id', 'light');
    const page = await context.newPage();
    let bytes = 0;
    page.on('response', async (res) => {
      if (res.url().endsWith('.js')) {
        const len = Number(res.headers()['content-length'] ?? 0);
        bytes += len || (await res.body().catch(() => Buffer.alloc(0))).length;
      }
    });
    await page.goto(adminURL(route), { waitUntil: 'load' });
    await ready(page);
    rows.push(`${route.padEnd(20)} ${(bytes / 1024).toFixed(1)} kB (wire, as served)`);
    await context.close();
  }
  console.log(`Initial + lazy JS fetched per route:\n${rows.join('\n')}`);
  expect(rows.length).toBe(STATIC_ROUTES.length);
});

test('FCP and LCP per route are recorded (median of 5 cold loads)', async ({ browser }) => {
  test.setTimeout(STATIC_ROUTES.length * ROUNDS * 15_000);
  const routes: Record<string, { fcp: number[]; lcp: number[]; fcpMedian: number; lcpMedian: number }> = {};
  for (const route of STATIC_ROUTES) {
    const fcp: number[] = [];
    const lcp: number[] = [];
    for (let i = 0; i < ROUNDS; i++) {
      const context = await browser.newContext({ viewport: { width: 1280, height: 800 } }); // cold HTTP cache
      await setPrefs(context, 'id', 'light');
      const page = await context.newPage();
      await page.goto(adminURL(route), { waitUntil: 'load' });
      await ready(page);
      const t = await paintTimings(page);
      expect(t.fcp, `${route}: the browser reported no first-contentful-paint entry`).toBeGreaterThan(0);
      expect(t.lcp, `${route}: the browser reported no largest-contentful-paint entry`).toBeGreaterThan(0);
      fcp.push(t.fcp);
      lcp.push(t.lcp);
      await context.close();
    }
    routes[route] = { fcp, lcp, fcpMedian: median(fcp), lcpMedian: median(lcp) };
  }
  const out = { measuredAt: new Date().toISOString(), rounds: ROUNDS, routes };
  const file = perfOutputPath();
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify(out, null, 1));
  const rows = Object.entries(routes).map(
    ([r, v]) => `${r.padEnd(20)} FCP median ${v.fcpMedian} ms (${v.fcp.join(', ')}); LCP median ${v.lcpMedian} ms (${v.lcp.join(', ')})`,
  );
  console.log(`FCP/LCP per route (${ROUNDS} cold loads) -> ${file}\n${rows.join('\n')}`);
  expect(Object.keys(routes).length).toBe(STATIC_ROUTES.length);
});
