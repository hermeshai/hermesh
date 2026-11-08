import assert from "node:assert/strict";
import { test } from "node:test";
import { demoModels } from "./mock/data.ts";
import { draftModel, modelGroups, recentModels } from "./models.ts";
import type { ListSession } from "./sessions.ts";

const models = demoModels();

test("a new session asks for the model chosen while its provider is there", () => {
  assert.equal(draftModel(models, "deepseek/deepseek-v4-pro"), "deepseek/deepseek-v4-pro");
  // A provider without a key is still there: the page asks for the key, not another model.
  assert.equal(draftModel(models, "anthropic/claude-sonnet-5-5"), "anthropic/claude-sonnet-5-5");
  assert.equal(draftModel(models, null), null);
});

test("a model whose connection is gone gives way to the default", () => {
  assert.equal(draftModel(models, "local-35e57942-0000-4000-8000-000000000001/flash"), null);
});

test("before the list comes the choice stands", () => {
  assert.equal(draftModel({ models: [], providers: [] }, "local-35e57942-0000-4000-8000-000000000001/flash"), "local-35e57942-0000-4000-8000-000000000001/flash");
});

test("a model menu groups models by provider, in the providers' order", () => {
  const providers = [{ id: "b", name: "Beta" }, { id: "a", name: "Alpha" }];
  const list = [
    { id: "a/one", name: "One", provider: "a" },
    { id: "b/two", name: "Two", provider: "b" },
    { id: "x/gone", name: "Gone", provider: "x" },
    { id: "a/three", name: "Three", provider: "a" },
  ];
  const groups = modelGroups(list, providers);
  assert.deepEqual(groups.map((g) => [g.name, g.models.map((m) => m.name)]), [["Beta", ["Two"]], ["Alpha", ["One", "Three"]], ["x", ["Gone"]]]);
  // A search finds models by name or reference, and a provider's whole group by its name.
  assert.deepEqual(modelGroups(list, providers, " THR ").map((g) => g.models.map((m) => m.id)), [["a/three"]]);
  assert.deepEqual(modelGroups(list, providers, "a/o").map((g) => g.models.map((m) => m.id)), [["a/one"]]);
  assert.deepEqual(modelGroups(list, providers, "alpha").map((g) => g.models.map((m) => m.id)), [["a/one", "a/three"]]);
  assert.deepEqual(modelGroups(list, providers, "nothing"), []);
});

test("recent models are the sessions' models, each once, last used first", () => {
  const s = (id: string, model: string | null, updated_at: string): ListSession =>
    ({ session_id: id, cwd: "/w", created_at: updated_at, updated_at, state: "idle", archived: false, loaded: false, busy: false, attached_clients: 0, model });
  const list = [
    s("1", "a/one", "2026-11-01T10:00:00Z"),
    s("2", "b/two", "2026-11-03T10:00:00Z"),
    s("3", "a/one", "2026-11-04T10:00:00Z"),
    s("4", null, "2026-11-05T10:00:00Z"),
    s("5", "c/three", "2026-10-01T10:00:00Z"),
    s("6", "d/four", "2026-09-01T10:00:00Z"),
  ];
  assert.deepEqual(recentModels(list), ["a/one", "b/two", "c/three"]);
  assert.deepEqual(recentModels(list, 1), ["a/one"]);
});
