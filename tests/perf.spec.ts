// T104 (NFR-001/NFR-002): First Contentful Paint of the signed-in dashboard on the local build < 1500 ms,
// and the initial JS transferred per route (also printed; the 250 kB gzip budget is measured on disk by
// reference/executor/r7_measure.mjs). Project `perf`: run it explicitly with `--project=perf`; `make e2e`
// and the CI never select it.
import { expect, test } from '@playwright/test';
import { STATIC_ROUTES, adminURL, ready, setPrefs } from './helpers';

const FCP_BUDGET_MS = 1500;
const ROUNDS = 5;

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
