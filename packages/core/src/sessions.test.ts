import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { DiffStatSchema, FailureKind, FailureSchema, SessionCursorSchema, SessionStatus, UsageSchema, type Session } from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import {
  createdDate,
  currentTurn,
  dateOf,
  dayOf,
  isFailed,
  mergeSessions,
  needsAttention,
  pendingQuestion,
  relistedCursor,
  searchSessions,
  SECTION_EXPAND_STEP,
  SECTION_INITIAL_LIMIT,
  sessionFilter,
  sessionSections,
  sessionView,
  sliceSectionSessions,
  statusOf,
  todayStats,
  updatedDate,
} from "./sessions";
import { ask, now, progressOf, session } from "./testing";

const ids = (list: Session[]) => list.map((s) => s.sessionId);

describe("session list", () => {
  test("by status (the default): needs you, running, done; archived only on request, last", () => {
    const waiting = session("w", SessionStatus.WAITING_INPUT, { updated: -600 });
    ask(waiting);
    const all = [
      session("today", SessionStatus.IDLE, { updated: -3600 }),
      session("old", SessionStatus.IDLE, { updated: -30 * 86400 }),
      session("run", SessionStatus.RUNNING),
      session("crash", SessionStatus.CRASHED, { updated: -120 }),
      waiting,
      session("arch", SessionStatus.ARCHIVED, { updated: -100 }),
    ];
    const secs = sessionSections(all, sessionView(), { archived: false, now });
    expect(secs.map((s) => s.key)).toEqual(["attention", "active", "done"]);
    // Each section by last activity, newest first: the failure was active after the question.
    expect(ids(secs[0].sessions)).toEqual(["crash", "w"]);
    expect(ids(secs[2].sessions)).toEqual(["today", "old"]);

    const withArchived = sessionSections(all, sessionView(), { archived: true, now });
    expect(withArchived.at(-1)).toMatchObject({ grouping: "archived", key: "archived" });
    expect(ids(withArchived.at(-1)!.sessions)).toEqual(["arch"]);

    // Oldest first turns each section around; the states keep their order.
    const oldest = sessionSections(all, sessionView({ newestFirst: false }), { archived: false, now });
    expect(oldest.map((s) => s.key)).toEqual(["attention", "active", "done"]);
    expect(ids(oldest[0].sessions)).toEqual(["w", "crash"]);
  });

  test("by status, the sidebar keeps the running section while nothing runs, empty; with no session, none", () => {
    const done = session("d", SessionStatus.IDLE);
    const waiting = session("w", SessionStatus.WAITING_INPUT);
    ask(waiting);
    const keep = (list: Session[], view = sessionView()) => sessionSections(list, view, { archived: true, now, alwaysActive: true }).map((s) => [s.key, ids(s.sessions)]);
    expect(keep([done])).toEqual([["active", []], ["done", ["d"]]]);
    expect(keep([waiting, done])).toEqual([["attention", ["w"]], ["active", []], ["done", ["d"]]]);
    expect(keep([])).toEqual([]);
    expect(keep([session("a", SessionStatus.ARCHIVED)])).toEqual([["archived", ["a"]]]);
    // Other groupings, and without the option, have no empty section.
    expect(keep([done], sessionView({ group: "agent" }))).toEqual([["claude", ["d"]]]);
    expect(sessionSections([done], sessionView(), { archived: false, now }).map((s) => s.key)).toEqual(["done"]);
  });

  test("by date, the days follow the sort", () => {
    const all = [
      session("today", SessionStatus.IDLE, { updated: -3600 }),
      session("yday", SessionStatus.IDLE, { updated: -86400 }),
      session("week", SessionStatus.IDLE, { updated: -4 * 86400 }),
      session("old", SessionStatus.IDLE, { updated: -30 * 86400 }),
      session("run", SessionStatus.RUNNING, { updated: -10 }),
    ];
    const secs = sessionSections(all, sessionView({ group: "date" }), { archived: false, now });
    expect(secs.map((s) => s.key)).toEqual(["today", "yesterday", "week", "earlier"]);
    expect(ids(secs[0].sessions)).toEqual(["run", "today"]);
    const oldest = sessionSections(all, sessionView({ group: "date", newestFirst: false }), { archived: false, now });
    expect(oldest.map((s) => s.key)).toEqual(["earlier", "week", "yesterday", "today"]);
  });

  test("by project and by coding agent, by name; by machine, in the order given", () => {
    const all = [
      session("a", SessionStatus.IDLE, { project: "web", agent: "gemini", machine: "m2", updated: -10 }),
      session("b", SessionStatus.IDLE, { project: "api", agent: "claude", machine: "m1", updated: -20 }),
      session("c", SessionStatus.IDLE, { project: "web", agent: "codex", machine: "m3", updated: -30 }),
    ];
    const projects = sessionSections(all, sessionView({ group: "project" }), { archived: false, now });
    expect(projects.map((s) => s.key)).toEqual(["api", "web"]);
    expect(ids(projects[1].sessions)).toEqual(["a", "c"]);
    expect(sessionSections(all, sessionView({ group: "agent" }), { archived: false, now }).map((s) => s.key)).toEqual(["claude", "codex", "gemini"]);
    expect(sessionSections(all, sessionView({ group: "machine" }), { archived: false, now, machines: ["m2", "m1"] }).map((s) => s.key)).toEqual(["m2", "m1", "m3"]);
  });

  test("entity sections fold past five while status, date and pinned sessions stay visible", () => {
    const ten = Array.from({ length: 10 }, (_, i) => session(`s${i + 1}`, SessionStatus.IDLE, { updated: -(i + 1) * 10 }));
    for (const g of ["status", "date", "archived"] as const) {
      expect(sliceSectionSessions(ten, g)).toEqual({ visible: ten, hiddenCount: 0 });
    }
    // 6 rows never fold for a single orphan row; 7 rows fold to 5 + 2.
    const six = ten.slice(0, 6);
    expect(sliceSectionSessions(six, "project")).toEqual({ visible: six, hiddenCount: 0 });
    const seven = ten.slice(0, 7);
    const folded7 = sliceSectionSessions(seven, "project");
    expect([ids(folded7.visible), folded7.hiddenCount]).toEqual([["s1", "s2", "s3", "s4", "s5"], 2]);

    for (const g of ["project", "agent", "machine", "sender"] as const) {
      const f = sliceSectionSessions(ten, g);
      expect([f.visible.length, f.hiddenCount]).toEqual([SECTION_INITIAL_LIMIT, 5]);
      const expanded = sliceSectionSessions(ten, g, SECTION_INITIAL_LIMIT + SECTION_EXPAND_STEP);
      expect([expanded.visible.length, expanded.hiddenCount]).toEqual([10, 0]);
    }

    // Running, waiting, failed and selected sessions in the tail stay visible in order.
    const run = session("s7", SessionStatus.RUNNING, { updated: -70 });
    const wait = session("s9", SessionStatus.WAITING_INPUT, { updated: -90 });
    ask(wait);
    const mixed = [...ten.slice(0, 6), run, ten[7], wait, ten[9]];
    const pinned = sliceSectionSessions(mixed, "project", SECTION_INITIAL_LIMIT, "s10");
    expect(ids(pinned.visible)).toEqual(["s1", "s2", "s3", "s4", "s5", "s7", "s9", "s10"]);
    expect(pinned.hiddenCount).toBe(2);

    // If pinning leaves at most 1 hidden idle row, all rows show.
    const sevenWithRunningTail = [...ten.slice(0, 6), run];
    expect(sliceSectionSessions(sevenWithRunningTail, "project")).toEqual({ visible: sevenWithRunningTail, hiddenCount: 0 });
  });

  test("a failed turn needs the owner until a new turn runs", () => {
    const s = session("f", SessionStatus.IDLE);
    progressOf(s).lastFailure = create(FailureSchema, { kind: FailureKind.AGENT_CRASHED, message: "hermeshd restarted during the turn" });
    expect(isFailed(s) && needsAttention(s)).toBe(true);
    expect(statusOf(s)).toBe("attention");

    for (const active of [SessionStatus.QUEUED, SessionStatus.PREPARING, SessionStatus.RUNNING]) {
      s.status = active;
      expect(isFailed(s)).toBe(false);
      expect(needsAttention(s)).toBe(false);
      expect(statusOf(s)).toBe("active");
    }
  });

  test("an interrupted turn did not fail", () => {
    const s = session("i", SessionStatus.IDLE);
    progressOf(s).lastFailure = create(FailureSchema, { kind: FailureKind.INTERRUPTED, message: "interrupted by owner" });
    expect(isFailed(s) || needsAttention(s)).toBe(false);
    expect(statusOf(s)).toBe("done");
  });

  test("search matches text and ranks what needs the owner", () => {
    const all = [
      session("a", SessionStatus.IDLE, { updated: -10, title: "修复登录页" }),
      session("b", SessionStatus.CRASHED, { updated: -500, title: "登录重试" }),
      session("c", SessionStatus.ARCHIVED, { title: "登录归档" }),
      session("d", SessionStatus.RUNNING, { title: "别的" }),
    ];
    const text = (s: Session) => s.title;
    expect(ids(searchSessions(all, "登录", sessionFilter(), text))).toEqual(["b", "a"]);
    expect(ids(searchSessions(all, " 登录 ", sessionFilter({ archived: true }), text))).toEqual(["b", "a", "c"]);
    expect(ids(searchSessions(all, "", sessionFilter(), text))).toEqual(["b", "d", "a"]);
  });

  test("a session's question and turn", () => {
    const s = session("s", SessionStatus.WAITING_INPUT);
    ask(s);
    progressOf(s).turnCount = 2;
    expect(pendingQuestion(s)?.questionId).toBe("q1");
    expect(currentTurn(s)).toBe(3);
    // A question left over from a session no longer waiting is not pending.
    s.status = SessionStatus.IDLE;
    expect(pendingQuestion(s)).toBeUndefined();
    expect(currentTurn(s)).toBe(2);
  });

  test("unset timestamps", () => {
    const s = session("s", SessionStatus.IDLE);
    s.createdAt = undefined;
    expect(dateOf(undefined)).toBeUndefined();
    expect(createdDate(s)).toEqual(updatedDate(s));
    s.updatedAt = undefined;
    expect(dayOf(s, now)).toBe("earlier");
  });
});

describe("today's stats", () => {
  test("counts today's work", () => {
    const a = session("a", SessionStatus.IDLE, { updated: -600, created: -3 * 3600, agent: "claude" });
    progressOf(a).diffStat = create(DiffStatSchema, { filesChanged: 3, insertions: 86, deletions: 12 });
    progressOf(a).usage = create(UsageSchema, { inputTokens: 400_000n, outputTokens: 18_000n, costUsd: 1.42 });
    const b = session("b", SessionStatus.CRASHED, { updated: -60, created: -1800, agent: "codex" });
    progressOf(b).usage = create(UsageSchema, { costUsd: 0.5 });
    const c = session("c", SessionStatus.RUNNING, { created: -120, agent: "claude" });
    const old = session("old", SessionStatus.IDLE, { updated: -3 * 86400, agent: "codex" });
    const t = todayStats([a, b, c, old], now);
    expect([t.sessions, t.done, t.failed]).toEqual([3, 1, 1]);
    expect([t.files, t.insertions, t.deletions]).toEqual([3, 86, 12]);
    expect(t.tokens).toBe(418_000n);
    expect(t.cost).toBeCloseTo(1.92, 3);
    expect(t.hours.length).toBe(9);
    expect(t.hours.at(-1)!.hour).toBe(15);
    expect(t.hours.find((h) => h.hour === 12)?.count).toBe(1);
    expect(t.hours.find((h) => h.hour === 15)?.count).toBe(2);
    expect(t.byAgent).toEqual([
      { agent: "claude", count: 2 },
      { agent: "codex", count: 1 },
    ]);
  });

  test("hours before midnight are yesterday's and count nothing", () => {
    const early = new Date(2026, 9, 19, 2, 10);
    const lastNight = session("n", SessionStatus.RUNNING);
    lastNight.createdAt = timestampFromDate(new Date(2026, 9, 18, 23, 30));
    const thisMorning = session("t", SessionStatus.RUNNING);
    thisMorning.createdAt = timestampFromDate(new Date(2026, 9, 19, 1, 30));
    const t = todayStats([lastNight, thisMorning], early);
    expect(t.hours.map((h) => h.hour)).toEqual([18, 19, 20, 21, 22, 23, 0, 1, 2]);
    expect(t.hours.map((h) => h.count)).toEqual([0, 0, 0, 0, 0, 0, 0, 1, 0]);
  });
});

describe("pushed and paged sessions", () => {
  test("a change replaces an earlier state, a new session joins, a stale one is dropped", () => {
    const run = session("a", SessionStatus.RUNNING, { updated: -60 });
    progressOf(run).lastEventSeq = 5n;
    const idle = session("a", SessionStatus.IDLE, { updated: -10 });
    progressOf(idle).lastEventSeq = 7n;
    // A diff stat computed after the turn: no new event, a later updated_at.
    const stat = session("a", SessionStatus.IDLE, { updated: -5 });
    progressOf(stat).lastEventSeq = 7n;
    progressOf(stat).diffStat = create(DiffStatSchema, { filesChanged: 2 });
    const other = session("b", SessionStatus.IDLE);

    const list = mergeSessions([run], [idle, other]);
    expect(ids(list)).toEqual(["a", "b"]);
    expect(list[0].status).toBe(SessionStatus.IDLE);
    expect(mergeSessions(list, [stat])[0].progress?.diffStat?.filesChanged).toBe(2);
    // A page listed before the change answers after the watch told of it.
    expect(mergeSessions(list, [run])[0].status).toBe(SessionStatus.IDLE);
    // A deleted session leaves the list.
    expect(ids(mergeSessions(list, [], ["a"]))).toEqual(["b"]);
  });

  test("relisting the first page goes on from the newer boundary", () => {
    const cursor = (seconds: number, id: string) =>
      create(SessionCursorSchema, { updatedAt: timestampFromDate(new Date(now.getTime() + seconds * 1000)), sessionId: id });
    const deep = cursor(-86400, "s1");
    const first = cursor(-60, "s9");
    expect(relistedCursor(deep, first)).toBe(first);
    expect(relistedCursor(first, deep)).toBe(first);
    expect(relistedCursor(undefined, first)).toBe(first);
    expect(relistedCursor(deep, undefined)).toBe(deep);
    expect(relistedCursor(undefined, undefined)).toBeUndefined();
    expect(relistedCursor(cursor(-60, "s1"), first)).toBe(first);
  });
});
