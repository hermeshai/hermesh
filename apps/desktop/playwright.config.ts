import { defineConfig } from "@playwright/test";
import path from "node:path";

// Run through `go test ./e2e -run TestDesktop`, which sets up the world and
// passes it in HERMESH_E2E_FIXTURE.
export default defineConfig({
  testDir: "e2e",
  workers: 1,
  timeout: 60_000,
  // CI runners are slow when several apps start at once.
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  outputDir: process.env.HERMESH_PW_OUT ?? (process.env.HERMESH_DESKTOP_OUT ? path.join(process.env.HERMESH_DESKTOP_OUT, "test-results") : "test-results"),
});
