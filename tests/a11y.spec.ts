// T094 a11y: axe-core on every page, light and dark theme, Indonesian and English: 0 serious and
// 0 critical violations (NFR-009, FR-106..FR-108). Moderate/minor findings are logged, not failed.
import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';
import { EMPTY_STATE, STATIC_ROUTES, adminURL, firstApp, ready, setPrefs } from './helpers';
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
