import { create } from "@bufbuild/protobuf";
import { SessionLineageSchema, SessionStatus, type Session } from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import {
  childrenOf,
  CHILDREN_INITIAL_LIMIT,
  HER_SECTION,
  herParentOf,
  isHerConversation,
  isTreeArchivable,
  searchSessions,
  sessionFilter,
  sessionSections,
  sessionView,
  sliceChildren,
  sliceSectionSessions,
  todayStats,
  treeMark,
  treeStatus,
} from "./sessions";
import { noticeDiff, noticeSnapshots } from "./notices";
import { teamState } from "./team";
import { now, session } from "./testing";

// Her's conversations and the work she sent from them (docs/her-conversations.md §3.4, §3.5).

const her = (id: string, status: SessionStatus, o: Parameters<typeof session>[2] = {}) =>
  session(id, status, { agent: "her", project: "scratch", by: "owner", machine: "mHer", ...o });
const sent = (id: string, parent: string, status: SessionStatus, o: Parameters<typeof session>[2] = {}) => {
  const s = session(id, status, { by: "cHer", ...o });
  s.lineage = create(SessionLineageSchema, { parentSessionId: parent, relationship: "her" });
  return s;
};
const keys = (secs: { key: string }[]) => secs.map((s) => s.key);
const ids = (list: Session[]) => list.map((s) => s.sessionId);
const opts = { archived: false, now, her: ["cHer"] };

describe("Her's conversations", () => {
  test("a conversation is Her's; work names the conversation it was sent from, a sub-agent does not", () => {
    const sub = session("sub", SessionStatus.IDLE);
    sub.lineage = create(SessionLineageSchema, { parentSessionId: "x", relationship: "subagent" });
    expect(isHerConversation(her("h", SessionStatus.IDLE))).toBe(true);
    expect(isHerConversation(sub)).toBe(false);
    expect(herParentOf(sent("t", "h", SessionStatus.IDLE))).toBe("h");
    expect(herParentOf(sub)).toBeUndefined();
  });

  test("nested by status: the work goes with its conversation, which is placed by all of it", () => {
    const done = her("plan", SessionStatus.IDLE, { updated: -3000 });
    const waits = sent("bench", "plan", SessionStatus.WAITING_INPUT, { updated: -60 });
    const runs = sent("e2e", "plan", SessionStatus.RUNNING, { updated: -30 });
    const quiet = her("week", SessionStatus.IDLE, { updated: -86400 });
    const other = session("react", SessionStatus.RUNNING);
    const all = [done, waits, runs, quiet, other];
    const secs = sessionSections(all, sessionView(), opts);
    expect(keys(secs)).toEqual(["attention", "active", "done"]);
    expect(ids(secs[0].sessions)).toEqual(["plan"]); // its work waits on the owner
    expect(ids(secs[1].sessions)).toEqual(["react"]);
    expect(ids(secs[2].sessions)).toEqual(["week"]);
    expect(ids(childrenOf(all, sessionView()).get("plan")!)).toEqual(["e2e", "bench"]); // newest first
  });

  test("a conversation runs while its work runs, and needs you while any of it does", () => {
    const h = her("h", SessionStatus.IDLE);
    expect(treeStatus(h, [sent("a", "h", SessionStatus.IDLE)])).toBe("done");
    expect(treeStatus(h, [sent("a", "h", SessionStatus.RUNNING)])).toBe("active");
    expect(treeStatus(h, [sent("a", "h", SessionStatus.RUNNING), sent("b", "h", SessionStatus.WAITING_INPUT)])).toBe("attention");
    expect(treeMark(h, [sent("b", "h", SessionStatus.WAITING_INPUT)])).toBe("waiting");
    expect(treeMark(h, [sent("b", "h", SessionStatus.CRASHED)])).toBe("failed");
    expect(treeMark(h, [sent("b", "h", SessionStatus.RUNNING)])).toBeUndefined();
  });

  test("nested by date: a conversation is as recent as its latest work", () => {
    const old = her("old", SessionStatus.IDLE, { updated: -5 * 86400 });
    const fresh = sent("fresh", "old", SessionStatus.IDLE, { updated: -60 });
    const secs = sessionSections([old, fresh], sessionView({ group: "date" }), opts);
    expect(keys(secs)).toEqual(["today"]);
    expect(ids(secs[0].sessions)).toEqual(["old"]);
  });

  test("by project: Her's conversations are a section of their own, first; nested, their work with them", () => {
    const h = her("h", SessionStatus.IDLE);
    const w = sent("w", "h", SessionStatus.IDLE, { project: "web" });
    const a = session("a", SessionStatus.IDLE, { project: "api" });
    const secs = sessionSections([h, w, a], sessionView({ group: "project" }), opts);
    expect(keys(secs)).toEqual([HER_SECTION, "api"]);
    expect(ids(secs[0].sessions)).toEqual(["h"]);
    // Not nested: the work goes back to its project, and Her's conversations stay first.
    const flat = sessionSections([h, w, a], sessionView({ group: "project", nest: false }), opts);
    expect(keys(flat)).toEqual([HER_SECTION, "api", "web"]);
    expect(childrenOf([h, w, a], sessionView({ nest: false })).size).toBe(0);
  });

  test("by agent, Her comes first; by sender, a conversation is the owner's and its work goes with it", () => {
    const h = her("h", SessionStatus.IDLE);
    const w = sent("w", "h", SessionStatus.IDLE, { agent: "codex" });
    const c = session("c", SessionStatus.IDLE, { agent: "claude" });
    expect(keys(sessionSections([h, w, c], sessionView({ group: "agent" }), opts))).toEqual(["her", "claude"]);
    expect(keys(sessionSections([h, w, c], sessionView({ group: "agent", nest: false }), opts))).toEqual(["her", "claude", "codex"]);
    const bySender = sessionSections([h, w, c], sessionView({ group: "sender" }), opts);
    expect(keys(bySender)).toEqual(["c1", "owner"]);
    // Work Her sent from her main conversation has no conversation to go under.
    const main = sent("m", "her-main", SessionStatus.IDLE);
    expect(keys(sessionSections([h, w, main], sessionView({ group: "sender" }), opts))).toEqual(["cHer", "owner"]);
  });

  test("a pinned conversation is pinned with its work; work is never pinned on its own while nested", () => {
    const h = her("h", SessionStatus.IDLE);
    h.pinned = true;
    const w = sent("w", "h", SessionStatus.RUNNING);
    w.pinned = true;
    const secs = sessionSections([h, w], sessionView(), opts);
    expect(keys(secs)).toEqual(["pinned"]);
    expect(ids(secs[0].sessions)).toEqual(["h"]);
  });

  test("an archived conversation or piece of work is listed on its own", () => {
    const h = her("h", SessionStatus.ARCHIVED);
    const w = sent("w", "h", SessionStatus.IDLE);
    expect(childrenOf([h, w], sessionView()).size).toBe(0);
    expect(ids(sessionSections([h, w], sessionView(), opts).flatMap((s) => s.sessions))).toEqual(["w"]);
  });

  test("more than five: five show, with whatever runs or waits among them", () => {
    const kids = Array.from({ length: 8 }, (_, i) => sent(`k${i}`, "h", SessionStatus.IDLE, { updated: -60 * (i + 1) }));
    kids[7].status = SessionStatus.RUNNING; // the oldest runs
    const folded = sliceChildren(kids, false);
    expect(folded.visible).toHaveLength(CHILDREN_INITIAL_LIMIT);
    expect(ids(folded.visible)).toContain("k7");
    expect(folded.hiddenCount).toBe(3);
    expect(sliceChildren(kids, true).hiddenCount).toBe(0);
    expect(sliceChildren(kids.slice(0, 5), false).hiddenCount).toBe(0);
  });

  test("a conversation is archived with its work, so only once all of it has stopped", () => {
    const h = her("h", SessionStatus.IDLE);
    expect(isTreeArchivable(h, [sent("a", "h", SessionStatus.IDLE)])).toBe(true);
    expect(isTreeArchivable(h, [sent("a", "h", SessionStatus.RUNNING)])).toBe(false);
  });

  test("with running or waiting child cards or sub-agents, a conversation never reads as Done in flat view, overview, search, stats or notices", () => {
    const h = her("h", SessionStatus.IDLE, { updated: -120 });
    const runChild = sent("c_run", "h", SessionStatus.RUNNING, { updated: -30 });
    const settledOther = session("done_1", SessionStatus.IDLE, { updated: -10 });

    // Flat by-status: both the Her conversation and its running child stay in "active", not "done".
    const flatByStatus = sessionSections([h, runChild, settledOther], sessionView({ group: "status", nest: false }), opts);
    expect(keys(flatByStatus)).toEqual(["active", "done"]);
    expect(ids(flatByStatus[0].sessions)).toEqual(["h", "c_run"]);
    expect(ids(flatByStatus[1].sessions)).toEqual(["done_1"]);

    // Overview / teamState: h is in running (not finished) while c_run runs; when a child waits on the owner, h is still not in finished.
    const tRun = teamState([h, runChild, settledOther], 0);
    expect(ids(tRun.running)).toContain("h");
    expect(ids(tRun.finished)).toEqual(["done_1"]);

    const waitChild = sent("c_wait", "h", SessionStatus.WAITING_INPUT, { updated: -20 });
    const tWait = teamState([h, waitChild, settledOther], 0);
    expect(ids(tWait.waiting)).toEqual(["c_wait"]);
    expect(ids(tWait.finished)).toEqual(["done_1"]);

    // Search ranks active Her conversation ahead of newer settled work; Today stats do not count h as done yet.
    expect(ids(searchSessions([settledOther, h, runChild], "", sessionFilter(), (s) => s.sessionId))).toEqual(["c_run", "h", "done_1"]);
    expect(todayStats([h, runChild, settledOther], now).done).toBe(1);

    // Entity section folding keeps active Her conversation past the 5-item cap.
    const many = Array.from({ length: 7 }, (_, i) => her(`h_${i}`, SessionStatus.IDLE, { updated: -10 * (i + 1) }));
    const lateKids = new Map([[many[6].sessionId, [sent("c_late", many[6].sessionId, SessionStatus.RUNNING)]]]);
    expect(ids(sliceSectionSessions(many, "project", 5, null, lateKids).visible)).toContain(many[6].sessionId);

    // Turn-done notice is suppressed while child cards or background sub-agents are still active.
    const hBefore = her("h", SessionStatus.RUNNING, { turns: 0 });
    const prev = noticeSnapshots([hBefore]);
    const hDispatched = her("h", SessionStatus.IDLE, { turns: 1 });
    expect(noticeDiff(prev, [hDispatched, runChild], () => "You")).toEqual([]);
    const hSubagent = her("h", SessionStatus.IDLE, { turns: 1 });
    hSubagent.activeBackgroundCount = 1;
    expect(noticeDiff(prev, [hSubagent], () => "You")).toEqual([]);
  });
});
