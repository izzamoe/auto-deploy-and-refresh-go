// 002 realtime event-driven admin (project `realtime`). Written RED first (T010-T013, T018):
// each test proves a waste or a desync of the pre-002 panel and turns GREEN in STOP-6.
//   idle      (T010, FR-001, SC-001): an idle tab sends no REST request on any route
//   two tabs  (T011, FR-004, SC-004): a change in tab A shows in tab B without REST in B
//   reconnect (T012, FR-011, SC-003): 30 s offline then online refetches nothing
//   focus     (T013, FR-014, SC-002): a focus event refetches nothing while live
//   topics    (T018, FR-015, SC-011): /account/ (no `jobs` topic) receives no progress frame
//   navigate  (T052, FR-014, RV-8): leave /apps/, change it elsewhere, come back -> shown with <= 1 REST
import { expect, test } from '@playwright/test';
import type { Browser, BrowserContext, Page } from '@playwright/test';
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
