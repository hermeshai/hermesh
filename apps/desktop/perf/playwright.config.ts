import { defineConfig } from "@playwright/test";

// The performance benchmark (pnpm perf): one long test, see bench.spec.ts.
export default defineConfig({
  testDir: ".",
  testMatch: "bench.spec.ts",
  workers: 1,
  timeout: 30 * 60_000,
  reporter: [["list"]],
  outputDir: "results/test-results",
});
