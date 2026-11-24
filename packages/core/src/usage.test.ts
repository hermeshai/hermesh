import { create } from "@bufbuild/protobuf";
import { QueryWorkResponseSchema, WorkBucket, WorkDimension, type QueryWorkResponse } from "@hermesh/proto/local";
import { describe, expect, test } from "vitest";
import { noUsageFilter, usageChoices, usageMenusRequest, usagePage, usagePageRequest, usageRequest, usageSince, usageWeeksRequest, usageYear } from "./usage";

type Day = { date: string; agent: string; tokens: number; sessions: number };
const day = (date: string, agent: string, tokens: number, sessions = 1): Day => ({ date, agent, tokens, sessions });

describe("usage", () => {
  // Tuesday 20 October 2026.
  const today = new Date(2026, 9, 20, 15, 0);
  // QueryWork's answer, by day (from usageSince) and agent.
  const answer = (days: Day[]): QueryWorkResponse => {
    const [y, m, d] = usageSince(today).split("-").map(Number);
    const bucket = (date: string) => {
      const [a, b, c] = date.split("-").map(Number);
      return Math.round((new Date(a, b - 1, c).getTime() - new Date(y, m - 1, d).getTime()) / 86_400_000);
    };
    return create(QueryWorkResponseSchema, {
      turns: days.map((x) => ({ bucket: bucket(x.date), group: { agent: x.agent }, turns: x.sessions, inputTokens: BigInt(x.tokens) })),
      sessions: days.map((x) => ({ bucket: bucket(x.date), group: { agent: x.agent }, sessions: x.sessions })),
    });
  };
  const year = (days: Day[]) => usageYear(answer(days), today);

  test("a year of weeks ends in today's week and starts on a Sunday", () => {
    expect(usageSince(today)).toBe("2025-10-19");
    const y = usageYear(null, today);
    const q = usageRequest(today, 53, "Asia/Shanghai");
    expect(q).toMatchObject({ firstDay: "2025-10-19", days: 367, bucket: WorkBucket.DAY, groupBy: [WorkDimension.AGENT], timeZone: "Asia/Shanghai" });
    expect(y.cells.length).toBe(53 * 7);
    expect(y.cells[0]?.key).toBe("2025-10-19");
    // Today is the third cell of the last column; the rest of the week is not shown.
    expect(y.cells[52 * 7 + 2]?.key).toBe("2026-10-20");
    expect(y.cells.slice(52 * 7 + 3)).toEqual([null, null, null, null]);
    expect(y.total).toBe(0);
    expect(y.delta).toBeNull();
  });

  test("tokens add up by day and agent, with levels, streak and the change over 30 days", () => {
    const y = year(
      [
        day("2026-10-20", "claude", 400),
        day("2026-10-20", "codex", 100, 2),
        day("2026-10-19", "claude", 200),
        day("2026-10-18", "claude", 100),
        day("2026-10-10", "codex", 50),
        day("2026-09-10", "claude", 150),
      ],
    );
    expect(y.today).toBe(500);
    expect(y.total).toBe(1000);
    expect(y.active).toBe(5);
    expect(y.streak).toBe(3);
    expect(y.delta).toBe(467); // 850 in the last 30 days against 150 before
    expect(y.byAgent).toEqual([
      { agent: "claude", share: 0.85 },
      { agent: "codex", share: 0.15 },
    ]);
    const cell = y.cells.find((c) => c?.key === "2026-10-20");
    expect(cell?.sessions).toBe(3);
    expect(cell?.level).toBe(4);
    expect(y.cells.find((c) => c?.key === "2026-10-10")?.level).toBe(1);
    expect(y.cells.find((c) => c?.key === "2026-10-11")?.level).toBe(0);
  });

  test("a lone day is drawn darkest", () => {
    const y = year([day("2026-10-20", "gemini", 375)]);
    expect(y.cells.find((c) => c?.key === "2026-10-20")?.level).toBe(4);
  });

  test("before today's first turn the streak counts up to yesterday", () => {
    const y = year([day("2026-10-19", "claude", 10), day("2026-10-18", "claude", 10)]);
    expect(y.streak).toBe(2);
  });

  // The statistics' Usage view (docs/overview-usage.md §6).
  test("the page, the menus and the weeks are three questions of one range and filter", () => {
    const filter = { ...noUsageFilter, agent: "claude", machineId: "m1", projectId: "app" };
    expect(usagePageRequest("today", filter, today, "Asia/Shanghai")).toEqual({ timeZone: "Asia/Shanghai", firstDay: "2026-10-20", days: 1, bucket: WorkBucket.HOUR, filter, previous: true });
    expect(usagePageRequest("week", noUsageFilter, today, "UTC")).toMatchObject({ firstDay: "2026-10-14", days: 7, bucket: WorkBucket.DAY, previous: true });
    expect(usagePageRequest("month", noUsageFilter, today, "UTC")).toMatchObject({ firstDay: "2026-09-21", days: 30, bucket: WorkBucket.DAY });
    // The menus' choices are not filtered: each menu adds its lines up under the other filters.
    const menus = usageMenusRequest("week", today, "UTC");
    expect(menus).toMatchObject({ firstDay: "2026-10-14", days: 7, bucket: WorkBucket.NONE });
    expect(menus.groupBy).toEqual([WorkDimension.CLIENT, WorkDimension.AGENT, WorkDimension.MACHINE, WorkDimension.PROJECT]);
    expect(menus.filter).toBeUndefined();
    expect(usageWeeksRequest(filter, today, 53, "UTC")).toEqual({ timeZone: "UTC", firstDay: "2025-10-19", days: 367, bucket: WorkBucket.DAY, filter, turnsOnly: true });
  });

  test("the page adds the range up and sets it against the span before", () => {
    const res = create(QueryWorkResponseSchema, {
      sessions: [{ bucket: 0, sessions: 2 }, { bucket: 6, sessions: 3 }, { bucket: 6, sessions: 1 }],
      turns: [{ bucket: 0, inputTokens: 900n, outputTokens: 100n }, { bucket: 6, inputTokens: 4000n, outputTokens: 500n }],
      rangeSessions: [{ sessions: 4, done: 3 }, { sessions: 1, done: 0 }],
      rangeTurns: [{ turns: 9, failedTurns: 2, inputTokens: 4900n, outputTokens: 600n, busySeconds: 3700n }],
      previousSessions: [{ sessions: 4 }],
      previousTurns: [{ inputTokens: 10_000n, outputTokens: 1000n }],
    });
    const p = usagePage(res, "week", today);
    expect(p.buckets.length).toBe(7);
    expect(p.buckets[0]).toEqual({ sessions: 2, tokens: 1000 });
    expect(p.buckets[6]).toEqual({ sessions: 4, tokens: 4500 });
    expect(p.now).toBe(6);
    expect(p).toMatchObject({ sessions: 5, done: 3, failedTurns: 2, busySeconds: 3700, inputTokens: 4900, outputTokens: 600, sessionsDelta: 25, tokensDelta: -50 });
    // Today by hour: the hour now is in; nothing before the range, no change to say.
    const t = usagePage(create(QueryWorkResponseSchema, { sessions: [{ bucket: 15, sessions: 1 }], rangeSessions: [{ sessions: 1 }] }), "today", today);
    expect(t.buckets.length).toBe(24);
    expect(t.now).toBe(15);
    expect(t.buckets[15].sessions).toBe(1);
    expect(t.sessionsDelta).toBeNull();
    expect(t.tokensDelta).toBeNull();
    expect(usagePage(null, "month", today)).toMatchObject({ sessions: 0, now: 29 });
  });

  test("a menu's choices are what worked under the other filters, the busiest first; a project is its machine's own", () => {
    const g = (client: string, agent: string, machineId: string, projectId: string) => ({ client, agent, machineId, projectId });
    const res = create(QueryWorkResponseSchema, {
      rangeSessions: [
        { group: g("her", "claude", "mac", "app"), sessions: 3 },
        { group: g("bot", "codex", "mac", "app"), sessions: 1 },
        { group: g("her", "claude", "vps", "app"), sessions: 5 },
        { group: g("her", "codex", "vps", "blog"), sessions: 2 },
      ],
      rangeTurns: [
        { group: g("her", "claude", "mac", "app"), inputTokens: 300n },
        { group: g("bot", "codex", "mac", "app"), inputTokens: 100n },
        { group: g("her", "claude", "vps", "app"), inputTokens: 500n },
        { group: g("her", "codex", "vps", "blog"), inputTokens: 150n, outputTokens: 50n },
      ],
    });
    const all = usageChoices(res, noUsageFilter);
    expect(all.agents).toEqual([{ id: "claude", sessions: 8, tokens: 800 }, { id: "codex", sessions: 3, tokens: 300 }]);
    expect(all.clients).toEqual([{ id: "her", sessions: 10, tokens: 1000 }, { id: "bot", sessions: 1, tokens: 100 }]);
    expect(all.machines.map((m) => [m.id, m.sessions, m.tokens])).toEqual([["vps", 7, 700], ["mac", 4, 400]]);
    // Both machines have an "app": two projects, each under its machine.
    expect(all.machines[0].projects).toEqual([{ id: "app", sessions: 5, tokens: 500 }, { id: "blog", sessions: 2, tokens: 200 }]);
    expect(all.machines[1].projects).toEqual([{ id: "app", sessions: 4, tokens: 400 }]);
    // With mac's app chosen: the agents and senders that worked there; the places are not narrowed by themselves.
    const app = usageChoices(res, { ...noUsageFilter, machineId: "mac", projectId: "app" });
    expect(app.agents).toEqual([{ id: "claude", sessions: 3, tokens: 300 }, { id: "codex", sessions: 1, tokens: 100 }]);
    expect(app.clients.map((c) => c.id)).toEqual(["her", "bot"]);
    expect(app.machines.map((m) => m.id)).toEqual(["vps", "mac"]);
    // With codex chosen the places are narrowed to its; a choice that then has no work stays listed.
    const codex = usageChoices(res, { ...noUsageFilter, agent: "codex", client: "bot", machineId: "vps", projectId: "app" });
    expect(codex.machines).toEqual([{ id: "mac", sessions: 1, tokens: 100, projects: [{ id: "app", sessions: 1, tokens: 100 }] }, { id: "vps", sessions: 0, tokens: 0, projects: [{ id: "app", sessions: 0, tokens: 0 }] }]);
    expect(codex.agents).toEqual([{ id: "codex", sessions: 0, tokens: 0 }]);
    expect(usageChoices(null, noUsageFilter)).toEqual({ agents: [], machines: [], clients: [] });
  });
});
