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

test("her-connect alarm waits for ready so restored tabs are not wiped", async (t) => {
  const old = { agentTabs: [[11, { session: "s1", url: "https://alarm.test/", title: "Saved" }]] };
  let saved = { her: old };
  let local = {};
  let releaseSession;
  const sessionGate = new Promise((resolve) => { releaseSession = resolve; });
  let connects = 0;
  const messages = [];
  const port = { onMessage: event(), onDisconnect: event(), postMessage: (m) => messages.push(m) };
  const area = (read, write, gate) => ({
    get: async () => { if (gate) await gate; return structuredClone(read()); },
    set: async (value) => write({ ...read(), ...structuredClone(value) }),
    remove: async (key) => { const value = { ...read() }; delete value[key]; write(value); },
  });
  const chrome = {
    storage: { session: area(() => saved, (v) => { saved = v; }, sessionGate), local: area(() => local, (v) => { local = v; }) },
    runtime: {
      onMessage: event(), onStartup: event(), onInstalled: event(), onUpdateAvailable: event(),
      connectNative: () => { connects++; return port; },
      getManifest: () => ({ version: "0.2.2", update_url: "https://unused.test/" }),
      sendMessage: async () => {},
    },
    tabs: {
      query: async () => [{ id: 11 }],
      onCreated: event(), onActivated: event(), onUpdated: event(), onRemoved: event(),
      remove: async () => {},
    },
    debugger: { onEvent: event(), onDetach: event(), attach: async () => {}, detach: async () => {}, sendCommand: async () => ({}) },
    windows: { WINDOW_ID_NONE: -1, onCreated: event(), onFocusChanged: event(), onRemoved: event() },
    tabGroups: { update: async () => {} },
    alarms: { create: () => {}, onAlarm: event() },
    action: { setTitle: async () => {}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {} },
    i18n: { getMessage: (name) => name },
  };
  t.mock.method(globalThis, "fetch", async () => assert.fail("the worker must not fetch with a store manifest"));
  const before = globalThis.chrome;
  globalThis.chrome = chrome;
  t.after(() => { if (before === undefined) delete globalThis.chrome; else globalThis.chrome = before; });

  await import("./background.js?test=alarm-ready");
  // Alarm fires immediately while ready is still restoring storage.
  chrome.alarms.onAlarm.emit({ name: "her-connect" });
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(connects, 0, "alarm must wait for ready before connecting");

  releaseSession();
  await waitFor(() => messages.some((m) => m.hello));
  assert.equal(connects, 1);
  port.onMessage.emit({ status: "connected" });
  await waitFor(() => messages.some((m) => m.control?.kind === "tabs"));
  const report = messages.filter((m) => m.control?.kind === "tabs").at(-1);
  assert.deepEqual(report.control.sessions.s1.map((v) => v.target_id), ["T11"]);
});

test("stale port hello and disconnect do not corrupt a newer port", async (t) => {
  let saved = {};
  let local = {};
  const ports = [];
  let releaseFirstIdentity;
  let identityCalls = 0;
  const area = (read, write) => ({
    get: async () => structuredClone(read()),
    set: async (value) => write({ ...read(), ...structuredClone(value) }),
    remove: async (key) => { const value = { ...read() }; delete value[key]; write(value); },
  });
  const chrome = {
    storage: { session: area(() => saved, (v) => { saved = v; }), local: area(() => local, (v) => { local = v; }) },
    identity: {
      getProfileUserInfo: () => {
        identityCalls++;
        if (identityCalls === 1) return new Promise((resolve) => { releaseFirstIdentity = () => resolve({ email: "stale@test" }); });
        return Promise.resolve({ email: "fresh@test" });
      },
    },
    runtime: {
      onMessage: event(), onStartup: event(), onInstalled: event(), onUpdateAvailable: event(),
      connectNative: () => {
        const p = { onMessage: event(), onDisconnect: event(), messages: [], postMessage(m) { this.messages.push(m); } };
        ports.push(p);
        return p;
      },
      getManifest: () => ({ version: "0.2.2", update_url: "https://unused.test/" }),
      sendMessage: async () => {},
    },
    tabs: { query: async () => [], onCreated: event(), onActivated: event(), onUpdated: event(), onRemoved: event(), remove: async () => {} },
    debugger: { onEvent: event(), onDetach: event(), attach: async () => {}, detach: async () => {}, sendCommand: async () => ({}) },
    windows: { WINDOW_ID_NONE: -1, onCreated: event(), onFocusChanged: event(), onRemoved: event() },
    tabGroups: { update: async () => {} },
    alarms: { create: () => {}, onAlarm: event() },
    action: { setTitle: async () => {}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {} },
    i18n: { getMessage: (name) => name },
  };
  t.mock.method(globalThis, "fetch", async () => assert.fail("the worker must not fetch with a store manifest"));
  const before = globalThis.chrome;
  globalThis.chrome = chrome;
  t.after(() => { if (before === undefined) delete globalThis.chrome; else globalThis.chrome = before; });

  await import("./background.js?test=stale-port");
  await waitFor(() => ports.length === 1);
  const first = ports[0];
  // First port disconnects before its async hello resolves, then alarm opens a fresh port.
  first.onDisconnect.emit();
  chrome.alarms.onAlarm.emit({ name: "her-connect" });
  await waitFor(() => ports.length === 2);
  const second = ports[1];
  await waitFor(() => second.messages.some((m) => m.hello));

  // Now the stale first port finishes hello() and emits a late message/disconnect.
  releaseFirstIdentity();
  await new Promise((resolve) => setTimeout(resolve, 20));
  first.onMessage.emit({ status: "not_open" });
  first.onDisconnect.emit();

  assert.equal(first.messages.filter((m) => m.hello).length, 0);
  assert.equal(second.messages.filter((m) => m.hello).length, 1);
  assert.equal(second.messages[0].hello.account, "fresh@test");
  // Second port is still active and responds to status/control.
  second.onMessage.emit({ status: "connected" });
  await waitFor(() => second.messages.some((m) => m.control?.kind === "tabs"));
});

test("browser activity reconnects immediately out of backoff while respecting cooldown", async (t) => {
  let now = 100_000;
  t.mock.method(Date, "now", () => now);
  let saved = {};
  let local = {};
  const ports = [];
  const area = (read, write) => ({
    get: async () => structuredClone(read()),
    set: async (value) => write({ ...read(), ...structuredClone(value) }),
    remove: async (key) => { const value = { ...read() }; delete value[key]; write(value); },
  });
  const chrome = {
    storage: { session: area(() => saved, (v) => { saved = v; }), local: area(() => local, (v) => { local = v; }) },
    runtime: {
      lastError: null,
      onMessage: event(), onStartup: event(), onInstalled: event(), onUpdateAvailable: event(),
      connectNative: () => {
        const p = { onMessage: event(), onDisconnect: event(), messages: [], postMessage(m) { this.messages.push(m); } };
        ports.push(p);
        return p;
      },
      getManifest: () => ({ version: "0.2.2", update_url: "https://unused.test/" }),
      sendMessage: async () => {},
    },
    tabs: { query: async () => [], onCreated: event(), onActivated: event(), onUpdated: event(), onRemoved: event(), remove: async () => {} },
    debugger: { onEvent: event(), onDetach: event(), attach: async () => {}, detach: async () => {}, sendCommand: async () => ({}) },
    windows: { WINDOW_ID_NONE: -1, onCreated: event(), onFocusChanged: event(), onRemoved: event() },
    tabGroups: { update: async () => {} },
    alarms: { create: () => {}, onAlarm: event() },
    action: { setTitle: async () => {}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {} },
    i18n: { getMessage: (name) => name },
  };
  t.mock.method(globalThis, "fetch", async () => assert.fail("the worker must not fetch with a store manifest"));
  const before = globalThis.chrome;
  globalThis.chrome = chrome;
  t.after(() => { if (before === undefined) delete globalThis.chrome; else globalThis.chrome = before; });

  await import("./background.js?test=activity-reconnect");
  await waitFor(() => ports.length === 1);
  // Disconnect twice so exponential backoff timer is > 1s (and won't fire during this test).
  ports[0].onDisconnect.emit();
  chrome.alarms.onAlarm.emit({ name: "her-connect" });
  await waitFor(() => ports.length === 2);
  ports[1].onDisconnect.emit();

  // Within cooldown (< 1000ms): activity is ignored.
  now += 500;
  chrome.windows.onCreated.emit({ id: 1 });
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(ports.length, 2);

  // WINDOW_ID_NONE does not reconnect even after cooldown.
  now += 1500;
  chrome.windows.onFocusChanged.emit(-1);
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(ports.length, 2);

  // Focusing/opening a real window bursts multiple events -> exactly one immediate reconnect.
  chrome.windows.onCreated.emit({ id: 2 });
  chrome.windows.onFocusChanged.emit(2);
  chrome.tabs.onCreated.emit({ id: 50 });
  chrome.tabs.onActivated.emit({ tabId: 50, windowId: 2 });
  await waitFor(() => ports.length === 3);
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(ports.length, 3);

  // When host is missing ("not found"), cooldown is 10s so normal tab switching doesn't spam.
  chrome.runtime.lastError = { message: "Specified native messaging host not found." };
  ports[2].onDisconnect.emit();
  chrome.runtime.lastError = null;
  now += 2000;
  chrome.tabs.onActivated.emit({ tabId: 51, windowId: 2 });
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(ports.length, 3);
  now += 9000;
  chrome.tabs.onActivated.emit({ tabId: 52, windowId: 2 });
  await waitFor(() => ports.length === 4);
  ports[3].onMessage.emit({ status: "connected" });
  await waitFor(() => ports[3].messages.some((m) => m.control?.kind === "tabs"));
});

