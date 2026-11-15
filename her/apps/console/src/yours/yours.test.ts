import assert from "node:assert/strict";
import { test } from "node:test";
import type { BrowserStatus, ExtensionStatus } from "@hermesh/her-protocol";
import { browserName, builtin, builtinConfig, controlConfig, controlEdits, controlOn, defaultBrowser, defaultEdits, extensionEdits, extensionFolder, herSystem, programName, yours, yoursConfig } from "./yours.ts";

// What the Browser section makes of browser/status and `[browser]`.

const chrome: ExtensionStatus = { browser: "Google Chrome 142.0.7444.60", extension_version: "0.2.1", instance: "a", in_use: true };
const status = (over: Partial<BrowserStatus> = {}): BrowserStatus => ({ embedded: false, window: false, ...over });

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

test("the guide goes by the system her says it runs on", () => {
  assert.equal(herSystem(status({ os: "linux" })), "linux");
  assert.equal(herSystem(status({ os: "macos" })), "macos");
  assert.equal(herSystem(status()), "macos", "a her that names no system");
  assert.equal(herSystem(null), "macos");
  // hermeshd's ExtensionFolder, for each.
  assert.equal(extensionFolder.linux, "~/.local/share/hermesh/chrome-extension");
  assert.equal(extensionFolder.macos, "~/Library/Application Support/Hermesh/Chrome Extension");
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

test("a browser's name without its build", () => {
  assert.equal(browserName("Google Chrome 142.0.7444.60"), "Google Chrome 142");
  assert.equal(browserName("Brave 1.84"), "Brave 1");
  assert.equal(browserName(undefined), "Chrome");
  assert.equal(browserName(""), "Chrome");
});

test("browser control: as set, else on where a desktop app shows browsers or the owner's Chrome is used", () => {
  const unset = controlConfig({});
  assert.deepEqual(unset, { enabled: null, default: "builtin" });
  assert.equal(controlOn(status(), unset, yoursConfig({})), false);
  assert.equal(controlOn(status({ embedded: true }), unset, yoursConfig({})), true);
  assert.equal(controlOn(status(), unset, yoursConfig({ browser: { extension: true } })), true);
  assert.equal(controlOn(status({ embedded: true }), controlConfig({ browser: { enabled: false } }), null), false);
  assert.equal(controlOn(null, controlConfig({ browser: { enabled: true } }), null), true);
  assert.deepEqual(controlEdits(false), [{ key_path: "browser.enabled", value: false }]);
});

test("new pages open in the owner's Chrome only while it is in use and connected", () => {
  const yoursDefault = controlConfig({ browser: { default: "yours" } });
  assert.equal(defaultBrowser(yoursDefault, "on"), "yours");
  for (const state of ["off", "ready", "lost", "outdated"] as const) assert.equal(defaultBrowser(yoursDefault, state), "builtin", state);
  assert.equal(defaultBrowser(controlConfig({}), "on"), "builtin");
  assert.deepEqual(defaultEdits("yours"), [{ key_path: "browser.default", value: "yours" }]);
});

test("the built-in browser's program and window: what her finds, unless the settings say", () => {
  const found = status({ program: "/usr/bin/google-chrome", window: false });
  assert.deepEqual(builtin(found, builtinConfig({})), { program: "/usr/bin/google-chrome", window: false });
  assert.equal(builtin(status(), builtinConfig({})).program, null, "none found");
  assert.equal(builtin(found, builtinConfig({ browser: { executable: "/opt/chromium/chrome" } })).program, "/opt/chromium/chrome");
  assert.equal(builtin(found, builtinConfig({ browser: { headless: false } })).window, true);
  assert.equal(builtin(status({ window: true }), builtinConfig({ browser: { headless: "true" } })).window, false);
});

test("a program's name, as the owner knows it", () => {
  assert.equal(programName("/usr/bin/google-chrome-stable"), "Google Chrome");
  assert.equal(programName("/snap/bin/chromium"), "Chromium");
  assert.equal(programName("/usr/bin/microsoft-edge"), "Microsoft Edge");
  assert.equal(programName("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"), "Google Chrome");
  assert.equal(programName("/root/.her/browser/chromium/chrome-linux64/chrome"), "Chrome for Testing");
  assert.equal(programName("/opt/thorium/thorium"), "thorium");
});
