// T095 (Q-25): visual + responsive checks, local. Three layers:
//  1. snapshots (dashboard, apps, form, history, login x light/dark x id/en x 1280/375) compared
//     against a baseline that is NOT committed: `--update-snapshots` first, then plain run;
//  2. layout assertions with data on screen (no horizontal scroll at 360/375/768/1280/1440,
//     the page scrolls to its end, balanced dashboard columns, scrollable sidebar on a short
//     window, readable chart axis, no clipped subtitle);
//  3. optional full-page captures at 360/768/1280/1440 into $SHOTS_DIR (not committed).
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';
import { EMPTY_STATE, adminURL, baseURL, firstApp, ready, seedJob, setPrefs } from './helpers';
import type { Locale, Theme } from './helpers';

const THEMES: Theme[] = ['light', 'dark'];
const LOCALES: Locale[] = ['id', 'en'];
const SNAP_WIDTHS = [1280, 375];
const ALL_WIDTHS = [360, 768, 1280, 1440];

interface Shot {
  name: string;
  route: string;
  signedOut?: boolean;
  /** CSS selectors of content that changes between runs (clock-dependent text). */
  mask?: string[];
}

const SHOTS: Shot[] = [
  { name: 'dashboard', route: '/', mask: ['[data-testid="account-card"]', 'time'] },
  { name: 'apps', route: '/apps/', mask: ['time'] },
  { name: 'form', route: '/apps/new/' },
  { name: 'history', route: '/history/', mask: ['time'] },
  { name: 'login', route: '/login/', signedOut: true },
];

async function open(page: Page, shot: Shot) {
  await page.goto(adminURL(shot.route));
  if (shot.signedOut) await expect(page.locator('form')).toBeVisible();
  else await ready(page);
  await page.waitForTimeout(500); // lazy chart chunk, font swap
}

const horizontalOverflow = (page: Page) => page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, innerWidth: window.innerWidth }));

// ---------------------------------------------------------------------------------------
// 1. snapshots (deterministic: the server of a run starts with an empty database)
// ---------------------------------------------------------------------------------------
for (const shot of SHOTS) {
  for (const theme of THEMES) {
    for (const locale of LOCALES) {
      for (const width of SNAP_WIDTHS) {
        test(`snapshot ${shot.name} ${theme} ${locale} ${width}px`, async ({ browser }) => {
          const context = await browser.newContext({
            viewport: { width, height: 900 },
            // An explicit `storageState: undefined` would DROP the configured signed-in session
            // (the T095 snapshots were the login page); only override it when signed out.
            ...(shot.signedOut ? { storageState: EMPTY_STATE } : {}),
            reducedMotion: 'reduce',
          });
          await setPrefs(context, locale, theme);
          const page = await context.newPage();
          await open(page, shot);
          const { scrollWidth, innerWidth } = await horizontalOverflow(page);
          expect(scrollWidth, `${shot.name}: horizontal scroll`).toBeLessThanOrEqual(innerWidth);
          await expect(page).toHaveScreenshot(`${shot.name}-${theme}-${locale}-${width}.png`, {
            fullPage: true,
            mask: (shot.mask ?? []).map((s) => page.locator(s)),
          });
          await context.close();
        });
      }
    }
  }
}

// ---------------------------------------------------------------------------------------
// 2. layout assertions with data (seeded deploys) + 3. captures
// ---------------------------------------------------------------------------------------
test.describe('layout with data', () => {
  test.beforeAll(async ({ request }) => {
    for (let i = 0; i < 3; i++) await seedJob(request, `pw-vis${i}`);
    // wait until the deploys settled so the chart/history have stable content
    await expect
      .poll(async () => {
        const res = await request.get(`${baseURL()}/admin/api/v1/history`);
        const body = await res.json();
        return body.history.filter((j: { status: string }) => j.status === 'failed' || j.status === 'succeeded').length;
      }, { timeout: 60_000 })
      .toBeGreaterThanOrEqual(3);
  });

  for (const width of ALL_WIDTHS) {
    for (const theme of THEMES) {
      for (const locale of LOCALES) {
        test(`no horizontal scroll, page reaches its end, captures: ${width}px ${theme} ${locale}`, async ({ browser, request }) => {
          const app = await firstApp(request);
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
          await setPrefs(context, locale, theme);
          const page = await context.newPage();
          const shotsDir = process.env.SHOTS_DIR;
          if (shotsDir) mkdirSync(shotsDir, { recursive: true });

          const pages: [string, string][] = [
            ['dashboard', '/'],
            ['apps', '/apps/'],
            ['form', '/apps/new/'],
            ['edit', `/apps/edit/?id=${encodeURIComponent(app.id)}`],
            ['history', '/history/'],
            ['github', '/settings/github/'],
            ['telegram', '/settings/telegram/'],
            ['logs', '/logs/'],
            ['account', '/account/'],
          ];
          for (const [name, route] of pages) {
            await page.goto(adminURL(route));
            await ready(page);
            await page.waitForTimeout(400);

            const { scrollWidth, innerWidth } = await horizontalOverflow(page);
            expect(scrollWidth, `${name} @${width}: horizontal scroll`).toBeLessThanOrEqual(innerWidth);

            // The last element of the main content can be scrolled into the viewport.
            await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
            const bottom = await page.evaluate(() => {
              const main = document.querySelector('main');
              const last = main?.lastElementChild as HTMLElement | null;
              return last ? last.getBoundingClientRect().bottom : 0;
            });
            const viewport = await page.evaluate(() => window.innerHeight);
            expect(bottom, `${name} @${width}: end of the page reachable`).toBeLessThanOrEqual(viewport + 1);
            await page.evaluate(() => window.scrollTo(0, 0));

            if (shotsDir) {
              await page.screenshot({ path: path.join(shotsDir, `${name}-${width}-${theme}-${locale}.png`), fullPage: true });
            }
          }
          await context.close();
        });
      }
    }
  }

  test('dashboard at 1440x1000: both columns end at the same height and nothing is cut off', async ({ browser }) => {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
    await setPrefs(context, 'en', 'light');
    const page = await context.newPage();
    await page.goto(adminURL('/'));
    await ready(page);
    await page.waitForTimeout(500);

    const boxes = await page.evaluate(() => {
      const rect = (id: string) => {
        const r = document.querySelector(`[data-testid="${id}"]`)!.getBoundingClientRect();
        return { top: r.top + window.scrollY, bottom: r.bottom + window.scrollY, right: r.right, left: r.left };
      };
      return { recent: rect('recent-deploys'), security: rect('security-card'), activity: rect('activity') };
    });
    expect(Math.abs(boxes.recent.bottom - boxes.security.bottom), 'left and right column end together').toBeLessThanOrEqual(2);

    // The account button of the right rail is fully reachable by scrolling the page.
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    const button = page.getByTestId('security-card').getByRole('link', { name: 'Open account' });
    await expect(button).toBeInViewport({ ratio: 1 });
    await context.close();
  });

  test('the chart keeps its last date label inside the chart at 360, 780 and 1440 px, in 12 px text', async ({ browser }) => {
    for (const width of [360, 780, 1440]) {
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
      await setPrefs(context, 'en', 'light');
      const page = await context.newPage();
      await page.goto(adminURL('/'));
      await ready(page);
      await expect(page.getByTestId('activity').locator('svg[role="img"]')).toBeVisible();
      const check = await page.evaluate(() => {
        const svg = document.querySelector('[data-testid="activity"] svg[role="img"]') as SVGSVGElement;
        const box = svg.getBoundingClientRect();
        const texts = [...svg.querySelectorAll('text')].map((t) => ({ r: t.getBoundingClientRect(), size: parseFloat(getComputedStyle(t).fontSize), s: t.textContent }));
        return {
          right: Math.max(...texts.map((t) => t.r.right)) - box.right,
          left: box.left - Math.min(...texts.map((t) => t.r.left)),
          minFont: Math.min(...texts.map((t) => t.size)),
          labels: texts.filter((t) => /\d+ (Nov|Dec|Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct)/.test(t.s ?? '')).length,
        };
      });
      expect(check.right, `@${width}: label past the right edge`).toBeLessThanOrEqual(0.5);
      expect(check.left, `@${width}: label past the left edge`).toBeLessThanOrEqual(0.5);
      expect(check.minFont, `@${width}: axis text size`).toBeGreaterThanOrEqual(12);
      expect(check.labels).toBeGreaterThanOrEqual(2);
      await context.close();
    }
  });

  test('a short window (437 px high): the sidebar rail scrolls and its last control is reachable', async ({ browser }) => {
    const context = await browser.newContext({ viewport: { width: 1280, height: 437 }, reducedMotion: 'reduce' });
    await setPrefs(context, 'en', 'light');
    const page = await context.newPage();
    await page.goto(adminURL('/'));
    await ready(page);
    const nav = page.getByRole('navigation', { name: 'Main navigation' });
    const m = await nav.evaluate((el) => ({ scrolls: el.scrollHeight > el.clientHeight, overflowY: getComputedStyle(el).overflowY, h: el.getBoundingClientRect().height, vh: window.innerHeight }));
    expect(m.overflowY).toBe('auto');
    expect(m.h).toBeLessThanOrEqual(m.vh);
    expect(m.scrolls, 'rail content is taller than the window, so it must scroll').toBe(true);

    const logout = page.getByRole('button', { name: 'Sign out' });
    await logout.scrollIntoViewIfNeeded();
    await expect(logout).toBeInViewport({ ratio: 1 });
    const language = page.getByRole('button', { name: /^EN / });
    await language.scrollIntoViewIfNeeded();
    await expect(language).toBeInViewport({ ratio: 1 });
    await context.close();
  });

  test('360 and 780 px: the header greeting and subtitle are not cut off and stay on few lines', async ({ browser }) => {
    for (const width of [360, 780]) {
      for (const locale of LOCALES) {
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
        await setPrefs(context, locale, 'light');
        const page = await context.newPage();
        await page.goto(adminURL('/'));
        await ready(page);
        for (const id of ['greeting', 'subtitle']) {
          const el = page.getByTestId(id);
          await expect(el).not.toBeEmpty();
          const m = await el.evaluate((n) => ({ clipped: n.scrollWidth > n.clientWidth + 1 || getComputedStyle(n).textOverflow === 'ellipsis' && n.scrollWidth > n.clientWidth, h: n.getBoundingClientRect().height }));
          expect(m.clipped, `${locale} @${width}: ${id} clipped`).toBe(false);
          expect(m.h, `${locale} @${width}: ${id} height`).toBeLessThanOrEqual(2 * 24 + 2);
        }
        await context.close();
      }
    }
  });

  test('375 px: the bottom bar shows every control without horizontal scrolling; theme, language and sign out sit behind a visible "More" button', async ({ browser }) => {
    for (const width of [360, 375, 430]) {
      const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: 'reduce' });
      await setPrefs(context, 'en', 'light');
      const page = await context.newPage();
      await page.goto(adminURL('/'));
      await ready(page);
      const nav = page.getByRole('navigation', { name: 'Main navigation' });
      const m = await nav.evaluate((el) => ({ scrollable: el.scrollWidth > el.clientWidth + 1, right: el.getBoundingClientRect().right, left: el.getBoundingClientRect().left, iw: window.innerWidth }));
      expect(m.scrollable, `@${width}: the bar needs no horizontal scroll`).toBe(false);
      expect(m.left, `@${width}`).toBeGreaterThanOrEqual(0);
      expect(m.right, `@${width}`).toBeLessThanOrEqual(m.iw);
      for (const name of ['Dashboard', 'Apps', 'History', 'System logs']) await expect(nav.getByRole('link', { name, exact: true })).toBeInViewport({ ratio: 1 });
      for (const name of ['GitHub', 'Telegram', 'Account']) await expect(nav.getByRole('link', { name, exact: true })).toBeHidden();
      await expect(nav.getByRole('button', { name: 'Sign out' })).toBeHidden();
      const more = nav.getByRole('button', { name: 'More', exact: true });
      await expect(more).toBeInViewport({ ratio: 1 });
      await more.click();
      const dialog = page.getByRole('dialog', { name: 'More options' });
      for (const control of [dialog.getByRole('link', { name: 'GitHub' }), dialog.getByRole('link', { name: 'Telegram' }), dialog.getByRole('link', { name: 'Account' }), dialog.getByRole('button', { name: 'Toggle theme' }), dialog.getByRole('button', { name: /^EN / }), dialog.getByRole('button', { name: 'Sign out' })]) {
        await expect(control).toBeInViewport({ ratio: 1 });
      }
      await dialog.getByRole('button', { name: 'Toggle theme' }).click();
      await expect(page.locator('html')).toHaveClass(/dark/);
      await dialog.getByRole('link', { name: 'Account' }).click();
      await expect(page).toHaveURL(/\/account\/?$/);
      await context.close();
    }
  });

  test('icon-only controls show a text tooltip next to the rail on hover', async ({ browser }) => {
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, reducedMotion: 'reduce' });
    await setPrefs(context, 'en', 'light');
    const page = await context.newPage();
    await page.goto(adminURL('/'));
    await ready(page);
    await page.getByRole('link', { name: 'History', exact: true }).hover();
    const tip = page.getByTestId('rail-tip');
    await expect(tip).toHaveText('History');
    const [linkBox, tipBox] = await Promise.all([page.getByRole('link', { name: 'History', exact: true }).boundingBox(), tip.boundingBox()]);
    expect(tipBox!.x).toBeGreaterThanOrEqual(linkBox!.x + linkBox!.width);
    expect(tipBox!.x + tipBox!.width).toBeLessThanOrEqual(1280);
    await context.close();
  });
});
