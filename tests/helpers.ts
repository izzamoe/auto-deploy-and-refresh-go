// Shared helpers of the Playwright specs (T094/T095). The server, the generated admin
// account and the signed-in session come from playwright/global-setup.js.
import path from 'node:path';
import { expect } from '@playwright/test';
import type { APIRequestContext, BrowserContext, Page } from '@playwright/test';

export const STORAGE_STATE = path.resolve(__dirname, '..', 'playwright', '.auth', 'admin.json');
export const EMPTY_STATE = { cookies: [], origins: [] };

export function baseURL(): string {
  const value = process.env.PLAYWRIGHT_BASE_URL;
  if (!value) throw new Error('PLAYWRIGHT_BASE_URL is not set');
  return value;
}

export const adminURL = (route = '/') => `${baseURL()}/admin${route}`;

export const credentials = () => ({
  username: process.env.PLAYWRIGHT_ADMIN_USERNAME ?? 'admin',
  password: process.env.PLAYWRIGHT_ADMIN_PASSWORD ?? 'playwright-secret',
});

/** What the SPA's same-origin fetch sends; mutations without it are refused (FR-011). */
export const browserHeaders = () => ({
  'Content-Type': 'application/json',
  'X-Admin-Request': 'true',
  'Sec-Fetch-Site': 'same-origin',
  Origin: baseURL(),
});

export type Theme = 'light' | 'dark';
export type Locale = 'id' | 'en';

/** Fixes the language and theme before any page script runs (localStorage keys of the UI). */
export async function setPrefs(context: BrowserContext, locale: Locale, theme: Theme): Promise<void> {
  await context.addInitScript(
    ([l, t]) => {
      window.localStorage.setItem('ad.locale', l);
      window.localStorage.setItem('theme', t);
    },
    [locale, theme],
  );
}

/** Queues a deploy through the public webhook; returns its tag. */
export async function seedJob(request: APIRequestContext, prefix = 'pw'): Promise<string> {
  const tag = `${prefix}-${Date.now()}-${Math.floor(Math.random() * 1000)}`;
  const secret = process.env.PLAYWRIGHT_WEBHOOK_SECRET;
  const res = await request.post(`${baseURL()}/webhook`, {
    headers: { Authorization: `Bearer ${secret}` },
    data: { tag },
  });
  expect(res.status()).toBe(202);
  return tag;
}

export async function firstApp(request: APIRequestContext): Promise<{ id: string; name: string }> {
  const res = await request.get(`${baseURL()}/admin/api/v1/apps`);
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.apps.length).toBeGreaterThanOrEqual(1);
  return { id: body.apps[0].id, name: body.apps[0].name };
}

/**
 * Collects what must never happen (FR-109, B50, K1): a browser dialog (the HTTP Basic prompt
 * is one) and any response carrying `www-authenticate`.
 */
export function watchBasicAuth(page: Page) {
  const seen = { dialogs: [] as string[], challenges: [] as string[] };
  page.on('dialog', (d) => {
    seen.dialogs.push(`${d.type()}: ${d.message()}`);
    void d.dismiss();
  });
  page.on('response', (r) => {
    if (r.headers()['www-authenticate']) seen.challenges.push(`${r.status()} ${r.url()}`);
  });
  return seen;
}

/** Waits until a page of the admin finished its first render and its initial requests. */
export async function ready(page: Page): Promise<void> {
  await expect(page.locator('main h1, main h2').first()).toBeAttached({ timeout: 20_000 });
  await page.waitForLoadState('networkidle');
}

export async function signInThroughForm(page: Page): Promise<void> {
  const { username, password } = credentials();
  await page.getByLabel(/^(Username|Nama pengguna)$/).fill(username);
  await page.getByLabel(/^(Password|Kata sandi)$/).fill(password);
  await page.getByRole('button', { name: /^(Sign in|Masuk)$/ }).click();
}

/** The routes of the admin (static export, trailing slash). */
export const STATIC_ROUTES = ['/', '/apps/', '/apps/new/', '/history/', '/settings/github/', '/settings/telegram/', '/logs/', '/account/'];
