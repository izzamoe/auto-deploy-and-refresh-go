// T094: smoke tests of the Next.js admin (replaces the Vite-era admin-smoke.spec.js, whose UI
// tests were skipped in T038). Same flows, on the session login and the new pages; the API-level
// checks of T038 are kept at the bottom. Texts are English: Chromium reports en-US (Q-9).
import { expect, request as pwRequest, test } from '@playwright/test';
import type { Page } from '@playwright/test';
import {
  EMPTY_STATE,
  adminURL,
  baseURL,
  browserHeaders,
  credentials,
  firstApp,
  ready,
  seedJob,
  signInThroughForm,
  watchBasicAuth,
} from './helpers';

const navigations = (page: Page) => page.evaluate(() => performance.getEntriesByType('navigation').length);

test.describe('login', () => {
  test('a wrong password shows an inline error without any dialog; the right one opens the dashboard', async ({ browser }) => {
    const context = await browser.newContext({ storageState: EMPTY_STATE });
    const page = await context.newPage();
    const seen = watchBasicAuth(page);

    await page.goto(adminURL('/login/'));
    await page.getByLabel('Username').fill(credentials().username);
    await page.getByLabel('Password').fill('definitely-wrong-password');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('alert').filter({ hasText: 'Wrong username or password.' })).toBeVisible();
    await expect(page).toHaveURL(adminURL('/login/'));

    await page.getByLabel('Password').fill('');
    await signInThroughForm(page);
    await expect(page).toHaveURL(adminURL('/'));
    await expect(page.getByTestId('dashboard')).toBeVisible();
    await expect(page.getByTestId('greeting')).toContainText(`Hello, ${credentials().username}`);

    expect(seen.dialogs).toEqual([]);
    expect(seen.challenges).toEqual([]);
    await context.close();
  });
});

test.describe('apps', () => {
  test('the apps page lists the bootstrap app and its actions', async ({ page, request }) => {
    const app = await firstApp(request);
    await page.goto(adminURL('/apps/'));
    await ready(page);
    const card = page.getByTestId(`app-card-${app.id}`);
    await expect(card).toBeVisible();
    await expect(card.getByRole('heading', { name: app.name })).toBeVisible();
    for (const name of ['Deploy', 'Edit', 'History', 'Disable', 'Delete']) {
      await expect(card.getByRole(name === 'Edit' || name === 'History' ? 'link' : 'button', { name, exact: true })).toBeVisible();
    }
  });

  test('a manual deploy settles live through the WebSocket without a reload', async ({ page, request }) => {
    const app = await firstApp(request);
    const tag = `pw-live-${Date.now()}`;
    await page.goto(adminURL('/apps/'));
    await ready(page);
    const card = page.getByTestId(`app-card-${app.id}`);
    const before = await navigations(page);

    await card.getByRole('button', { name: 'Deploy', exact: true }).click();
    const dialog = page.getByRole('dialog');
    // The release list comes from GitHub (not reachable from the test): the tag is typed by hand.
    await dialog.getByPlaceholder('Tag (e.g. v1.2.3)').fill(tag);
    await dialog.getByRole('button', { name: 'Deploy', exact: true }).click();
    await expect(page.getByText(`Deploy ${tag} queued.`)).toBeVisible();

    // The job fails quickly in the sandbox; the card must say so without a page reload.
    await expect(card.getByTestId('progress-badge')).toContainText(/Failed|Succeeded/, { timeout: 45_000 });
    expect(await navigations(page)).toBe(before);
  });

  test('toggle, edit, history link and delete update the page in place', async ({ page }) => {
    const suffix = `${Date.now()}`;
    const name = `pw-lifecycle-${suffix}`;
    await page.goto(adminURL('/apps/new/'));
    await ready(page);
    await expect(page.getByRole('heading', { name: 'New app' })).toBeVisible();
    await page.getByLabel('App name').fill(name);
    await page.getByLabel('Service name').fill(`${name}.service`);
    await page.getByLabel('Binary path').fill(`/opt/${name}`);
    await page.getByLabel('GitHub repo').fill('example/pw-lifecycle');
    await page.getByLabel('Artifact name').fill(`${name}-artifact`);

    const [create] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/admin/api/v1/apps') && r.request().method() === 'POST'),
      page.getByRole('button', { name: 'Create app' }).click(),
    ]);
    expect(create.status()).toBe(201);
    // The generated webhook secret is shown once.
    await expect(page.getByText(/shown once/)).toBeVisible();
    await page.getByRole('button', { name: 'Done', exact: true }).click();
    await expect(page).toHaveURL(adminURL('/apps/'));

    const card = page.locator('article', { has: page.getByRole('heading', { name, exact: true }) });
    await expect(card).toBeVisible();
    const here = await navigations(page);

    // toggle off and on
    await card.getByRole('button', { name: 'Disable' }).click();
    await expect(card.getByRole('button', { name: 'Enable' })).toBeVisible();
    await expect(card.getByRole('button', { name: 'Deploy', exact: true })).toHaveCount(0);
    await card.getByRole('button', { name: 'Enable' }).click();
    await expect(card.getByRole('button', { name: 'Disable' })).toBeVisible();
    expect(await navigations(page)).toBe(here);

    // edit keeps the secret and saves the new artifact name
    await card.getByRole('link', { name: 'Edit' }).click();
    await expect(page.getByRole('heading', { name: 'Edit app' })).toBeVisible();
    await page.getByLabel('Artifact name').fill(`${name}-artifact-2`);
    const [update] = await Promise.all([
      page.waitForResponse((r) => /\/admin\/api\/v1\/apps\/[^/]+$/.test(r.url()) && r.request().method() === 'PUT'),
      page.getByRole('button', { name: 'Save changes' }).click(),
    ]);
    expect(update.status()).toBe(200);
    await expect(page.getByText('App updated.')).toBeVisible();
    await expect(page).toHaveURL(/\/admin\/apps\/edit\/\?id=/); // stays on the form (no redirect, no reload)
    await page.getByRole('link', { name: 'Back to apps' }).click();
    await expect(page).toHaveURL(adminURL('/apps/'));
    await expect(card).toContainText(`${name}-artifact-2`);

    // history link
    await card.getByRole('link', { name: 'History' }).click();
    await expect(page).toHaveURL(/\/admin\/history\/\?appId=/);
    await expect(page.getByRole('heading', { name: 'History' }).first()).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(adminURL('/apps/'));

    // delete needs a confirmation
    await card.getByRole('button', { name: 'Delete' }).click();
    await expect(page.getByRole('dialog')).toContainText('This cannot be undone.');
    await page.getByRole('dialog').getByRole('button', { name: 'Yes, delete' }).click();
    await expect(page.getByRole('heading', { name, exact: true })).toHaveCount(0);
  });

  test('the new-app form keeps the typed values and shows the server error per field', async ({ page }) => {
    await page.goto(adminURL('/apps/new/'));
    await ready(page);
    // the bootstrap app already owns this binary path / service name -> a conflict from the server
    await page.getByLabel('App name').fill('pw-conflict');
    await page.getByLabel('Service name').fill('playwright-bootstrap.service');
    await page.getByLabel('Binary path').fill('/tmp/playwright-bootstrap-binary');
    await page.getByLabel('GitHub repo').fill('example/pw-conflict');
    await page.getByLabel('Artifact name').fill('pw-conflict-artifact');
    const [res] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/admin/api/v1/apps') && r.request().method() === 'POST'),
      page.getByRole('button', { name: 'Create app' }).click(),
    ]);
    expect(res.status()).toBeGreaterThanOrEqual(400);
    await expect(page.getByTestId('form-errors')).toBeVisible();
    await expect(page.getByLabel('App name')).toHaveValue('pw-conflict');
    await expect(page.getByLabel('Service name')).toHaveValue('playwright-bootstrap.service');
    await expect(page).toHaveURL(adminURL('/apps/new/'));
  });
});

test.describe('history', () => {
  test('deep links survive a reload and back/forward; a retry adds a second row in place', async ({ page, request }) => {
    const app = await firstApp(request);
    const tag = await seedJob(request, 'pw-history');
    await page.goto(adminURL(`/history/?appId=${encodeURIComponent(app.id)}`));
    await ready(page);

    const rows = () => page.locator('tr[data-job-id]').filter({ hasText: tag });
    await expect(rows().first()).toBeVisible({ timeout: 30_000 });
    await expect(rows().first()).toHaveAttribute('data-status', /failed|succeeded/, { timeout: 45_000 });

    await page.reload();
    await expect(page).toHaveURL(new RegExp(`/admin/history/\\?appId=${app.id}`));
    await expect(rows().first()).toBeVisible();

    await page.goto(adminURL('/apps/'));
    await page.goBack();
    await expect(rows().first()).toBeVisible();

    const before = await navigations(page);
    await page.getByRole('button', { name: `Retry deploy ${tag}` }).click();
    await expect(page.getByText('Retry queued.')).toBeVisible();
    await expect(rows()).toHaveCount(2, { timeout: 45_000 });
    expect(await navigations(page)).toBe(before);
  });
});

test.describe('live events', () => {
  test('malformed frames are ignored and a failed frame is rendered on the card', async ({ page, request }) => {
    const app = await firstApp(request);
    // The card shows live progress of the job its app lists as the last one: make sure there is one.
    await seedJob(request, 'pw-ws');
    let jobId = '';
    await expect
      .poll(async () => {
        const list = await (await request.get(`${baseURL()}/admin/api/v1/apps`)).json();
        jobId = list.apps.find((a: { id: string }) => a.id === app.id)?.lastJobId ?? '';
        return jobId;
      })
      .not.toBe('');
    let sendToPage: ((data: string) => void) | undefined;
    await page.routeWebSocket(/\/admin\/events\//, (ws) => {
      ws.connectToServer(); // forward everything; the test only ADDS frames
      sendToPage = (data) => ws.send(data);
    });

    await page.goto(adminURL('/apps/'));
    await ready(page);
    const card = page.getByTestId(`app-card-${app.id}`);
    await expect.poll(() => sendToPage !== undefined).toBe(true);
    const badge = card.getByTestId('progress-badge');
    await expect(badge).toContainText(/Failed|Succeeded/, { timeout: 45_000 }); // the seeded job settled
    const original = await badge.textContent();

    sendToPage!('not-json-at-all');
    sendToPage!('{"protocol":"3"');
    await page.waitForTimeout(300);
    await expect(badge).toHaveText(original ?? '');

    const ts = new Date().toISOString();
    // Protocol 3 (002 T050, RV-2 d): compact progress `v:"3"`; job_status is a topic event with
    // seq/topic (a seq far above the server's so the per-topic dedup accepts it).
    sendToPage!(JSON.stringify({ v: '3', t: 'p', i: 'evt-900', ts, a: app.id, j: jobId, ph: 'downloading', st: 'in_progress', pct: 50, db: 524288, tb: 1048576, bps: 524288, msg: '' }));
    await expect(card.getByTestId('progress-live')).toBeVisible();
    await expect(card).toContainText('Downloading the release artifact');
    await expect(card).toContainText('512');

    sendToPage!(JSON.stringify({ protocol: '3', id: 'evt-901', seq: 2_000_000_000, topic: 'jobs', type: 'job_status', ts: new Date().toISOString(), appId: app.id, jobId, payload: { status: 'failed', previousStatus: 'in_progress', tag: 'v0.0.0-synthetic' } }));
    await expect(card.getByTestId('progress-live')).toHaveCount(0);
  });
});

// ---------------------------------------------------------------------------------------
// API-level smoke (FR-136, PL-1), kept from T038: global-setup logged in through
// POST /admin/api/v1/session, replaced the generated password and saved the session.
// ---------------------------------------------------------------------------------------

test.describe('v1 API', () => {
  test('signed-in session reaches the v1 API: login -> GET /apps 200', async ({ request }) => {
    const session = await request.get(`${baseURL()}/admin/api/v1/session`);
    expect(session.status()).toBe(200);
    const body = await session.json();
    expect(body.username).toBe(credentials().username);
    expect(body.mustChangePassword).toBe(false);

    const apps = await request.get(`${baseURL()}/admin/api/v1/apps`);
    expect(apps.status()).toBe(200);
    expect(Array.isArray((await apps.json()).apps)).toBe(true);
  });

  test('without a session the v1 API answers 401 JSON and never a Basic challenge', async () => {
    const anonymous = await pwRequest.newContext({ storageState: EMPTY_STATE });
    try {
      const res = await anonymous.get(`${baseURL()}/admin/api/v1/apps`);
      expect(res.status()).toBe(401);
      expect(res.headers()['www-authenticate']).toBeUndefined();
      expect(res.headers()['content-type']).toContain('application/json');
      const body = await res.json();
      expect(body.status).toBe('error');
      expect(body.code).toBe('session_expired');
    } finally {
      await anonymous.dispose();
    }
  });

  test('the admin password is not a Basic credential on the API', async () => {
    const anonymous = await pwRequest.newContext({ storageState: EMPTY_STATE });
    try {
      const { username, password } = credentials();
      const basic = Buffer.from(`${username}:${password}`).toString('base64');
      const res = await anonymous.get(`${baseURL()}/admin/api/v1/apps`, { headers: { Authorization: `Basic ${basic}` } });
      expect(res.status()).toBe(401);
    } finally {
      await anonymous.dispose();
    }
  });

  test('cookie-authenticated mutations are refused without the CSRF header', async ({ request }) => {
    const refused = await request.post(`${baseURL()}/admin/api/v1/tokens`, { data: { name: 'smoke' }, headers: { 'Content-Type': 'application/json' } });
    expect(refused.status()).toBe(403);
    expect((await refused.json()).code).toBe('csrf_rejected');

    const ok = await request.post(`${baseURL()}/admin/api/v1/tokens`, { data: { name: 'smoke' }, headers: browserHeaders() });
    expect(ok.status()).toBe(201);
    const created = await ok.json();
    expect(created.token).toMatch(/^adt_/);
    const revoke = await request.delete(`${baseURL()}/admin/api/v1/tokens/${created.id}`, { headers: browserHeaders() });
    expect(revoke.status()).toBe(204);
  });

  test('a webhook-queued deploy shows up in the v1 history', async ({ request }) => {
    const tag = await seedJob(request, 'pw-api');
    const history = await request.get(`${baseURL()}/admin/api/v1/history`);
    expect(history.status()).toBe(200);
    expect((await history.json()).history.some((job: { tag: string }) => job.tag === tag)).toBe(true);
  });
});
