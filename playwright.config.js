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
  reporter: 'line',
  globalSetup: require.resolve('./playwright/global-setup'),
  use: {
    storageState,
    trace: 'off',
    screenshot: 'only-on-failure',
    video: 'off'
  },
  // One project per gate named in the plan (FR-136): the CI selects them with
  // --project. Spec files for routing/session/a11y/visual arrive with the UI (Fase 5/7).
  projects: [
    { name: 'smoke', testMatch: /admin-smoke\.spec\.js$/ },
    { name: 'routing', testMatch: /routing\.spec\.[jt]s$/ },
    { name: 'session', testMatch: /session-expiry\.spec\.[jt]s$/ },
    { name: 'a11y', testMatch: /a11y\.spec\.[jt]s$/ },
    { name: 'visual', testMatch: /visual\.spec\.[jt]s$/ }
  ]
});
