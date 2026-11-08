import assert from "node:assert/strict";
import fs from "node:fs";
import { test } from "node:test";
import type { BrowserStatus, ExtensionStatus } from "@hermesh/her-protocol";
import { browserName, extensionEdits, yours, yoursConfig } from "./yours.ts";

// What the Browser section makes of browser/status and `[browser]`.

const chrome: ExtensionStatus = { browser: "Google Chrome 142.0.7444.60", extension_version: "0.2.1", instance: "a", in_use: true };
const status = (over: Partial<BrowserStatus> = {}): BrowserStatus => ({ embedded: false, ...over });

test("the row's state: connected or not, used or not, or one side too old", () => {
  const on = yoursConfig({ browser: { extension: true } });
  const off = yoursConfig({ browser: { extension: false } });
  const unset = yoursConfig({});
  assert.equal(yours(status(), unset).state, "off");
  assert.equal(yours(status({ extension: chrome }), unset).state, "ready");
  assert.equal(yours(status({ extension: chrome }), off).state, "ready");
  assert.equal(yours(status({ extension: chrome }), on).state, "on");
  assert.equal(yours(status(), on).state, "lost");
  for (const problem of ["update_extension", "update_her"] as const) {
    const y = yours(status({ extension: { ...chrome, problem } }), on);
    assert.equal(y.state, "outdated");
    assert.equal(y.problem, problem);
    assert.equal(y.connected, null, "one that cannot be used is not connected");
  }
  assert.equal(yours(null, null).state, "off");
});

test("what to choose from, and why a host was refused", () => {
  const other: ExtensionStatus = { browser: "Microsoft Edge 142.0.1", instance: "b", account: "me@example.com" };
  const y = yours(status({ extension: chrome, extensions: [chrome, other], refused: null }), null);
  assert.deepEqual(y.several.map((e) => e.instance), ["a", "b"]);
  // The one chosen is away: the one connected waits to be chosen.
  const away = yours(status({ extensions: [other], refused: "not her's extension host" }), yoursConfig({ browser: { extension: true } }));
  assert.equal(away.state, "lost");
  assert.deepEqual(away.several, [other]);
  assert.equal(away.refused, "not her's extension host");
});

test("the old way of choosing it reads as on, and goes with the next write", () => {
  const legacy = yoursConfig({ browser: { backend: "extension" } });
  assert.deepEqual(legacy, { extension: true, legacyBackend: true });
  assert.deepEqual(extensionEdits(false, legacy.legacyBackend), [
    { key_path: "browser.extension", value: false },
    { key_path: "browser.backend", value: null },
  ]);
  assert.deepEqual(extensionEdits(true, yoursConfig({ browser: { extension: false } }).legacyBackend), [{ key_path: "browser.extension", value: true }]);
});

test("Chinese says everything English does, with the same placeholders", () => {
  const read = (lang: string) => JSON.parse(fs.readFileSync(new URL(`locales/${lang}/yours.json`, import.meta.url), "utf8"));
  const leaves = (o: Record<string, unknown>, at = ""): [string, string][] =>
    Object.entries(o).flatMap(([k, v]) => (typeof v === "string" ? [[at + k, v] as [string, string]] : leaves(v as Record<string, unknown>, `${at}${k}.`)));
  const en = new Map(leaves(read("en")));
  const zh = new Map(leaves(read("zh-CN")));
  assert.deepEqual([...zh.keys()].sort(), [...en.keys()].sort());
  const holes = (s: string) => (s.match(/{{\w+}}/g) ?? []).sort();
  for (const [key, text] of en) assert.deepEqual(holes(zh.get(key)!), holes(text), key);
});

test("a browser's name without its build", () => {
  assert.equal(browserName("Google Chrome 142.0.7444.60"), "Google Chrome 142");
  assert.equal(browserName("Brave 1.84"), "Brave 1");
  assert.equal(browserName(undefined), "Chrome");
  assert.equal(browserName(""), "Chrome");
});
