// T094 routing: every inner route of the static export can be opened directly and reloaded
// (FR-108/NFR-005, B50): no response >= 400 for the document, its assets or its RSC/prefetch
// requests, the page renders, and a refresh keeps the same page.
import { expect, test } from '@playwright/test';
import type { Page, Response } from '@playwright/test';
import { STATIC_ROUTES, adminURL, firstApp, ready, watchBasicAuth } from './helpers';

/** Data endpoints whose answer depends on the host, not on routing (journald, GitHub). */
const HOST_DEPENDENT = [/\/admin\/api\/v1\/logs\/system/, /\/admin\/api\/v1\/apps\/[^/]+\/releases/, /\/admin\/api\/v1\/apps\/[^/]+\/service/];

function collectErrors(page: Page): string[] {
  const bad: string[] = [];
  page.on('response', (r: Response) => {
    if (r.status() >= 400 && !HOST_DEPENDENT.some((re) => re.test(r.url()))) bad.push(`${r.status()} ${r.request().method()} ${r.url()}`);
  });
  return bad;
}

test.describe('direct load and reload of every route', () => {
  for (const route of STATIC_ROUTES) {
    test(`${route}: opens, reloads, 0 responses >= 400`, async ({ page }) => {
      const bad = collectErrors(page);
      const seen = watchBasicAuth(page);

      await page.goto(adminURL(route));
      await ready(page);
      await expect(page.locator('main').first()).toBeVisible();
      await page.reload();
      await ready(page);
      await expect(page.locator('main').first()).toBeVisible();
      await expect(page).toHaveURL(adminURL(route));

      expect(bad).toEqual([]);
      expect(seen.dialogs).toEqual([]);
      expect(seen.challenges).toEqual([]);
    });
  }

  test('/apps/edit/?id=<id>: a deep link with a query reloads', async ({ page, request }) => {
    const app = await firstApp(request);
    const bad = collectErrors(page);
    const url = adminURL(`/apps/edit/?id=${encodeURIComponent(app.id)}`);
    await page.goto(url);
    await expect(page.getByLabel('App name')).toHaveValue(app.name);
    await page.reload();
    await expect(page.getByLabel('App name')).toHaveValue(app.name);
    await expect(page).toHaveURL(url);
    expect(bad).toEqual([]);
  });

  test('/history/?appId=<id>: a deep link with a query reloads', async ({ page, request }) => {
    const app = await firstApp(request);
    const bad = collectErrors(page);
    const url = adminURL(`/history/?appId=${encodeURIComponent(app.id)}`);
    await page.goto(url);
    await ready(page);
    await page.reload();
    await ready(page);
    await expect(page).toHaveURL(url);
    expect(bad).toEqual([]);
  });

  test('a path without the trailing slash reaches the same page', async ({ page }) => {
    await page.goto(adminURL('/apps'));
    await ready(page);
    await expect(page).toHaveURL(adminURL('/apps/'));
    await expect(page.getByRole('heading', { name: 'Apps', exact: true })).toBeVisible();
  });

  test('client-side navigation by the sidebar never reloads the document', async ({ page }) => {
    const bad = collectErrors(page);
    await page.goto(adminURL('/'));
    await ready(page);
    const before = await page.evaluate(() => performance.getEntriesByType('navigation').length);
    await page.getByRole('link', { name: 'History', exact: true }).click();
    await expect(page).toHaveURL(adminURL('/history/'));
    await page.getByRole('link', { name: 'Telegram', exact: true }).click();
    await expect(page).toHaveURL(adminURL('/settings/telegram/'));
    await page.getByRole('link', { name: 'Dashboard', exact: true }).click();
    await expect(page).toHaveURL(adminURL('/'));
    expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(before);
    expect(bad).toEqual([]);
  });

  test('an unknown address shows the not-found page, not a blank screen or a login redirect', async ({ page }) => {
    await page.goto(adminURL('/does-not-exist/'));
    await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Back to the dashboard' })).toBeVisible();
  });

  test('an old hash address (#/history?appId=...) is redirected to the new route', async ({ page, request }) => {
    const app = await firstApp(request);
    await page.goto(adminURL(`/#/history?appId=${encodeURIComponent(app.id)}`));
    await expect(page).toHaveURL(new RegExp(`/admin/history/\\?appId=${app.id}`));
  });

  test('assets and the logo are served from /admin/ without a CDN', async ({ page }) => {
    const external: string[] = [];
    page.on('request', (r) => {
      const u = new URL(r.url());
      if (u.hostname !== '127.0.0.1' && u.hostname !== 'localhost') external.push(r.url());
    });
    await page.goto(adminURL('/'));
    await ready(page);
    expect(external).toEqual([]);
    const logo = await page.request.get(adminURL('/logo.svg'));
    expect(logo.status()).toBe(200);
  });
});
