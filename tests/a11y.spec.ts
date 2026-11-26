// T094 a11y: axe-core on every page, light and dark theme, Indonesian and English: 0 serious and
// 0 critical violations (NFR-009, FR-106..FR-108). Moderate/minor findings are logged, not failed.
// 002 T086 adds the realtime UI: the edit conflict notice, the live/paused system log and the
// "reconnecting" connection indicator (NFR-009, FR-022, FR-024).
import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import type { Page, WebSocketRoute } from '@playwright/test';
import { EMPTY_STATE, STATIC_ROUTES, adminURL, baseURL, browserHeaders, firstApp, ready, setPrefs } from './helpers';
import type { Locale, Theme } from './helpers';

// Dialogs fade in; axe would read the blended colours of a half-transparent frame.
test.use({ reducedMotion: 'reduce' });

const THEMES: Theme[] = ['light', 'dark'];
const LOCALES: Locale[] = ['id', 'en'];

async function scan(page: Page, label: string) {
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  const blocking = result.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
  const other = result.violations.filter((v) => v.impact !== 'serious' && v.impact !== 'critical');
  if (other.length > 0) console.log(`[a11y] ${label}: non-blocking -> ${other.map((v) => `${v.id}(${v.impact})`).join(', ')}`);
  const summary = blocking.map((v) => `${v.id} (${v.impact}): ${v.help}\n   ${v.nodes.slice(0, 3).map((n) => n.target.join(' ') + ' :: ' + (n.failureSummary ?? '').replace(/\n/g, ' ')).join('\n   ')}`);
  expect(summary, `${label}: serious/critical violations`).toEqual([]);
}

for (const locale of LOCALES) {
  for (const theme of THEMES) {
    test.describe(`${locale} / ${theme}`, () => {
      for (const route of STATIC_ROUTES) {
        test(`${route}`, async ({ page, context }) => {
          await setPrefs(context, locale, theme);
          await page.goto(adminURL(route));
          await ready(page);
          if (route === '/') await expect(page.getByTestId('dashboard')).toBeVisible();
          await page.waitForTimeout(400); // lazy chunks (chart) and the first paint of fonts
          expect(await page.evaluate(() => document.documentElement.classList.contains('dark'))).toBe(theme === 'dark');
          expect(await page.evaluate(() => document.documentElement.lang)).toBe(locale);
          await scan(page, `${locale}/${theme} ${route}`);
        });
      }

      test('/apps/edit/?id=<id>', async ({ page, context, request }) => {
        const app = await firstApp(request);
        await setPrefs(context, locale, theme);
        await page.goto(adminURL(`/apps/edit/?id=${encodeURIComponent(app.id)}`));
        await expect(page.locator('#f-name')).toHaveValue(app.name);
        await scan(page, `${locale}/${theme} /apps/edit/`);
      });

      test('/login/ (signed out)', async ({ browser }) => {
        const context = await browser.newContext({ storageState: EMPTY_STATE });
        await setPrefs(context, locale, theme);
        const page = await context.newPage();
        await page.goto(adminURL('/login/'));
        await expect(page.locator('form')).toBeVisible();
        await scan(page, `${locale}/${theme} /login/`);
        await context.close();
      });

      test('dialogs: the deploy and delete dialogs of an app card', async ({ page, context, request }) => {
        const app = await firstApp(request);
        await setPrefs(context, locale, theme);
        await page.goto(adminURL('/apps/'));
        await ready(page);
        const card = page.getByTestId(`app-card-${app.id}`);
        await card.getByRole('button', { name: 'Deploy', exact: true }).click();
        await expect(page.getByRole('dialog')).toBeVisible();
        await scan(page, `${locale}/${theme} deploy dialog`);
        await page.keyboard.press('Escape');
        await expect(page.getByRole('dialog')).toHaveCount(0);
        await card.getByRole('button', { name: /^(Delete|Hapus)$/ }).click();
        await expect(page.getByRole('dialog')).toBeVisible();
        await scan(page, `${locale}/${theme} delete dialog`);
      });

      // 002 T086 (NFR-009, FR-022, FR-024): the new realtime UI states.
      test('edit conflict notice (the app changed in another session, Q-10)', async ({ page, context, request }) => {
        const name = `pw-a11y-conflict-${Date.now()}`;
        const fields = { binaryPath: `/opt/${name}/app`, serviceName: `${name}.service`, githubRepo: `example/${name}`, artifactName: `${name}-art` };
        const created = await request.post(`${baseURL()}/admin/api/v1/apps`, { data: { name, ...fields }, headers: browserHeaders() });
        expect([200, 201]).toContain(created.status());
        const body = await created.json();
        const id: string = body.app?.id ?? body.id;
        try {
          await setPrefs(context, locale, theme);
          await page.goto(adminURL(`/apps/edit/?id=${encodeURIComponent(id)}`));
          await expect(page.locator('#f-name')).toHaveValue(name);
          await expect(page.getByTestId('connection-status')).toHaveAttribute('data-connection', 'live', { timeout: 10_000 });
          const changed = await request.put(`${baseURL()}/admin/api/v1/apps/${id}`, { data: { name: `${name}-x`, ...fields }, headers: browserHeaders() });
          expect(changed.status()).toBe(200);
          await expect(page.getByTestId('edit-conflict')).toBeVisible();
          await scan(page, `${locale}/${theme} edit conflict notice`);
        } finally {
          await request.delete(`${baseURL()}/admin/api/v1/apps/${id}`, { headers: browserHeaders() });
        }
      });

      test('/logs/ live mode, then paused (Q-5)', async ({ page, context }) => {
        await setPrefs(context, locale, theme);
        await page.goto(adminURL('/logs/'));
        await ready(page);
        await expect(page.getByTestId('logs-live-status')).toBeVisible();
        await scan(page, `${locale}/${theme} /logs/ live`);
        await page.getByTestId('logs-pause').click();
        await expect(page.getByTestId('logs-pause')).toHaveAttribute('aria-pressed', 'true');
        await scan(page, `${locale}/${theme} /logs/ paused`);
      });

      test('connection indicator "reconnecting" (FR-024)', async ({ page, context }) => {
        let sockets = 0;
        let first: WebSocketRoute | undefined;
        await page.routeWebSocket(/\/admin\/events\//, (ws) => {
          sockets++;
          if (sockets === 1) {
            first = ws;
            ws.connectToServer(); // the first socket is the real one
          }
          // later sockets are held open and silent: no hello, so the panel keeps reconnecting
        });
        await setPrefs(context, locale, theme);
        await page.goto(adminURL('/apps/'));
        await ready(page);
        const status = page.getByTestId('connection-status');
        await expect(status).toHaveAttribute('data-connection', 'live', { timeout: 10_000 });
        await first!.close({ code: 1012, reason: 'a11y test: server restart' });
        await expect(status).toHaveAttribute('data-connection', 'reconnecting');
        await scan(page, `${locale}/${theme} connection reconnecting`);
      });

      test('mobile "More" dialog (375 px)', async ({ page, context }) => {
        await setPrefs(context, locale, theme);
        await page.setViewportSize({ width: 375, height: 800 });
        await page.goto(adminURL('/'));
        await ready(page);
        await page.getByRole('navigation').getByRole('button', { name: /^(Lainnya|More)$/ }).click();
        await expect(page.getByRole('dialog')).toBeVisible();
        await scan(page, `${locale}/${theme} more dialog`);
      });
    });
  }
}

test.describe('keyboard', () => {
  test('the skip link is the first tab stop and moves focus to the main landmark', async ({ page, context }) => {
    await setPrefs(context, 'en', 'light');
    await page.goto(adminURL('/apps/'));
    await ready(page);
    await page.keyboard.press('Tab');
    const skip = page.getByRole('link', { name: 'Skip to main content' });
    await expect(skip).toBeFocused();
    await expect(skip).toBeVisible();
    await page.keyboard.press('Enter');
    await expect(page.locator('#main-content')).toBeFocused();
  });

  test('every sidebar icon control has an accessible name and a tooltip on focus', async ({ page, context }) => {
    await setPrefs(context, 'en', 'light');
    await page.goto(adminURL('/'));
    await ready(page);
    const nav = page.getByRole('navigation', { name: 'Main navigation' });
    for (const link of await nav.getByRole('link').all()) {
      const name = await link.getAttribute('aria-label');
      expect(name, 'aria-label').toBeTruthy();
      await link.focus();
      await expect(page.getByTestId('rail-tip')).toHaveText(name!);
    }
  });
});
