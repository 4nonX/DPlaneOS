import { defineConfig, devices } from '@playwright/test'

// Browser tests of the web interface against a mocked API (Design 0001
// phase 4 exit criterion), on the production build. Every /api request is
// answered by e2e/mockApi.ts;
// no daemon runs.
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    // The production build: code limited to development builds must not be
    // what a test relies on.
    command: 'npx vite build --outDir dist --emptyOutDir --logLevel warn && npx vite preview --outDir dist --host 127.0.0.1 --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
