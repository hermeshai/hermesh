import { create } from "@bufbuild/protobuf";
import { QueryWorkResponseSchema, WorkBucket, WorkDimension, type QueryWorkResponse } from "@hermesh/proto/local";
import { describe, expect, test } from "vitest";
import { usageRequest, usageSince, usageYear } from "./usage";

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
});
