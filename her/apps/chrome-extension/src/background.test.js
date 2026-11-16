import assert from "node:assert/strict";
import test from "node:test";

const event = () => ({ listeners: [], addListener(f) { this.listeners.push(f); }, emit(...args) { for (const f of this.listeners) f(...args); } });
const waitFor = async (check) => {
  for (let i = 0; i < 1000; i++) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
  assert.fail("the worker did not answer");
};

// The real worker, native port, storage format and relay, with only Chrome's APIs faked.
// There are no user profiles, subprocesses or network requests in this test.
for (const carried of [false, true]) test(`recovery from ${carried ? "carried" : "session"} storage preserves valid tabs across streams`, async (t) => {
  const old = {
    agentTabs: [7, 8, 9].map((id) => [id, { session: "s1", url: "https://recovery.test/", title: "Kept" }]),
    // Older saved state need not have groups, windowId or sessions.
  };
  let saved = carried ? {} : { her: old };
  let local = carried ? { herCarried: old } : {};
  const messages = [];
  const port = { onMessage: event(), onDisconnect: event(), postMessage: (m) => messages.push(m) };
  const area = (read, write) => ({
    get: async () => structuredClone(read()),
    set: async (value) => write({ ...read(), ...structuredClone(value) }),
    remove: async (key) => { const value = { ...read() }; delete value[key]; write(value); },
  });
  let missing = false;
  const chrome = {
    storage: { session: area(() => saved, (v) => { saved = v; }), local: area(() => local, (v) => { local = v; }) },
    runtime: {
      onMessage: event(), onStartup: event(), onInstalled: event(), onUpdateAvailable: event(),
      connectNative: () => port, getManifest: () => ({ version: "0.2.2", update_url: "https://unused.test/" }),
      sendMessage: async () => {},
    },
    tabs: {
      query: async () => [{ id: 7 }, { id: 8 }], // 9 closed while the worker was stopped.
      onCreated: event(), onUpdated: event(), onRemoved: event(),
      remove: async () => assert.fail("recovery must never close a real tab"),
    },
    debugger: {
      onEvent: event(), onDetach: event(),
      attach: async ({ tabId }) => { if (missing && tabId === 7) throw new Error(`No tab with given id 7${carried ? "." : ""}`); },
      detach: async () => {}, sendCommand: async () => ({}),
    },
    windows: { onRemoved: event() },
    tabGroups: { update: async () => {} },
    alarms: { create: () => {}, onAlarm: event() },
    action: { setTitle: async () => {}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {} },
    i18n: { getMessage: (name) => name },
  };
  t.mock.method(globalThis, "fetch", async () => assert.fail("the worker must not fetch with a store manifest"));
  const before = globalThis.chrome;
  globalThis.chrome = chrome;
  t.after(() => { if (before === undefined) delete globalThis.chrome; else globalThis.chrome = before; });
  await import(`./background.js?test=${carried ? "carried" : "session"}`);
  await waitFor(() => messages.some((m) => m.hello));
  port.onMessage.emit({ status: "connected" });
  await waitFor(() => saved.her !== old && saved.her?.agentTabs.length === 2);
  assert.equal(local.herCarried, undefined);
  let id = 0;
  const call = async (stream, method, params = {}) => {
    const n = ++id;
    port.onMessage.emit({ stream, data: { id: n, method, params } });
    await waitFor(() => messages.some((m) => m.stream === stream && m.data?.id === n));
    return messages.find((m) => m.stream === stream && m.data?.id === n).data;
  };
  assert.deepEqual((await call(1, "Target.getTargets", { herSession: "s1" })).result.targetInfos.map((v) => v.targetId), ["T7", "T8"]);
  missing = true; // Missed onRemoved after startup, before attach.
  assert.match((await call(1, "Target.attachToTarget", { targetId: "T7" })).error.message, /No tab/);
  assert.ok((await call(1, "Target.attachToTarget", { targetId: "T8" })).result.sessionId);
  await waitFor(() => saved.her.agentTabs.length === 1);
  assert.equal(saved.her.agentTabs[0][0], 8);
  const report = messages.filter((m) => m.control?.kind === "tabs").at(-1);
  assert.deepEqual(report.control.sessions.s1.map((v) => v.target_id), ["T8"]);
  port.onMessage.emit({ status: "connected" }); // Native host reconnects; ownership survives.
  assert.deepEqual((await call(2, "Target.getTargets", { herSession: "s1" })).result.targetInfos.map((v) => v.targetId), ["T8"]);
  // The last tab closes between listing and attaching it.
  chrome.tabs.onRemoved.emit(8);
  assert.match((await call(2, "Target.attachToTarget", { targetId: "T8" })).error.message, /No target with given id T8/);
  await waitFor(() => saved.her.agentTabs.length === 0);
  assert.deepEqual((await call(2, "Target.getTargets", { herSession: "s1" })).result.targetInfos, []);
});
