import assert from "node:assert/strict";
import { test } from "node:test";
import { settingsRoute, settingsUp } from "./settingsRoute.ts";

test("settings links, old and new", () => {
  assert.deepEqual(settingsRoute(""), { page: "general", item: "" });
  assert.deepEqual(settingsRoute("notifications"), { page: "notifications", item: "" });
  assert.deepEqual(settingsRoute("devices"), { page: "account", item: "" });
  assert.deepEqual(settingsRoute("grants/g_1"), { page: "grants", item: "g_1" });
  assert.deepEqual(settingsRoute("machines"), { page: "env", item: "this" });
  assert.deepEqual(settingsRoute("machines/m_1"), { page: "env", item: "m_1" });
  assert.deepEqual(settingsRoute("machines/m_1/codex"), { page: "env", item: "m_1/providers/codex" });
  assert.deepEqual(settingsRoute("env"), { page: "env", item: "this" });
  assert.deepEqual(settingsRoute("env/m_1/storage"), { page: "env", item: "m_1/storage" });
  assert.deepEqual(settingsRoute("nope/x"), { page: "general", item: "x" });
});

test("back goes one level up, an environment being the top", () => {
  assert.equal(settingsUp("env", "m_1/providers/codex"), "m_1/providers");
  assert.equal(settingsUp("env", "m_1/storage"), "m_1");
  assert.equal(settingsUp("env", "m_1"), "m_1");
  assert.equal(settingsUp("grants", "g_1"), "");
});
