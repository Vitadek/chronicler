import { defineConfig, devices } from '@playwright/test';

// Matches the parent formal suite's env conventions (tests/formal/compose.yml's
// x-formal-environment anchor): CHRONICLE_URL for the app under test, REPORT_DIR
// for where artifacts land.
const CHRONICLE_URL = process.env.CHRONICLE_URL ?? 'http://localhost:3000';
const REPORT_DIR = process.env.REPORT_DIR ?? 'artifacts';

export default defineConfig({
  testDir: './specs',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: false, // cases 1/7 need controlled ordering; not worth splitting config for it
  retries: 0, // this tier is informational, not a gate — a flake should be visible, not hidden
  reporter: [
    ['list'],
    ['json', { outputFile: `${REPORT_DIR}/browser-report.json` }],
  ],
  use: {
    baseURL: CHRONICLE_URL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  outputDir: `${REPORT_DIR}/test-results`,
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
