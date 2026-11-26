import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { MachineSchema } from "@hermesh/proto/local";
import { describe, expect, test } from "vitest";
import { agentLimits, hasUnseenLowLimits, lowLimits, markLowLimitsSeen } from "./limits";

describe("limits", () => {
  const now = new Date(2026, 10, 24, 14, 20);
  const min = (n: number) => new Date(now.getTime() + n * 60_000);
  const ts = (d: Date) => timestampFromDate(d);
  const machine = (machineId: string, agents: { name: string; available?: boolean; said?: Date; plan?: string; windows?: [number, number, Date, string?][] }[]) =>
    create(MachineSchema, {
      machineId,
      status: {
        agents: agents.map((a) => ({
          name: a.name,
          available: a.available ?? true,
          limits: a.said ? { plan: a.plan ?? "", observedAt: ts(a.said), windows: (a.windows ?? []).map(([minutes, usedPercent, resetsAt, group]) => ({ minutes, usedPercent, resetsAt: ts(resetsAt), group })) } : undefined,
        })),
      },
    });

  test("one card an agent, from the machine that said it last; the least left first; what is left of each window's time", () => {
    const cards = agentLimits(
      [
        machine("mac", [
          { name: "claude", said: min(-120), windows: [[10080, 10, min(5000)], [300, 30, min(100)]] },
          { name: "codex", said: min(-3), plan: "pro", windows: [[10080, 61, min(2520)]] },
          { name: "gemini" },
        ]),
        machine("mini", [{ name: "claude", said: min(-2), windows: [[300, 86, min(140)], [10080, 52, min(5000)]] }, { name: "kimi", available: false }]),
      ],
      now,
    );
    expect(cards.map((c) => c.agent)).toEqual(["claude", "codex", "gemini"]);
    expect(cards[0]).toMatchObject({ machineId: "mini", observedAt: min(-2), stale: false });
    expect(cards[0].windows).toEqual([
      { minutes: 300, left: 14, timeLeft: 47, resetsAt: min(140), group: "" },
      { minutes: 10080, left: 48, timeLeft: 50, resetsAt: min(5000), group: "" },
    ]);
    expect(cards[1]).toMatchObject({ plan: "pro", machineId: "mac", windows: [{ minutes: 10080, left: 39, timeLeft: 25 }] });
    // Available and never said: a card that says so; not available and never said: none.
    expect(cards[2]).toMatchObject({ agent: "gemini", machineId: "", observedAt: null, windows: [], stale: false });
    expect(lowLimits(cards)).toEqual([{ card: cards[0], window: cards[0].windows[0] }]);
  });

  test("a window not started (no reset time, or agy's 1970) is shown whole, its time too; one that has reset is not", () => {
    const m = machine("mac", [{ name: "antigravity", said: min(-1), windows: [[10080, 0, new Date(0), "Claude and GPT models"], [300, 0, new Date(0), "Claude and GPT models"], [300, 3, min(-10), "Gemini Models"]] }]);
    m.status!.agents[0].limits!.windows.push({ ...m.status!.agents[0].limits!.windows[0], resetsAt: undefined, minutes: 60, group: "Other" });
    const card = agentLimits([m], now)[0];
    expect(card.windows).toEqual([
      { minutes: 300, left: 100, timeLeft: 100, resetsAt: null, group: "Claude and GPT models" },
      { minutes: 10080, left: 100, timeLeft: 100, resetsAt: null, group: "Claude and GPT models" },
      { minutes: 60, left: 100, timeLeft: 100, resetsAt: null, group: "Other" },
    ]);
    expect(card.stale).toBe(false);
    expect(lowLimits([card])).toEqual([]);
  });

  test("a window that has reset since is not shown: what is left now is not known", () => {
    const cards = agentLimits([machine("mac", [{ name: "codex", said: min(-400), windows: [[300, 95, min(-100)], [10080, 70, min(3000)]] }, { name: "claude", said: min(-600), windows: [[300, 99, min(-300)]] }])], now);
    expect(cards.find((c) => c.agent === "codex")!.windows.map((w) => w.minutes)).toEqual([10080]);
    expect(cards.find((c) => c.agent === "claude")).toMatchObject({ windows: [], stale: true });
    expect(lowLimits(cards)).toEqual([]);
  });

  test("an agent that counts groups of models apart: its groups in the order it gave them, the shortest first in each; the least left of all needs the owner", () => {
    const cards = agentLimits(
      [
        machine("mac", [
          {
            name: "antigravity",
            said: min(-1),
            windows: [
              [300, 3, min(120), "Gemini Models"],
              [10080, 17, min(9000), "Gemini Models"],
              [10080, 88, min(1080), "Claude and GPT models"],
              [300, 0, min(300), "Claude and GPT models"],
            ],
          },
        ]),
      ],
      now,
    );
    expect(cards[0].windows.map((w) => [w.group, w.minutes, w.left])).toEqual([
      ["Gemini Models", 300, 97],
      ["Gemini Models", 10080, 83],
      ["Claude and GPT models", 300, 100],
      ["Claude and GPT models", 10080, 12],
    ]);
    expect(lowLimits(cards)).toEqual([{ card: cards[0], window: cards[0].windows[3] }]);
  });

  test("a low window stays unseen until marked seen, stays quiet until it resets, and returns for a new low window or cycle", () => {
    const initial = agentLimits(
      [machine("mac", [{ name: "claude", said: min(-2), windows: [[300, 86, min(140)], [10080, 82, min(5000)]] }, { name: "codex", said: min(-2), windows: [[300, 30, min(120)]] }])],
      now,
    );
    expect(hasUnseenLowLimits(initial, {}, now)).toBe(true);
    const seen = markLowLimitsSeen(initial, {}, now);
    expect(hasUnseenLowLimits(initial, seen, now)).toBe(false);

    // Same window drops further and resetsAt drifts slightly: still quiet.
    const lower = agentLimits(
      [machine("mac", [{ name: "claude", said: min(5), windows: [[300, 93, min(142)], [10080, 85, min(5000)]] }, { name: "codex", said: min(-2), windows: [[300, 30, min(120)]] }])],
      min(5),
    );
    expect(hasUnseenLowLimits(lower, seen, min(5))).toBe(false);

    // A different agent drops low: dot comes back.
    const another = agentLimits(
      [machine("mac", [{ name: "claude", said: min(5), windows: [[300, 93, min(142)], [10080, 85, min(5000)]] }, { name: "codex", said: min(6), windows: [[300, 88, min(120)]] }])],
      min(6),
    );
    expect(hasUnseenLowLimits(another, seen, min(6))).toBe(true);

    // After the 5-hour window resets (past +140 min), weekly is still suppressed until +5000 min.
    const afterFiveReset = agentLimits(
      [machine("mac", [{ name: "claude", said: min(150), windows: [[300, 20, min(450)], [10080, 85, min(5000)]] }])],
      min(150),
    );
    expect(hasUnseenLowLimits(afterFiveReset, seen, min(150))).toBe(false);

    // Next 5-hour cycle drops low again: dot comes back, and marking prunes expired entries.
    const nextCycleLow = agentLimits(
      [machine("mac", [{ name: "claude", said: min(200), windows: [[300, 90, min(450)], [10080, 85, min(5000)]] }])],
      min(200),
    );
    expect(hasUnseenLowLimits(nextCycleLow, seen, min(200))).toBe(true);
    const seenAgain = markLowLimitsSeen(nextCycleLow, seen, min(200));
    expect(hasUnseenLowLimits(nextCycleLow, seenAgain, min(200))).toBe(false);
    expect(seenAgain["claude//300"]).toBe(min(450).getTime());
  });
});
