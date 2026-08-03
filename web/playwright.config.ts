import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  retries: process.env.CI ? 2 : 0,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:4179",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "pnpm dev --host 127.0.0.1 --port 4179",
    url: "http://127.0.0.1:4179",
    reuseExistingServer: !process.env.CI,
  },
  projects: [
    { name: "desktop-chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 1000 } } },
    { name: "mobile-390-chromium", use: { ...devices["Pixel 7"], viewport: { width: 390, height: 844 } } },
    { name: "iphone-webkit", use: { ...devices["iPhone 13"] } },
    { name: "android-chromium", use: { ...devices["Pixel 7"] } },
    { name: "ipad-webkit", use: { ...devices["iPad Pro 11"] } },
    { name: "ipad-landscape-webkit", use: { ...devices["iPad Pro 11 landscape"], viewport: { width: 1194, height: 834 } } },
  ],
});
