import assert from "node:assert/strict";
import test from "node:test";

import { headline, listed, offersTab } from "./panel.js";

const state = (over = {}) => ({ host: "connected", inUse: true, stuck: false, sessions: [], offerable: true, ...over });
const running = { id: "a", title: "A", state: "running", tabs: 1 };
const approval = { id: "b", title: "B", state: "awaiting_approval", tabs: 1 };
const handoff = { id: "c", title: "C", state: "awaiting_approval", handoff: "sign in", tabs: 1 };

test("every state says one thing and asks at most one", () => {
  const states = [
    state(),
    state({ inUse: false }),
    state({ sessions: [approval] }),
    state({ host: "connecting" }),
    state({ host: "connecting", stuck: true }),
    state({ host: "not_open" }),
    state({ host: "missing" }),
    state({ host: "outdated", update: "extension" }),
    state({ host: "outdated", update: "her" }),
  ];
  for (const s of states) {
    const line = headline(s);
    assert.equal(typeof line.title, "string", JSON.stringify(s));
    assert.ok(!line.action || typeof line.action.do === "string", JSON.stringify(s));
  }
});

test("a worker that does not answer still gets a line and a way out", () => {
  const line = headline(null);
  assert.equal(line.title, "statusNoAnswer");
  assert.equal(line.action.do, "reload");
  assert.deepEqual(listed(null), []);
  assert.equal(offersTab(null), false);
});

test("every name the page shows is in each language", async () => {
  const { readFileSync } = await import("node:fs");
  const names = [null, ...["connecting", "not_open", "missing", "outdated"].map((host) => state({ host })), state({ host: "connecting", stuck: true }), state({ inUse: false })]
    .map(headline)
    .flatMap((l) => [l.title, l.hint, l.action?.label].filter(Boolean));
  for (const lang of ["en", "zh_CN"]) {
    const messages = JSON.parse(readFileSync(new URL(`../_locales/${lang}/messages.json`, import.meta.url), "utf8"));
    for (const name of names) assert.ok(messages[name]?.message, `${lang}: ${name}`);
  }
});

test("a connection only just lost is not a problem yet", () => {
  assert.equal(headline(state({ host: "connecting" })).action, undefined);
  assert.equal(headline(state({ host: "connecting", stuck: true })).action.do, "retry");
});

test("the update asked for is of the side that is too old", () => {
  assert.equal(headline(state({ host: "outdated", update: "extension" })).action.do, "update");
  assert.equal(headline(state({ host: "outdated", update: "her" })).action.do, "open_app");
  assert.equal(headline(state({ host: "outdated" })).action.do, "update", "an older host does not say");
});

test("the top counts who waits", () => {
  assert.equal(headline(state({ sessions: [running] })).title, "statusConnected");
  assert.equal(headline(state({ sessions: [running, handoff] })).title, "waitingOne");
  assert.deepEqual(headline(state({ sessions: [approval, handoff] })), { dot: "warn", title: "waiting", count: 2 });
});

test("standing by comes before who waits: the choice of browser decides the rest", () => {
  assert.equal(headline(state({ inUse: false, sessions: [approval] })).action.do, "use_this");
});

test("conversations show on a working connection, who waits first", () => {
  assert.deepEqual(listed(state({ sessions: [running, handoff] })).map((s) => s.id), ["c", "a"]);
  for (const host of ["connecting", "not_open", "missing", "outdated"]) {
    assert.deepEqual(listed(state({ host, sessions: [running] })), [], host);
  }
});

test("a tab is offered only when nothing else asks for the user", () => {
  assert.equal(offersTab(state({ sessions: [running] })), true);
  assert.equal(offersTab(state({ sessions: [handoff] })), false);
  assert.equal(offersTab(state({ offerable: false })), false);
  assert.equal(offersTab(state({ inUse: false })), false);
  assert.equal(offersTab(state({ host: "not_open" })), false);
});
