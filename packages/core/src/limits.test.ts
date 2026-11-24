import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { MachineSchema } from "@hermesh/proto/local";
import { describe, expect, test } from "vitest";
import { agentLimits, lowLimits } from "./limits";

describe("limits", () => {
  const now = new Date(2026, 10, 24, 14, 20);
  const min = (n: number) => new Date(now.getTime() + n * 60_000);
  const ts = (d: Date) => timestampFromDate(d);
  const machine = (machineId: string, agents: { name: string; available?: boolean; said?: Date; plan?: string; windows?: [number, number, Date][] }[]) =>
    create(MachineSchema, {
      machineId,
      status: {
        agents: agents.map((a) => ({
          name: a.name,
          available: a.available ?? true,
          limits: a.said ? { plan: a.plan ?? "", observedAt: ts(a.said), windows: (a.windows ?? []).map(([minutes, usedPercent, resetsAt]) => ({ minutes, usedPercent, resetsAt: ts(resetsAt) })) } : undefined,
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
      { minutes: 300, left: 14, timeLeft: 47, resetsAt: min(140) },
      { minutes: 10080, left: 48, timeLeft: 50, resetsAt: min(5000) },
    ]);
    expect(cards[1]).toMatchObject({ plan: "pro", machineId: "mac", windows: [{ minutes: 10080, left: 39, timeLeft: 25 }] });
    // Available and never said: a card that says so; not available and never said: none.
    expect(cards[2]).toMatchObject({ agent: "gemini", machineId: "", observedAt: null, windows: [], stale: false });
    expect(lowLimits(cards)).toEqual([{ card: cards[0], window: cards[0].windows[0] }]);
  });

  test("a window that has reset since is not shown: what is left now is not known", () => {
    const cards = agentLimits([machine("mac", [{ name: "codex", said: min(-400), windows: [[300, 95, min(-100)], [10080, 70, min(3000)]] }, { name: "claude", said: min(-600), windows: [[300, 99, min(-300)]] }])], now);
    expect(cards.find((c) => c.agent === "codex")!.windows.map((w) => w.minutes)).toEqual([10080]);
    expect(cards.find((c) => c.agent === "claude")).toMatchObject({ windows: [], stale: true });
    expect(lowLimits(cards)).toEqual([]);
  });
});
