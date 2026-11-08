import assert from "node:assert/strict";
import { test } from "node:test";
import { ExtensionBrowser } from "./browser.js";

/** Tabs that answer every command with its name, and remember what happened to them. */
function fakeTabs() {
  let nextTab = 100;
  const log = [];
  const sent = [];
  const kept = [];
  const tabs = {
    released: (label) => kept.filter((t) => t.label === label),
    create: async (url, label) => (log.push(["create", url, label]), nextTab++),
    adopt: (tabId, label) => void log.push(["adopt", tabId, label]),
    close: (tabId) => void log.push(["close", tabId]),
    activate: (tabId) => void log.push(["activate", tabId]),
    attach: async (tabId) => void log.push(["attach", tabId]),
    detach: (tabId) => void log.push(["detach", tabId]),
    send: async (tabId, child, method, params) => (sent.push({ tabId, child, method, params }), { echo: method }),
  };
  return { tabs, log, sent, kept };
}

function browser() {
  const fake = fakeTabs();
  const out = [];
  const b = new ExtensionBrowser(fake.tabs, (message) => out.push(message));
  let id = 0;
  const call = async (method, params = {}, sessionId) => {
    const n = ++id;
    b.receive({ id: n, method, params, ...(sessionId ? { sessionId } : {}) });
    for (let i = 0; i < 50 && !out.some((m) => m.id === n); i++) await new Promise((r) => setTimeout(r, 1));
    return out.find((m) => m.id === n);
  };
  const events = (method) => out.filter((m) => m.method === method);
  const open = async (herSession) => {
    const targetId = (await call("Target.createTarget", { url: "about:blank", herSession })).result.targetId;
    const sessionId = (await call("Target.attachToTarget", { targetId, flatten: true })).result.sessionId;
    return { targetId, sessionId };
  };
  return { b, fake, out, call, events, open };
}

test("tabs are created in the agent's group, attached and driven through their debugger", async () => {
  const { b, fake, call, events, open } = browser();
  const { targetId, sessionId } = await open("s1");
  assert.deepEqual(fake.log.slice(0, 2), [["create", "about:blank", "s1"], ["attach", 100]]);
  assert.equal(targetId, "T100");
  assert.equal(events("Target.targetCreated")[0].params.targetInfo.targetId, targetId);
  assert.equal(events("Target.attachedToTarget")[0].params.sessionId, sessionId);

  const navigated = await call("Page.navigate", { url: "https://example.com" }, sessionId);
  assert.deepEqual(navigated.result, { echo: "Page.navigate" });
  assert.deepEqual(fake.sent.at(-1), { tabId: 100, child: undefined, method: "Page.navigate", params: { url: "https://example.com" } });

  // A debugger event of the tab comes back under the kernel's session.
  b.event(100, undefined, "Page.loadEventFired", { timestamp: 1 });
  assert.equal(events("Page.loadEventFired")[0].sessionId, sessionId);
  // An iframe the tab attached keeps its own session, both ways.
  b.event(100, undefined, "Target.attachedToTarget", { sessionId: "child-1", targetInfo: { type: "iframe" } });
  b.event(100, "child-1", "Page.frameNavigated", { frame: { id: "F2" } });
  assert.equal(events("Page.frameNavigated")[0].sessionId, "child-1");
  await call("DOM.resolveNode", { backendNodeId: 3 }, "child-1");
  assert.equal(fake.sent.at(-1).child, "child-1");
  await call("Target.detachFromTarget", { sessionId: "child-1" });
  assert.deepEqual(fake.sent.at(-1), { tabId: 100, child: undefined, method: "Target.detachFromTarget", params: { sessionId: "child-1" } });

  b.tabUpdated(100, { url: "https://example.com/", title: "Example" });
  const info = (await call("Target.getTargetInfo", { targetId })).result.targetInfo;
  assert.deepEqual([info.url, info.title, info.attached], ["https://example.com/", "Example", true]);
  await call("Target.activateTarget", { targetId });
  assert.deepEqual(fake.log.at(-1), ["activate", 100]);
});

test("commands off the list are refused and never reach a tab", async () => {
  const { fake, call, open } = browser();
  const { sessionId } = await open();
  const refused = await call("Network.getAllCookies", {}, sessionId);
  assert.match(refused.error.message, /not available/);
  assert.match((await call("Storage.getCookies")).error.message, /not available/);
  assert.equal(fake.sent.length, 0);
  assert.match((await call("Page.navigate", {}, "nope")).error.message, /No session/);
});

test("only tabs opened from the agent's tabs become the agent's; the user's stay unseen", async () => {
  const { b, fake, events, open } = browser();
  const { targetId } = await open("s1");
  b.tabCreated(7, undefined, "https://mail.example.com/");
  b.tabCreated(8, 55, "https://other.example.com/");
  assert.equal(events("Target.targetCreated").length, 1, "the user's own tabs are not reported");
  b.tabCreated(9, 100, "https://popup.example.com/");
  const created = events("Target.targetCreated").at(-1).params.targetInfo;
  assert.deepEqual([created.targetId, created.openerId, created.url], ["T9", targetId, "https://popup.example.com/"]);
  assert.deepEqual(fake.log.at(-1), ["adopt", 9, "s1"]);
  // The user's tab never gets events through, even if Chrome sent some.
  b.event(7, undefined, "Page.loadEventFired", {});
  assert.equal(events("Page.loadEventFired").length, 0);
});

test("closing, the user taking a tab back, and the kernel leaving", async () => {
  const { b, fake, call, events, open } = browser();
  const first = await open("s1");
  const second = await open("s1");
  const third = await open("s2");

  // The kernel closes a tab: it is closed in Chrome.
  assert.deepEqual((await call("Target.closeTarget", { targetId: first.targetId })).result, { success: true });
  assert.deepEqual(fake.log.at(-1), ["close", 100]);
  assert.equal(events("Target.detachedFromTarget")[0].params.sessionId, first.sessionId);
  b.tabRemoved(100);
  assert.equal(events("Target.targetDestroyed").length, 1, "closing twice reports once");

  // The user closes the debugging bar: the tab stays open but is no longer the agent's.
  b.detached(101, "canceled_by_user");
  assert.match(events("Target.detachedFromTarget")[1].params.reason, /user stopped/);
  assert.ok(!fake.log.some(([what, tab]) => what === "close" && tab === 101));
  assert.match((await call("Page.reload", {}, second.sessionId)).error.message, /No session/);

  // The kernel goes away: the rest are let go and left open.
  b.close();
  assert.deepEqual(fake.log.at(-1), ["detach", 102]);
  assert.ok(!fake.log.some(([what, tab]) => what === "close" && tab === 102));
  const before = events("Target.targetDestroyed").length;
  b.tabRemoved(102);
  assert.equal(events("Target.targetDestroyed").length, before);
  void third;
});

test("a tab closed while the kernel attaches is not left attached", async () => {
  const { b, fake, call } = browser();
  const targetId = (await call("Target.createTarget", { url: "about:blank" })).result.targetId;
  fake.tabs.attach = async (tabId) => {
    fake.log.push(["attach", tabId]);
    b.tabRemoved(tabId);
  };
  const attached = await call("Target.attachToTarget", { targetId });
  assert.match(attached.error.message, /closed/);
  assert.deepEqual(fake.log.at(-1), ["detach", 100]);
});

test("a session that ends lets its tab go, and a later browser finds it", async () => {
  const first = browser();
  const { targetId } = await first.open("s1");
  const released = await first.call("Target.detachFromTarget", { targetId, herRelease: true });
  assert.deepEqual(released.result, {});
  assert.ok(first.fake.log.some((e) => e[0] === "detach" && e[1] === 100), "the debugger lets go");
  assert.ok(!first.fake.log.some((e) => e[0] === "close"), "the tab stays open");
  assert.equal(first.b.byTab.size, 0);

  // Another browser of the kernel: only that session's tabs, and they can be attached.
  const next = browser();
  next.fake.kept.push({ tabId: 100, label: "s1", url: "https://example.com/a", title: "A" }, { tabId: 101, label: "s2", url: "https://example.com/b", title: "B" });
  const listed = await next.call("Target.getTargets", { herSession: "s1" });
  assert.deepEqual(listed.result.targetInfos.map((t) => [t.targetId, t.url, t.title, t.attached]), [["T100", "https://example.com/a", "A", false]]);
  const attached = await next.call("Target.attachToTarget", { targetId: "T100", flatten: true });
  assert.ok(attached.result.sessionId);
  assert.ok(next.fake.log.some((e) => e[0] === "attach" && e[1] === 100));
  const again = await next.call("Target.getTargets", { herSession: "s1" });
  assert.deepEqual(again.result.targetInfos, [], "an attached tab is not offered again");
  assert.deepEqual((await next.call("Target.getTargets", {})).result.targetInfos, [], "nothing without a session");
});

test("the user taking control back says why, and keeps the tab", async () => {
  const { b, fake, events, open } = browser();
  await open("s1");
  b.detached(100, "canceled_by_user");
  assert.match(events("Target.detachedFromTarget")[0].params.reason, /user stopped/);
  assert.ok(!fake.log.some((e) => e[0] === "close"));
});

