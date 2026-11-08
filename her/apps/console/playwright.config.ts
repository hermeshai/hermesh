import { defineConfig } from "@playwright/test";
import { existsSync } from "node:fs";

// The console end to end, in a headless browser, against the stand-in for hermeshd and a real her
// (scripts/stand-in.ts): `pnpm e2e`, with HER_BIN_DIR naming her's target/debug when it is not
// this checkout's. The installed Google Chrome when there is one, else Playwright's Chromium.
const chrome = ["/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/opt/google/chrome/chrome"].some((p) => existsSync(p));

export default defineConfig({
  testDir: "e2e",
  workers: 1,
  timeout: 180_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  outputDir: "test-results",
  use: { headless: true, channel: chrome ? "chrome" : undefined, locale: "en-US", viewport: { width: 1180, height: 820 }, trace: "retain-on-failure" },
});
