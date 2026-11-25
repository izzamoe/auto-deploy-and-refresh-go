// T094 session: an expired, deleted or revoked session ends on the login page (with `next`) and
// brings you back to where you were; never a Basic-auth dialog or a `www-authenticate`
// header (FR-108/FR-109, B50, K1). Also: the event socket stays up while idle (the Hertz
// Read/WriteTimeout is 45 s, "Tidak terverifikasi" #10) and a browser that still stores old HTTP
// Basic credentials is not challenged.
import { expect, test } from '@playwright/test';
import type { Browser, Page } from '@playwright/test';
import { EMPTY_STATE, STORAGE_STATE, adminURL, baseURL, browserHeaders, credentials, ready, signInThroughForm, watchBasicAuth } from './helpers';

async function signedInContext(browser: Browser) {
  // A session of its own: revoking it must not end the shared one used by the other specs.
  const context = await browser.newContext({ storageState: EMPTY_STATE });
  const res = await context.request.post(`${baseURL()}/admin/api/v1/session`, { data: credentials(), headers: browserHeaders() });
  expect(res.status()).toBe(200);
  return context;
}

async function expectLoginWithNext(page: Page, nextPath: string) {
  await expect(page).toHaveURL(/\/admin\/login\//);
  expect(new URL(page.url()).searchParams.get('next')).toBe(nextPath);
}

test.describe('session end', () => {
  test('a deleted cookie sends the user to the login page and back to the page they asked for', async ({ browser }) => {
    const context = await signedInContext(browser);
    const page = await context.newPage();
    const seen = watchBasicAuth(page);

    await page.goto(adminURL('/history/'));
    await ready(page);
    await context.clearCookies();

    await page.goto(adminURL('/history/'));
    await expectLoginWithNext(page, '/admin/history/');
    await signInThroughForm(page);
    await expect(page).toHaveURL(adminURL('/history/'));
    await ready(page);

    expect(seen.dialogs).toEqual([]);
    expect(seen.challenges).toEqual([]);
    await context.close();
  });

  test('a session revoked while the page is open: the next action ends on the login page, no dialog', async ({ browser }) => {
    const context = await signedInContext(browser);
    const page = await context.newPage();
    const seen = watchBasicAuth(page);

    await page.goto(adminURL('/'));
    await ready(page);
    // Revoke this very session through the API (the cookie of the context is sent with it).
    const revoke = await context.request.delete(`${baseURL()}/admin/api/v1/session`, { headers: browserHeaders() });
    expect(revoke.status()).toBe(204);

    // The server closes the event socket (4401) as soon as the revocation is seen, which may
    // already have moved the page to the login form; otherwise the next action does (a 401).
    const apps = page.getByRole('link', { name: 'Apps', exact: true });
    if (await apps.isVisible()) await apps.click({ timeout: 2_000 }).catch(() => undefined);
    await expect(page).toHaveURL(/\/admin\/login\//, { timeout: 20_000 });
    expect(['/admin/', '/admin/apps/']).toContain(new URL(page.url()).searchParams.get('next'));
    await expect(page.getByLabel('Username')).toBeVisible();

    expect(seen.dialogs).toEqual([]);
    expect(seen.challenges).toEqual([]);
    await context.close();
  });

  test('a revoked cookie that the browser still sends is refused with JSON, never a Basic challenge', async ({ browser }) => {
    const context = await signedInContext(browser);
    const cookies = await context.cookies();
    const cookie = cookies.find((c) => c.name.endsWith('admin_session'));
    expect(cookie).toBeDefined();
    const revoke = await context.request.delete(`${baseURL()}/admin/api/v1/session`, { headers: browserHeaders() });
    expect(revoke.status()).toBe(204);

    const stale = await browser.newContext({ storageState: { cookies: [cookie!], origins: [] } });
    const page = await stale.newPage();
    const seen = watchBasicAuth(page);
    const res = await stale.request.get(`${baseURL()}/admin/api/v1/apps`);
    expect(res.status()).toBe(401);
    expect(res.headers()['www-authenticate']).toBeUndefined();

    await page.goto(adminURL('/apps/'));
    await expectLoginWithNext(page, '/admin/apps/');
    expect(seen.dialogs).toEqual([]);
    expect(seen.challenges).toEqual([]);
    await stale.close();
    await context.close();
  });

  test('without any session an inner route lands on the login page with `next`, and a dot-segment next is not followed', async ({ browser }) => {
    const context = await browser.newContext({ storageState: EMPTY_STATE });
    const page = await context.newPage();
    const seen = watchBasicAuth(page);
    await page.goto(adminURL('/settings/github/'));
    await expectLoginWithNext(page, '/admin/settings/github/');

    await page.goto(adminURL('/login/?next=%2Fadmin%2F..%2Fevil'));
    await signInThroughForm(page);
    await expect(page).toHaveURL(adminURL('/'));
    expect(seen.dialogs).toEqual([]);
    await context.close();
  });
});

test.describe('stored HTTP Basic credentials', () => {
  test('a browser with old Basic credentials and a valid cookie gets no dialog and no challenge', async ({ browser }) => {
    const { username, password } = credentials();
    const context = await browser.newContext({ storageState: STORAGE_STATE, httpCredentials: { username, password } });
    const page = await context.newPage();
    const seen = watchBasicAuth(page);
    await page.goto(adminURL('/'));
    await expect(page.getByTestId('dashboard')).toBeVisible();
    await page.goto(adminURL('/apps/'));
    await ready(page);
    expect(seen.dialogs).toEqual([]);
    expect(seen.challenges).toEqual([]);
    await context.close();
  });

  test('a browser with old Basic credentials and NO session is sent to the login form, not challenged', async ({ browser }) => {
    const { username, password } = credentials();
    const context = await browser.newContext({ storageState: EMPTY_STATE, httpCredentials: { username, password } });
    const page = await context.newPage();
    const seen = watchBasicAuth(page);
    await page.goto(adminURL('/apps/'));
    await expectLoginWithNext(page, '/admin/apps/');
    expect(seen.dialogs).toEqual([]);
    expect(seen.challenges).toEqual([]);
    await context.close();
  });
});

test.describe('event socket', () => {
  test('stays connected, with no periodic reconnect, while the page is idle for 90 s', async ({ page }) => {
    const sockets: { url: string; closed: boolean }[] = [];
    page.on('websocket', (ws) => {
      if (!/\/admin\/events\//.test(ws.url())) return;
      const entry = { url: ws.url(), closed: false };
      sockets.push(entry);
      ws.on('close', () => {
        entry.closed = true;
      });
    });

    await page.goto(adminURL('/'));
    await ready(page);
    await expect(page.getByRole('status').filter({ hasText: 'Connected' })).toBeAttached();
    expect(sockets).toHaveLength(1);

    await page.waitForTimeout(95_000); // longer than the 45 s server timeouts and two 30 s heartbeats

    expect(sockets, 'no reconnect while idle').toHaveLength(1);
    expect(sockets[0].closed).toBe(false);
    await expect(page.getByRole('status').filter({ hasText: 'Connected' })).toBeAttached();
  });
});
