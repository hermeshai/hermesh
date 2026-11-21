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

test("in-flight replies stay with their original port and browser stream", async (t) => {
  const ports = [];
  const pending = [];
  const storage = (value) => ({ get: async () => value, set: async () => {} });
  const chrome = {
    storage: {
      session: storage({ her: { agentTabs: [[1, { session: "test", url: "about:blank", title: "Test tab" }]] } }),
      local: storage({ herInstance: "test-profile" }),
    },
    runtime: {
      onMessage: event(), onStartup: event(), onInstalled: event(), onUpdateAvailable: event(),
      connectNative: () => {
        const current = { onMessage: event(), onDisconnect: event(), sent: [], postMessage(message) { this.sent.push(message); } };
        ports.push(current);
        return current;
      },
      getManifest: () => ({ version: "0.2.2", update_url: "https://unused.test/" }),
      sendMessage: async () => {},
    },
    tabs: { query: async () => [{ id: 1 }], onCreated: event(), onUpdated: event(), onRemoved: event() },
    debugger: {
      onEvent: event(), onDetach: event(), attach: async () => {}, detach: async () => {},
      sendCommand: (_target, method) => method === "Page.getFrameTree"
        ? new Promise((resolve, reject) => pending.push({ resolve, reject }))
        : Promise.resolve({}),
    },
    windows: { onRemoved: event() },
    alarms: { create: () => {}, onAlarm: event() },
    action: { setTitle: async () => {}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {} },
    i18n: { getMessage: (name) => name },
  };
  t.mock.method(globalThis, "fetch", async () => assert.fail("the worker must not make network requests"));
  const before = globalThis.chrome;
  globalThis.chrome = chrome;
  t.after(() => { if (before === undefined) delete globalThis.chrome; else globalThis.chrome = before; });
  await import("./background.js?test=connection-replies");
  await waitFor(() => ports[0]?.sent.some((message) => message.hello));

  let nextId = 0;
  const send = (port, stream, method, params = {}, sessionId) => {
    const id = ++nextId;
    port.onMessage.emit({ stream, data: { id, method, params, ...(sessionId ? { sessionId } : {}) } });
    return id;
  };
  const reply = async (port, id) => {
    await waitFor(() => port.sent.some((message) => message.data?.id === id));
    return port.sent.find((message) => message.data?.id === id).data;
  };
  const attach = async (port, stream) => {
    const listed = await reply(port, send(port, stream, "Target.getTargets", { herSession: "test" }));
    const targetId = listed.result.targetInfos[0].targetId;
    const attached = await reply(port, send(port, stream, "Target.attachToTarget", { targetId }));
    return attached.result.sessionId;
  };

  let port = ports[0];
  let stream = 1;
  port.onMessage.emit({ status: "connected" });
  let session = await attach(port, stream);
  for (const reset of ["native port", "service connection", "browser stream"]) {
    const staleResult = send(port, stream, "Page.getFrameTree", {}, session);
    const staleError = send(port, stream, "Page.getFrameTree", {}, session);
    assert.equal(pending.length, 2);
    const [result, error] = pending.splice(0);
    let staleClose;

    if (reset === "native port") {
      port.onDisconnect.emit();
      chrome.alarms.onAlarm.emit({ name: "her-connect" });
      await waitFor(() => ports[1]?.sent.some((message) => message.hello));
      port = ports[1];
      port.onMessage.emit({ status: "connected" });
    } else if (reset === "service connection") {
      port.onMessage.emit({ status: "waiting" });
      port.onMessage.emit({ status: "connected" });
    } else {
      staleClose = send(port, stream, "Browser.close");
      stream++;
    }
    session = await attach(port, stream);

    const current = send(port, stream, "Page.getFrameTree", {}, session);
    assert.equal(pending.length, 1);
    pending.shift().resolve({ marker: "current" });
    result.resolve({ marker: "expired" });
    error.reject(new Error("expired result"));
    assert.deepEqual((await reply(port, current)).result, { marker: "current" });
    const retired = [staleResult, staleError];
    if (staleClose !== undefined) retired.push(staleClose);
    assert.equal(ports.flatMap((candidate) => candidate.sent).some((message) =>
      retired.includes(message.data?.id)), false, reset);
  }
  await waitFor(() => port.sent.some((message) => message.control?.kind === "tabs"));
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

test("reveal with target_id activates only the exact session tab and detach/url change report immediately", async (t) => {
  const old = {
    agentTabs: [
      [21, { session: "s1", url: "https://one.example/login", title: "One" }],
      [22, { session: "s1", url: "https://two.example/", title: "Two" }],
      [23, { session: "s2", url: "https://other.example/", title: "Other" }],
    ],
  };
  let saved = { her: old };
  let local = {};
  const messages = [];
  const activated = [];
  const focused = [];
  let detached = [];
  const port = { onMessage: event(), onDisconnect: event(), postMessage: (m) => messages.push(m) };
  const area = (read, write) => ({
    get: async () => structuredClone(read()),
    set: async (value) => write({ ...read(), ...structuredClone(value) }),
    remove: async (key) => { const value = { ...read() }; delete value[key]; write(value); },
  });
  const chrome = {
    storage: { session: area(() => saved, (v) => { saved = v; }), local: area(() => local, (v) => { local = v; }) },
    runtime: {
      onMessage: event(), onStartup: event(), onInstalled: event(), onUpdateAvailable: event(),
      connectNative: () => port, getManifest: () => ({ version: "0.2.2", update_url: "https://unused.test/" }),
      sendMessage: async () => {},
    },
    tabs: {
      query: async () => [{ id: 21 }, { id: 22 }, { id: 23 }],
      update: async (tabId, props) => { activated.push({ tabId, props }); return { id: tabId, windowId: 700 + tabId }; },
      onCreated: event(), onActivated: event(), onUpdated: event(), onRemoved: event(),
      remove: async () => {},
    },
    debugger: {
      onEvent: event(), onDetach: event(),
      attach: async () => {},
      detach: async ({ tabId }) => { detached.push(tabId); },
      sendCommand: async () => ({}),
    },
    windows: {
      WINDOW_ID_NONE: -1, onCreated: event(), onFocusChanged: event(), onRemoved: event(),
      update: async (windowId, props) => { focused.push({ windowId, props }); },
    },
    tabGroups: { update: async () => {} },
    alarms: { create: () => {}, onAlarm: event() },
    action: { setTitle: async () => {}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {} },
    i18n: { getMessage: (name) => name },
  };
  t.mock.method(globalThis, "fetch", async () => assert.fail("the worker must not fetch with a store manifest"));
  const before = globalThis.chrome;
  globalThis.chrome = chrome;
  t.after(() => { if (before === undefined) delete globalThis.chrome; else globalThis.chrome = before; });

  await import("./background.js?test=chrome-takeover-isolation");
  await waitFor(() => messages.some((m) => m.hello));
  port.onMessage.emit({ status: "connected" });
  await waitFor(() => messages.some((m) => m.control?.kind === "tabs"));

  // Exact target_id reveals that exact tab.
  port.onMessage.emit({ control: { kind: "reveal", session_id: "s1", target_id: "T21" } });
  await waitFor(() => activated.length === 1 && focused.length === 1);
  assert.equal(activated[0].tabId, 21);
  assert.equal(focused[0].windowId, 721);

  // Target owned by another session or missing target must NOT fall back to another tab.
  port.onMessage.emit({ control: { kind: "reveal", session_id: "s1", target_id: "T23" } });
  port.onMessage.emit({ control: { kind: "reveal", session_id: "s1", target_id: "T999" } });
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(activated.length, 1);

  // Attach then release via Target.detachFromTarget { herRelease: true } detaches & reports immediately.
  const call = async (id, method, params = {}) => {
    port.onMessage.emit({ stream: 1, data: { id, method, params } });
    await waitFor(() => messages.some((m) => m.stream === 1 && m.data?.id === id));
    return messages.find((m) => m.stream === 1 && m.data?.id === id).data;
  };
  await call(1, "Target.getTargets", { herSession: "s1" });
  await call(2, "Target.attachToTarget", { targetId: "T21" });
  await waitFor(() =>
    messages
      .filter((m) => m.control?.kind === "tabs")
      .at(-1)
      ?.control?.sessions?.s1?.find((x) => x.target_id === "T21")?.attached === true,
  );
  const beforeDetach = messages.filter((m) => m.control?.kind === "tabs").length;
  await call(3, "Target.detachFromTarget", { targetId: "T21", herRelease: true });
  assert.deepEqual(detached, [21]);
  const afterDetach = messages.filter((m) => m.control?.kind === "tabs");
  assert.ok(afterDetach.length > beforeDetach, "detach must report tabs immediately");
  assert.equal(afterDetach.at(-1).control.sessions.s1.find((x) => x.target_id === "T21").attached, false);

  // URL change and tab removal report immediately (no 100ms stale window).
  const beforeUrl = messages.filter((m) => m.control?.kind === "tabs").length;
  chrome.tabs.onUpdated.emit(21, { url: "https://evil.example/phish" });
  const afterUrl = messages.filter((m) => m.control?.kind === "tabs");
  assert.ok(afterUrl.length > beforeUrl);
  assert.equal(afterUrl.at(-1).control.sessions.s1.find((x) => x.target_id === "T21").url, "https://evil.example/phish");

  const beforeRemove = messages.filter((m) => m.control?.kind === "tabs").length;
  chrome.tabs.onRemoved.emit(21);
  const afterRemove = messages.filter((m) => m.control?.kind === "tabs");
  assert.ok(afterRemove.length > beforeRemove);
  assert.equal(afterRemove.at(-1).control.sessions.s1.find((x) => x.target_id === "T21"), undefined);
  await waitFor(() => saved.her.agentTabs.length === 2);
});

test("inactive and minimized tabs stay alive, emulate focus, un-discard, and wake stalled screenshots", async (t) => {
  let saved = {};
  let local = {};
  const messages = [];
  const tabUpdates = [];
  const winUpdates = [];
  const reloaded = [];
  const cdpCalls = [];
  let winState = "minimized";
  let failCreateInExisting = false;
  let tabDiscarded = true;
  let resolveScreenshot;
  const port = { onMessage: event(), onDisconnect: event(), postMessage: (m) => messages.push(m) };
  const area = (read, write) => ({
    get: async () => structuredClone(read()),
    set: async (value) => write({ ...read(), ...structuredClone(value) }),
    remove: async (key) => { const value = { ...read() }; delete value[key]; write(value); },
  });
  let nextWin = 10;
  let nextTab = 20;
  const chrome = {
    storage: { session: area(() => saved, (v) => { saved = v; }), local: area(() => local, (v) => { local = v; }) },
    runtime: {
      onMessage: event(), onStartup: event(), onInstalled: event(), onUpdateAvailable: event(),
      connectNative: () => port,
      getManifest: () => ({ version: "0.2.2", update_url: "https://unused.test/" }),
      sendMessage: async () => {},
    },
    tabs: {
      query: async () => [],
      get: async (id) => ({ id, windowId: nextWin, discarded: tabDiscarded }),
      create: async (opts) => {
        if (failCreateInExisting) throw new Error("No window with id");
        return { id: ++nextTab, windowId: opts.windowId, url: opts.url };
      },
      update: async (id, props) => { tabUpdates.push([id, props]); return { id, windowId: nextWin }; },
      reload: async (id) => { reloaded.push(id); tabDiscarded = false; },
      group: async () => 1,
      remove: async () => {},
      onCreated: event(), onActivated: event(), onUpdated: event(), onRemoved: event(),
    },
    debugger: {
      onEvent: event(), onDetach: event(),
      attach: async () => {},
      detach: async () => {},
      sendCommand: (target, method, params) => {
        cdpCalls.push({ target, method, params });
        if (method === "Page.captureScreenshot") {
          return new Promise((resolve) => { resolveScreenshot = resolve; });
        }
        return Promise.resolve({});
      },
    },
    windows: {
      WINDOW_ID_NONE: -1,
      get: async (id) => ({ id, state: winState }),
      create: async ({ url }) => {
        const id = ++nextWin;
        winState = "normal";
        return { id, tabs: [{ id: ++nextTab, windowId: id, url }] };
      },
      update: async (id, props) => {
        winUpdates.push([id, props]);
        if (props.state) winState = props.state;
        return { id, state: winState };
      },
      onCreated: event(), onFocusChanged: event(), onRemoved: event(),
    },
    tabGroups: { update: async () => {} },
    alarms: { create: () => {}, onAlarm: event() },
    action: { setTitle: async () => {}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {} },
    i18n: { getMessage: (name) => name },
  };
  t.mock.method(globalThis, "fetch", async () => assert.fail("the worker must not fetch with a store manifest"));
  const before = globalThis.chrome;
  globalThis.chrome = chrome;
  t.after(() => { if (before === undefined) delete globalThis.chrome; else globalThis.chrome = before; });

  await import("./background.js?test=inactive-hardening");
  await waitFor(() => messages.some((m) => m.hello));
  assert.equal(messages.find((m) => m.hello).hello.heartbeat, true);

  // Heartbeat ping gets immediate pong without touching streams.
  port.onMessage.emit({ ping: 42 });
  assert.ok(messages.some((m) => m.pong === 42));

  port.onMessage.emit({ status: "connected" });
  let id = 0;
  const call = async (method, params = {}, sessionId) => {
    const n = ++id;
    port.onMessage.emit({ stream: 1, data: { id: n, method, params, ...(sessionId ? { sessionId } : {}) } });
    await waitFor(() => messages.some((m) => m.stream === 1 && m.data?.id === n));
    return messages.find((m) => m.stream === 1 && m.data?.id === n).data;
  };

  // Creating a tab sets autoDiscardable: false; attaching reloads a discarded tab and enables focus emulation.
  const created = await call("Target.createTarget", { url: "about:blank", herSession: "s1" });
  const tabId = Number(created.result.targetId.slice(1));
  assert.ok(tabUpdates.some(([tId, props]) => tId === tabId && props.autoDiscardable === false));

  const attached = await call("Target.attachToTarget", { targetId: created.result.targetId });
  const sessionId = attached.result.sessionId;
  assert.deepEqual(reloaded, [tabId], "discarded tab is reloaded before debugger attach");
  assert.ok(cdpCalls.some((c) => c.target.tabId === tabId && c.method === "Emulation.setFocusEmulationEnabled" && c.params.enabled === true));

  // Minimized window is restored without stealing focus on Target.activateTarget.
  winState = "minimized";
  winUpdates.length = 0;
  await call("Target.activateTarget", { targetId: created.result.targetId });
  assert.deepEqual(winUpdates, [[nextWin, { state: "normal", focused: false }]]);
  assert.ok(tabUpdates.some(([tId, props]) => tId === tabId && props.active === true));

  // If the existing agent window vanished right as createTarget runs, it falls back to a fresh window.
  failCreateInExisting = true;
  const second = await call("Target.createTarget", { url: "about:blank", herSession: "s1" });
  assert.ok(second.result.targetId);
  failCreateInExisting = false;

  // Fast screenshot finishes without focusing the OS window.
  winUpdates.length = 0;
  const fastId = ++id;
  port.onMessage.emit({ stream: 1, data: { id: fastId, method: "Page.captureScreenshot", params: { format: "png" }, sessionId } });
  await waitFor(() => typeof resolveScreenshot === "function");
  resolveScreenshot({ data: "ZmFzdA==" });
  resolveScreenshot = null;
  await waitFor(() => messages.some((m) => m.stream === 1 && m.data?.id === fastId));
  assert.equal(winUpdates.length, 0, "fast screenshot does not steal focus");
  assert.equal(cdpCalls.some((c) => c.method === "Page.bringToFront"), false);

  // A stalled screenshot (e.g. occluded window) wakes the window once after the wake delay.
  const origSetTimeout = globalThis.setTimeout;
  const scheduled = [];
  t.mock.method(globalThis, "setTimeout", (fn, delay, ...args) => {
    if (delay === 4000) {
      const handle = { cancelled: false, fn: () => { if (!handle.cancelled) fn(...args); } };
      scheduled.push(handle);
      return handle;
    }
    return origSetTimeout(fn, delay, ...args);
  });
  const origClearTimeout = globalThis.clearTimeout;
  t.mock.method(globalThis, "clearTimeout", (handle) => {
    if (handle && typeof handle === "object" && "cancelled" in handle) {
      handle.cancelled = true;
      return;
    }
    return origClearTimeout(handle);
  });
  const slowId = ++id;
  port.onMessage.emit({ stream: 1, data: { id: slowId, method: "Page.captureScreenshot", params: { format: "png" }, sessionId } });
  await waitFor(() => typeof resolveScreenshot === "function" && scheduled.length === 1);
  scheduled[0].fn();
  await waitFor(() => cdpCalls.some((c) => c.method === "Page.bringToFront"));
  assert.ok(winUpdates.some(([, props]) => props.state === "normal" && props.focused === true));
  resolveScreenshot({ data: "d29rZQ==" });
  resolveScreenshot = null;
  await waitFor(() => messages.some((m) => m.stream === 1 && m.data?.id === slowId));

  // Let debounced storage/tab reports finish before tearing down globalThis.chrome.
  await waitFor(() => saved.her?.agentTabs?.length === 2);
});


