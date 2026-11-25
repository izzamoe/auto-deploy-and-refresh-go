const path = require('node:path');
const { defineConfig } = require('@playwright/test');

// The signed-in session that playwright/global-setup.js saves after logging in through
// the v1 API. Tests that exercise login/expiry themselves override it per file.
const storageState = path.resolve(__dirname, 'playwright', '.auth', 'admin.json');

module.exports = defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  timeout: 60_000,
  reporter: 'line',
  globalSetup: require.resolve('./playwright/global-setup'),
  use: {
    storageState,
    trace: 'off',
    screenshot: 'only-on-failure',
    video: 'off'
  },
  // One project per gate named in the plan (FR-136): the CI selects them with --project.
  // smoke/routing/session/a11y run against the build; visual compares screenshots against a
  // baseline that is NOT committed (Q-25): create it with --update-snapshots first.
  projects: [
    { name: 'smoke', testMatch: /admin-smoke\.spec\.ts$/ },
    { name: 'routing', testMatch: /routing\.spec\.ts$/ },
    { name: 'session', testMatch: /session-expiry\.spec\.ts$/, timeout: 180_000 },
    { name: 'a11y', testMatch: /a11y\.spec\.ts$/, timeout: 120_000 },
    { name: 'visual', testMatch: /visual\.spec\.ts$/, timeout: 120_000 },
    // T104: timing budget (FCP); only run on request (`--project=perf`), never by `make e2e` or the CI.
    { name: 'perf', testMatch: /perf\.spec\.ts$/, timeout: 120_000 }
  ],
  expect: {
    toHaveScreenshot: { maxDiffPixelRatio: 0.02, animations: 'disabled' }
  }
});
