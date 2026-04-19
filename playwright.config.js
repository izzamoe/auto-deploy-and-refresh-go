const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: 'line',
  globalSetup: require.resolve('./playwright/global-setup'),
  use: {
    trace: 'off',
    screenshot: 'only-on-failure',
    video: 'off'
  }
});
