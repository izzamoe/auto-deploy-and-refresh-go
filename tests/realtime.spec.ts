// 002 realtime event-driven admin (project `realtime`). Written RED first (T010-T013, T018):
// each test proves a waste or a desync of the pre-002 panel and turns GREEN in STOP-6.
//   idle      (T010, FR-001, SC-001): an idle tab sends no REST request on any route
//   two tabs  (T011, FR-004, SC-004): a change in tab A shows in tab B without REST in B
//   reconnect (T012, FR-011, SC-003): 30 s offline then online refetches nothing
//   focus     (T013, FR-014, SC-002): a focus event refetches nothing while live
//   topics    (T018, FR-015, SC-011): /account/ (no `jobs` topic) receives no progress frame
//   navigate  (T052, FR-014, RV-8): leave /apps/, change it elsewhere, come back -> shown with <= 1 REST
//   bundle    (T073, FR-027, FR-028, SC-007): a cold load fetches no other route before intent
//   latency   (T081, NFR-001, SC-004): p95 change -> shown in another tab <= 500 ms (LOCAL only:
//             the CI runs `--grep-invert "latency"`, RV2-15)
import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { expect, request as pwRequest, test } from '@playwright/test';
import type { APIRequestContext, Browser, BrowserContext, Page } from '@playwright/test';
import { STATIC_ROUTES, STORAGE_STATE, adminURL, baseURL, browserHeaders, countApi, firstApp, ready, seedJob, setPrefs } from './helpers';

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function openTab(browser: Browser, route: string): Promise<{ context: BrowserContext; page: Page; api: ReturnType<typeof countApi> }> {
  const context = await browser.newContext({ storageState: STORAGE_STATE, viewport: { width: 1280, height: 800 } });
  await setPrefs(context, 'en', 'light');
  const page = await context.newPage();
  const api = countApi(page);
  await page.goto(adminURL(route), { waitUntil: 'load' });
  await ready(page);
  return { context, page, api };
}

/** Waits for the header to report a live socket (data-connection, T059); 5 s when absent. */
async function waitLive(page: Page): Promise<void> {
  await page.locator('[data-connection="live"]').first().waitFor({ state: 'attached', timeout: 5_000 }).catch(() => {});
}

test.describe('idle', () => {
  test('idle: no periodic REST on any route', async ({ browser }) => {
    // RV3-7: the eight routes idle IN PARALLEL (one 65 s window), each in its own context.
    const tabs = await Promise.all(STATIC_ROUTES.map((route) => openTab(browser, route)));
    try {
      for (const t of tabs) t.api.reset();
      await sleep(65_000);
      const violations = tabs.flatMap((t, i) => t.api.list().map((req) => `${STATIC_ROUTES[i]}: ${req}`));
      expect(violations, 'REST requests of idle tabs during 65 s (route: request)').toEqual([]);
    } finally {
      await Promise.all(tabs.map((t) => t.context.close()));
    }
  });
});

test.describe('two tabs', () => {
  test('two tabs: a change in A appears in B without REST', async ({ browser, request }) => {
    const app = await firstApp(request);
    const a = await openTab(browser, '/apps/');
    const b = await openTab(browser, '/apps/');
    try {
      const cardA = a.page.getByTestId(`app-card-${app.id}`);
      const cardB = b.page.getByTestId(`app-card-${app.id}`);
      await expect(cardA.getByRole('button', { name: 'Disable' })).toBeVisible();
      await expect(cardB.getByRole('button', { name: 'Disable' })).toBeVisible();
      b.api.reset();

      await cardA.getByRole('button', { name: 'Disable' }).click();
      await expect(cardA.getByRole('button', { name: 'Enable' })).toBeVisible();
      // B is not touched: the new state must arrive by itself, within 2 s.
      await expect(cardB.getByRole('button', { name: 'Enable' }), 'tab B: Timed out (2 s) waiting for the app disabled in A').toBeVisible({ timeout: 2_000 });
      expect(b.api.list(), 'REST requests of tab B').toEqual([]);
    } finally {
      await request.post(`${baseURL()}/admin/api/v1/apps/${app.id}/toggle`, { data: { enabled: true }, headers: browserHeaders() });
      await a.context.close();
      await b.context.close();
    }
  });

  test('two tabs: a Telegram setting saved in A appears in B without REST', async ({ browser }) => {
    const a = await openTab(browser, '/settings/telegram/');
    const b = await openTab(browser, '/settings/telegram/');
    try {
      const chat = `@pwrt${Date.now() % 1_000_000}`;
      b.api.reset();
      const saved = await a.page.request.post(`${baseURL()}/admin/api/v1/telegram`, {
        data: { appId: 0, appHash: '', botToken: '', chatUsername: chat, enabled: false },
        headers: browserHeaders(),
      });
      expect(saved.status()).toBe(200);
      await expect(b.page.locator('#tg-chat'), 'tab B: Timed out (2 s) waiting for the Telegram chat saved in A').toHaveValue(chat, { timeout: 2_000 });
      // T011 amendment (Hermes, HANDOFF exception (c)): settings.changed carries only the section
      // (events-ws.v3 §2.2), so B makes exactly one targeted request and nothing else (FR-004(c)).
      expect(b.api.list(), 'REST requests of tab B').toEqual(['GET /admin/api/v1/telegram']);
    } finally {
      await a.context.close();
      await b.context.close();
    }
  });

  test('two tabs: a token created in A appears in B without REST', async ({ browser }) => {
    const a = await openTab(browser, '/account/');
    const b = await openTab(browser, '/account/');
    let tokenId = '';
    try {
      const name = `pw-rt-${Date.now()}`;
      b.api.reset();
      const created = await a.page.request.post(`${baseURL()}/admin/api/v1/tokens`, { data: { name }, headers: browserHeaders() });
      expect(created.status()).toBe(201);
      tokenId = (await created.json()).id;
      await expect(b.page.getByTestId('tokens-card'), 'tab B: Timed out (2 s) waiting for the token created in A').toContainText(name, { timeout: 2_000 });
      // T011 amendment (Hermes, HANDOFF exception (c)): account.changed carries only the section
      // (events-ws.v3 §2.2), so B makes exactly one targeted request and nothing else (FR-004(c)).
      expect(b.api.list(), 'REST requests of tab B').toEqual(['GET /admin/api/v1/tokens']);
    } finally {
      if (tokenId) await a.page.request.delete(`${baseURL()}/admin/api/v1/tokens/${tokenId}`, { headers: browserHeaders() });
      await a.context.close();
      await b.context.close();
    }
  });
});

test.describe('reconnect', () => {
  test('reconnect: 30 s offline then online makes no REST', async ({ browser }) => {
    const routes = ['/', '/apps/'];
    const tabs = await Promise.all(routes.map((route) => openTab(browser, route)));
    try {
      await Promise.all(tabs.map((t) => t.context.setOffline(true)));
      await sleep(30_000);
      // Count from the moment the network is back: the recovery itself must cost no request.
      for (const t of tabs) t.api.reset();
      await Promise.all(tabs.map((t) => t.context.setOffline(false)));
      await Promise.all(tabs.map((t) => waitLive(t.page)));
      await sleep(5_000);
      const violations = tabs.flatMap((t, i) => t.api.list().map((req) => `${routes[i]}: ${req}`));
      expect(violations, 'REST requests after reconnecting (route: request)').toEqual([]);
    } finally {
      await Promise.all(tabs.map((t) => t.context.close()));
    }
  });
});

test.describe('focus', () => {
  test('focus: visibilitychange makes no REST while live', async ({ browser }) => {
    const t = await openTab(browser, '/apps/');
    try {
      await sleep(6_000); // > staleTime 5 s
      t.api.reset();
      // TanStack Query's focusManager listens to `visibilitychange` on window (research.md S4).
      await t.page.evaluate(() => window.dispatchEvent(new Event('visibilitychange')));
      await sleep(3_000);
      expect(t.api.list(), 'REST requests after one focus event on /apps/').toEqual([]);
    } finally {
      await t.context.close();
    }
  });
});

test.describe('navigate', () => {
  test('navigate away and back: changes made meanwhile are shown with <= 1 REST', async ({ browser, request }) => {
    const app = await firstApp(request);
    const t = await openTab(browser, '/apps/');
    try {
      const card = t.page.getByTestId(`app-card-${app.id}`);
      await expect(card.getByRole('button', { name: 'Disable' })).toBeVisible();
      await waitLive(t.page);
      // Client-side navigation: the socket stays; the page leaves topic `apps` (unsub after 1 s).
      await t.page.getByRole('link', { name: 'Account' }).first().click();
      await expect(t.page.getByTestId('tokens-card')).toBeVisible();
      await sleep(2_000);
      // Another context disables the app while this tab is elsewhere.
      const off = await request.post(`${baseURL()}/admin/api/v1/apps/${app.id}/toggle`, { data: { enabled: false }, headers: browserHeaders() });
      expect(off.status()).toBe(200);
      await sleep(1_000);
      t.api.reset();
      await t.page.getByRole('link', { name: 'Apps' }).first().click();
      await expect(card.getByRole('button', { name: 'Enable' }), 'the change made meanwhile is shown').toBeVisible({ timeout: 5_000 });
      await sleep(3_000);
      const rest = t.api.list();
      expect(rest.length, `REST requests after coming back to /apps/: ${rest.join(', ')}`).toBeLessThanOrEqual(1);
    } finally {
      await request.post(`${baseURL()}/admin/api/v1/apps/${app.id}/toggle`, { data: { enabled: true }, headers: browserHeaders() });
      await t.context.close();
    }
  });
});

test.describe('topics', () => {
  test('topics: /account/ receives no progress frames', async ({ browser, request }) => {
    const context = await browser.newContext({ storageState: STORAGE_STATE, viewport: { width: 1280, height: 800 } });
    await setPrefs(context, 'en', 'light');
    const page = await context.newPage();
    let progressFrames = 0;
    page.on('websocket', (ws) => {
      ws.on('framereceived', (f) => {
        const data = typeof f.payload === 'string' ? f.payload : f.payload.toString();
        if (data.includes('"t":"p"')) progressFrames++;
      });
    });
    try {
      await page.goto(adminURL('/account/'), { waitUntil: 'load' });
      await ready(page);
      await seedJob(request, 'pw-topics');
      await sleep(10_000);
      expect(progressFrames, 'progress frames received by /account/ during a deploy').toBe(0);
    } finally {
      await context.close();
    }
  });
});

// T073 (FR-027, FR-028, SC-007, Q-8=A): a cold load fetches only its own route; another route is
// prefetched only on intent (hover/focus/touch of its link), and only that route.
/** The route an RSC payload belongs to: `/admin/apps/edit/__next._tree.txt` -> `/apps/edit/`. */
function rscRoute(pathname: string): string {
  const dir = pathname.replace(/^\/admin/, '').replace(/[^/]*$/, '');
  return dir === '' ? '/' : dir;
}

async function coldLoad(browser: Browser, route: string) {
  const context = await browser.newContext({ storageState: STORAGE_STATE, viewport: { width: 1280, height: 800 } });
  await setPrefs(context, 'en', 'light');
  const page = await context.newPage();
  const rsc: string[] = [];
  const afterLoad: Promise<{ path: string; bytes: number }>[] = [];
  let loaded = false;
  page.once('load', () => {
    loaded = true;
  });
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith('/admin/') && u.pathname.endsWith('.txt')) rsc.push(u.pathname);
  });
  page.on('response', (res) => {
    const u = new URL(res.url());
    if (!loaded || !u.pathname.endsWith('.js')) return;
    afterLoad.push(res.body().then((b) => ({ path: u.pathname, bytes: b.length }), () => ({ path: u.pathname, bytes: 0 })));
  });
  await page.goto(adminURL(route), { waitUntil: 'load' });
  await ready(page);
  await page.waitForLoadState('networkidle');
  await sleep(4_000);
  return { context, page, rsc, afterLoad: () => Promise.all(afterLoad) };
}

test.describe('bundle', () => {
  test("bundle: a cold load fetches no other route's chunks before intent", async ({ browser }) => {
    for (const route of STATIC_ROUTES) {
      const t = await coldLoad(browser, route);
      try {
        const foreign = t.rsc.filter((p) => rscRoute(p) !== route);
        expect(foreign, `${route}: RSC payloads of other routes fetched before any intent`).toEqual([]);
        const js = await t.afterLoad();
        const bytes = js.reduce((n, f) => n + f.bytes, 0);
        console.log(`[bundle] ${route}: ${t.rsc.length} RSC payload(s) of its own; ${js.length} JS file(s) after load, ${bytes} B raw ${js.map((f) => `${f.path}=${f.bytes}`).join(' ')}`);
        if (route === '/') expect(bytes, `JS fetched after load on the dashboard (raw B): ${js.map((f) => `${f.path}=${f.bytes}`).join(', ')}`).toBeLessThanOrEqual(15_000); // SC-007d
      } finally {
        await t.context.close();
      }
    }
  });

  test('bundle: hovering a sidebar link prefetches that route only', async ({ browser }) => {
    const t = await coldLoad(browser, '/');
    try {
      expect(t.rsc.filter((p) => rscRoute(p) !== '/'), 'nothing of another route before the hover').toEqual([]);
      const before = t.rsc.length;
      await t.page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'History' }).hover();
      await expect.poll(() => t.rsc.length, { message: 'hovering the History link prefetches /history/', timeout: 5_000 }).toBeGreaterThan(before);
      await sleep(2_000);
      const fetched = t.rsc.slice(before);
      expect(fetched.filter((p) => rscRoute(p) !== '/history/'), `RSC payloads after hovering History: ${fetched.join(', ')}`).toEqual([]);
    } finally {
      await t.context.close();
    }
  });
});

// ---------------------------------------------------------------------------------------
// T081 (NFR-001, SC-004, Q-12): cross-tab latency. 50 trials per kind; t0 = just before the
// action in tab A (a click, or the webhook POST for a job), t1 = the MutationObserver in tab B
// seeing the change (both Date.now() on the same machine). p95 must be <= 500 ms. Local only
// (RV2-15: the CI runs `--grep-invert "latency"`). RV-25: jobs only assert the queued job
// appearing (never a terminal state); the service kind uses a FAKE systemctl (a second server
// with the fake first on its PATH), never a real `systemctl stop/start` (RV2-17). SC-012 (journal
// latency) is proven by TestJournalFollower_LatencyUnder1s (T046): e2e has no journal unit.
// ---------------------------------------------------------------------------------------
const LATENCY_TRIALS = 50;
const LATENCY_P95_MAX_MS = 500;

/** What tab B waits for: an element whose text includes `text`, whose value is `value`, or whose `attr` is `attrValue`. */
type Match = { selector: string; text?: string; value?: string; attr?: string; attrValue?: string };

/** Arms a MutationObserver in B that records Date.now() the first time `m` holds. */
async function armB(page: Page, m: Match): Promise<void> {
  const already = await page.evaluate((m) => {
    const w = window as unknown as { __latT1: number };
    const holds = () => {
      const el = document.querySelector(m.selector);
      if (!el) return false;
      if (m.text !== undefined) return (el.textContent ?? '').includes(m.text);
      if (m.value !== undefined) return (el as HTMLInputElement).value === m.value;
      if (m.attr !== undefined) return el.getAttribute(m.attr) === m.attrValue;
      return false;
    };
    w.__latT1 = 0;
    if (holds()) return true;
    const mo = new MutationObserver(() => {
      if (w.__latT1 === 0 && holds()) {
        w.__latT1 = Date.now();
        mo.disconnect();
      }
    });
    mo.observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true });
    return false;
  }, m);
  expect(already, `tab B already shows ${JSON.stringify(m)} before the action`).toBe(false);
}

/** Runs `act` (tab A) and returns t1 - t0 as seen by tab B (fails after 5 s). */
async function measure(b: Page, m: Match, act: () => Promise<unknown>): Promise<number> {
  await armB(b, m);
  const t0 = Date.now();
  await act();
  const handle = await b.waitForFunction(() => (window as unknown as { __latT1: number }).__latT1, null, { timeout: 5_000, polling: 50 });
  return (await handle.jsonValue()) - t0;
}

function report(kind: string, samples: number[]): { p50: number; p95: number } {
  const s = [...samples].sort((x, y) => x - y);
  const rank = (p: number) => s[Math.max(0, Math.ceil((p / 100) * s.length) - 1)];
  const out = { p50: rank(50), p95: rank(95) };
  console.log(`[latency] ${kind}: n=${s.length} p50=${out.p50} ms p95=${out.p95} ms min=${s[0]} ms max=${s[s.length - 1]} ms`);
  return out;
}

async function createApp(api: APIRequestContext, base: string, prefix: string, headers: Record<string, string> = browserHeaders()): Promise<string> {
  const name = `${prefix}-${Date.now()}`;
  const res = await api.post(`${base}/admin/api/v1/apps`, {
    data: { name, binaryPath: `/opt/${name}/app`, serviceName: `${name}.service`, githubRepo: `example/${name}`, artifactName: `${name}-art` },
    headers,
  });
  expect([200, 201], `create app ${name}`).toContain(res.status());
  const body = await res.json();
  return body.app?.id ?? body.id;
}

test.describe('latency', () => {
  test('latency: toggle app (A /apps/ -> B /apps/)', async ({ browser, request }) => {
    const appId = await createApp(request, baseURL(), 'pw-lat-toggle');
    const a = await openTab(browser, '/apps/');
    const b = await openTab(browser, '/apps/');
    try {
      await waitLive(a.page);
      await waitLive(b.page);
      const cardA = a.page.getByTestId(`app-card-${appId}`);
      const pill = `[data-testid="app-card-${appId}"] header > span`;
      await expect(cardA.getByRole('button', { name: 'Disable' })).toBeVisible();
      const samples: number[] = [];
      for (let i = 0; i < LATENCY_TRIALS; i++) {
        const disabling = i % 2 === 0;
        const button = cardA.getByRole('button', { name: disabling ? 'Disable' : 'Enable' });
        samples.push(await measure(b.page, { selector: pill, text: disabling ? 'Disabled' : 'Enabled' }, () => button.click()));
        await expect(cardA.getByRole('button', { name: disabling ? 'Enable' : 'Disable' })).toBeVisible();
      }
      expect(report('toggle app', samples).p95).toBeLessThanOrEqual(LATENCY_P95_MAX_MS);
    } finally {
      await a.context.close();
      await b.context.close();
      await request.delete(`${baseURL()}/admin/api/v1/apps/${appId}`, { headers: browserHeaders() });
    }
  });

  test('latency: edit app (A /apps/edit/ -> B /apps/)', async ({ browser, request }) => {
    const appId = await createApp(request, baseURL(), 'pw-lat-edit');
    const a = await openTab(browser, `/apps/edit/?id=${appId}`);
    const b = await openTab(browser, '/apps/');
    try {
      await waitLive(a.page);
      await waitLive(b.page);
      const save = a.page.getByRole('button', { name: 'Save changes' });
      const card = `[data-testid="app-card-${appId}"]`;
      const samples: number[] = [];
      for (let i = 0; i < LATENCY_TRIALS; i++) {
        const artifact = `lat-art-${Date.now()}-${i}`;
        await a.page.locator('#f-artifact').fill(artifact);
        samples.push(await measure(b.page, { selector: card, text: artifact }, () => save.click()));
        await expect(a.page.getByText('App updated.')).toBeVisible();
        await expect(save).toBeEnabled();
      }
      expect(report('edit app', samples).p95).toBeLessThanOrEqual(LATENCY_P95_MAX_MS);
    } finally {
      await a.context.close();
      await b.context.close();
      await request.delete(`${baseURL()}/admin/api/v1/apps/${appId}`, { headers: browserHeaders() });
    }
  });

  test('latency: Telegram setting (A /settings/telegram/ -> B)', async ({ browser }) => {
    const a = await openTab(browser, '/settings/telegram/');
    const b = await openTab(browser, '/settings/telegram/');
    try {
      await waitLive(a.page);
      await waitLive(b.page);
      const chat = a.page.locator('#tg-chat');
      const save = a.page.getByRole('button', { name: 'Save settings' });
      const samples: number[] = [];
      for (let i = 0; i < LATENCY_TRIALS; i++) {
        const value = `@pwlat${Date.now() % 1_000_000}${i}`;
        await chat.fill(value);
        samples.push(await measure(b.page, { selector: '#tg-chat', value }, () => save.click()));
        await expect(save).toBeEnabled();
        await expect(chat).toHaveValue(value);
      }
      expect(report('Telegram setting', samples).p95).toBeLessThanOrEqual(LATENCY_P95_MAX_MS);
    } finally {
      await a.context.close();
      await b.context.close();
    }
  });

  test('latency: create token (A /account/ -> B /account/)', async ({ browser, request }) => {
    const a = await openTab(browser, '/account/');
    const b = await openTab(browser, '/account/');
    const names: string[] = [];
    try {
      await waitLive(a.page);
      await waitLive(b.page);
      const create = a.page.getByRole('button', { name: 'Create token' });
      const samples: number[] = [];
      for (let i = 0; i < LATENCY_TRIALS; i++) {
        const name = `pw-lat-${Date.now()}-${i}`;
        names.push(name);
        await a.page.locator('#tok-name').fill(name);
        samples.push(await measure(b.page, { selector: '[data-testid="tokens-card"]', text: name }, () => create.click()));
        await expect(a.page.getByTestId('tokens-card')).toContainText(name);
        await expect(a.page.locator('#tok-name')).toHaveValue(''); // A's create finished (the field is cleared)
      }
      expect(report('create token', samples).p95).toBeLessThanOrEqual(LATENCY_P95_MAX_MS);
    } finally {
      await a.context.close();
      await b.context.close();
      const list = (await (await request.get(`${baseURL()}/admin/api/v1/tokens`)).json()) as { tokens?: { id: string; name: string }[] };
      for (const tok of list.tokens ?? []) {
        if (names.includes(tok.name)) await request.delete(`${baseURL()}/admin/api/v1/tokens/${tok.id}`, { headers: browserHeaders() });
      }
    }
  });

  test('latency: job transition (webhook -> B /history/ shows the queued job)', async ({ browser, request }) => {
    const b = await openTab(browser, '/history/');
    try {
      await waitLive(b.page);
      const samples: number[] = [];
      for (let i = 0; i < LATENCY_TRIALS; i++) {
        // A unique tag per trial: only the queued job appearing in B is asserted (RV-25).
        const tag = `pw-lat-job-${Date.now()}-${i}`;
        samples.push(
          await measure(b.page, { selector: 'main', text: tag }, async () => {
            const res = await request.post(`${baseURL()}/webhook`, { headers: { Authorization: `Bearer ${process.env.PLAYWRIGHT_WEBHOOK_SECRET}` }, data: { tag } });
            expect(res.status()).toBe(202);
          }),
        );
        await sleep(400); // the debounced history refetch of this job's later transitions settles first
      }
      expect(report('job transition', samples).p95).toBeLessThanOrEqual(LATENCY_P95_MAX_MS);
    } finally {
      await b.context.close();
    }
  });

  test('latency: service control (fake systemctl, A /apps/ -> B /apps/)', async ({ browser }) => {
    const srv = await startFakeSystemctlServer();
    try {
      const appId = await createApp(srv.api, srv.base, 'lat-svc', {});
      const open = async () => {
        const context = await browser.newContext({ storageState: srv.storage, viewport: { width: 1280, height: 800 } });
        await setPrefs(context, 'en', 'light');
        const page = await context.newPage();
        await page.goto(`${srv.base}/admin/apps/`, { waitUntil: 'load' });
        await ready(page);
        await waitLive(page);
        return { context, page };
      };
      const a = await open();
      const b = await open();
      try {
        const status = `[data-testid="service-status-${appId}"]`;
        await expect(a.page.locator(status)).toHaveAttribute('data-state', 'inactive');
        await expect(b.page.locator(status)).toHaveAttribute('data-state', 'inactive');
        const controls = a.page.getByTestId(`service-${appId}`);
        const samples: number[] = [];
        for (let i = 0; i < LATENCY_TRIALS; i++) {
          const starting = i % 2 === 0;
          const target = starting ? 'active' : 'inactive';
          const button = controls.getByRole('button', { name: starting ? 'Start' : 'Stop', exact: true });
          samples.push(await measure(b.page, { selector: status, attr: 'data-state', attrValue: target }, () => button.click()));
          await expect(a.page.locator(status)).toHaveAttribute('data-state', target);
          await expect(button).toBeEnabled();
        }
        expect(report('service control', samples).p95).toBeLessThanOrEqual(LATENCY_P95_MAX_MS);
      } finally {
        await a.context.close();
        await b.context.close();
      }
    } finally {
      await srv.stop();
    }
  });
});

/**
 * A second server for the service kind: same source, its own DB, a FAKE `systemctl` first on
 * its PATH (start/stop write the unit's state to a file, is-active reads it), the watcher
 * interval at 60 s so only the control's refresh reports the change.
 */
async function startFakeSystemctlServer() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'auto-deploy-latency-'));
  const fakeBin = path.join(dir, 'fakebin');
  const units = path.join(dir, 'units');
  fs.mkdirSync(fakeBin);
  fs.mkdirSync(units);
  fs.writeFileSync(
    path.join(fakeBin, 'systemctl'),
    [
      '#!/bin/sh',
      '# Fake systemctl of the T081 latency test: never touches systemd.',
      `dir='${units}'`,
      'verb=$1; shift',
      'rc=0',
      'for arg in "$@"; do',
      '  case "$arg" in -*) continue ;; esac',
      '  f="$dir/$arg"',
      '  case "$verb" in',
      '    is-active) s=inactive; [ -f "$f" ] && s=$(cat "$f"); echo "$s"; [ "$s" = active ] || rc=3 ;;',
      '    start|restart) echo active > "$f" ;;',
      '    stop) echo inactive > "$f" ;;',
      '  esac',
      'done',
      'exit $rc',
      '',
    ].join('\n'),
    { mode: 0o755 },
  );
  const bin = path.join(dir, 'srv');
  const repoRoot = path.resolve(__dirname, '..');
  const built = await new Promise<number | null>((resolve) => spawn('go', ['build', '-o', bin, './cmd/server'], { cwd: repoRoot, stdio: 'inherit' }).on('exit', resolve));
  expect(built, 'go build of the second server').toBe(0);
  const port = await new Promise<number>((resolve) => {
    const s = net.createServer();
    s.listen(0, '127.0.0.1', () => {
      const p = (s.address() as net.AddressInfo).port;
      s.close(() => resolve(p));
    });
  });
  const base = `http://127.0.0.1:${port}`;
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    PATH: `${fakeBin}:${process.env.PATH ?? ''}`,
    LISTEN_ADDR: `127.0.0.1:${port}`,
    DEPLOY_QUEUE_DB_PATH: path.join(dir, 'db.sqlite'),
    COOKIE_SECURE: 'false',
    WEBHOOK_SECRET: 'latency-secret-latency-secret-xx',
    DEPLOY_BINARY_PATH: path.join(dir, 'boot-bin'),
    DEPLOY_SERVICE_NAME: 'lat-boot.service',
    GITHUB_REPO: 'example/latency',
    ARTIFACT_NAME: 'latency',
    SERVICE_WATCH_INTERVAL: '60s',
  };
  delete env.ADMIN_USERNAME;
  delete env.ADMIN_PASSWORD;
  const log = fs.openSync(path.join(dir, 'server.log'), 'a');
  const proc: ChildProcess = spawn(bin, [], { cwd: dir, env, stdio: ['ignore', log, log] });
  const exited = new Promise((resolve) => proc.once('exit', resolve));
  let api: APIRequestContext | undefined;
  const stop = async () => {
    await api?.dispose();
    proc.kill('SIGTERM');
    await Promise.race([exited, sleep(5_000)]);
    if (proc.exitCode === null && proc.signalCode === null) proc.kill('SIGKILL');
    fs.closeSync(log);
    fs.rmSync(dir, { recursive: true, force: true });
  };
  try {
    api = await pwRequest.newContext({ baseURL: base, extraHTTPHeaders: { 'X-Admin-Request': 'true', 'Sec-Fetch-Site': 'same-origin', Origin: base } });
    const ctx = api;
    await expect.poll(async () => (await ctx.get('/admin/api/v1/session').catch(() => null))?.status() ?? 0, { timeout: 30_000 }).toBe(401);
    const initial = fs.readFileSync(path.join(dir, 'initial-admin-password'), 'utf8').trim();
    expect((await ctx.post('/admin/api/v1/session', { data: { username: 'admin', password: initial } })).status()).toBe(200);
    expect((await ctx.post('/admin/api/v1/account/password', { data: { currentPassword: initial, newPassword: 'latency-secret-123' } })).status()).toBe(200);
    return { base, api: ctx, storage: await ctx.storageState(), stop };
  } catch (err) {
    await stop();
    throw err;
  }
}
