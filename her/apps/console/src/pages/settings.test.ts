import assert from "node:assert/strict";
import { test } from "node:test";
import type { ModelSummary, ModelsListResult } from "@hermesh/her-protocol";
import { approvals, modelChoices, readyModel, siteKey, siteOrigin, sitesOf, startsWith, thinkingChoices } from "./settings.ts";

// What Overview, Defaults and Browser make of her's answers.

const model = (id: string, over: Partial<ModelSummary> = {}): ModelSummary => ({
  id,
  name: id,
  provider: id.split("/")[0],
  available: true,
  shown: true,
  known: true,
  context_window: 200_000,
  default_thinking: "default",
  images: false,
  reasoning: false,
  thinking_levels: [],
  ...over,
});

const list = (models: ModelSummary[], defaults?: ModelsListResult["defaults"]): ModelsListResult => ({ models, providers: [], defaults });

test("what new assistants start with: the file's settings, else her's own choice as models/list gives it", () => {
  const started = list([], { model: "mock/mock-1", thinking: "default", permission_mode: "manual" });
  assert.deepEqual(startsWith(started, {}), { model: "mock/mock-1", thinking: "default", permission_mode: "manual" });
  assert.deepEqual(startsWith(started, { model: "mock/deepseek-flash", thinking: "high", permission_mode: "accept-edits" }), { model: "mock/deepseek-flash", thinking: "high", permission_mode: "accept-edits" });
  assert.deepEqual(startsWith(list([]), null), { model: null, thinking: "default", permission_mode: "manual" });
  assert.deepEqual(startsWith(null, { model: "", thinking: 3 }), { model: null, thinking: "default", permission_mode: "manual" });
});

test("a model is ready when the one new assistants start with can run", () => {
  const models = list([model("mock/mock-1"), model("deepseek/flash", { available: false })]);
  assert.equal(readyModel(models, "mock/mock-1")?.id, "mock/mock-1");
  assert.equal(readyModel(models, "deepseek/flash"), null);
  assert.equal(readyModel(models, null), null);
  assert.equal(readyModel(null, "mock/mock-1"), null);
});

test("defaults: the models that can run and are in the menu, the one set now; its thinking levels; no full access", () => {
  const models = [model("mock/mock-1"), model("mock/mock-2", { shown: false }), model("mock/hidden", { shown: false }), model("other/x", { available: false })];
  assert.deepEqual(
    modelChoices(list(models), "mock/mock-2").map((m) => m.id),
    ["mock/mock-1", "mock/mock-2"],
  );
  assert.deepEqual(thinkingChoices(model("mock/think", { thinking_levels: ["low", "high", "max"] })), ["default", "low", "high", "max"]);
  assert.deepEqual(thinkingChoices(undefined), ["default"]);
  assert.deepEqual(approvals, ["manual", "accept-edits"]);
});

test("sites: an origin as typed, as her keeps it, and the key path that names it", () => {
  assert.equal(siteOrigin("example.com"), "https://example.com");
  assert.equal(siteOrigin(" GitHub.com/her/issues?q=1 "), "https://github.com");
  assert.equal(siteOrigin("http://192.168.1.10:8080/admin"), "http://192.168.1.10:8080");
  assert.equal(siteOrigin("https://example.com:443"), "https://example.com");
  assert.equal(siteOrigin("ftp://example.com"), null);
  assert.equal(siteOrigin("https://user:pw@example.com"), null);
  assert.equal(siteOrigin("not a site"), null);
  assert.equal(siteOrigin(""), null);
  assert.equal(siteKey("https://example.com"), 'browser.sites."https://example.com"');
});

test("sites from config/read: all sites first, then by origin; anything not a level left out", () => {
  const config = { browser: { sites: { "https://b.example": "navigate", "*": "act", "https://a.example": "deny", odd: 3 } } };
  assert.deepEqual(sitesOf(config), [
    ["*", "act"],
    ["https://a.example", "deny"],
    ["https://b.example", "navigate"],
  ]);
  assert.deepEqual(sitesOf({}), []);
  assert.deepEqual(sitesOf(null), []);
});
