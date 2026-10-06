import { defineConfig, devices } from '@playwright/test'

// The browser suite runs against a real `precious serve` over the regression
// corpus; e2e/global-setup.ts builds and starts it and sets PRECIOUS_E2E_ORIGIN.
// The tests share that one server and change its state in order, so they run
// serially in one worker.
export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  // The spec opens its own browser context (one session shared by every
  // test) with its locale, time zone, and timeouts.
  use: { trace: 'retain-on-failure' },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
