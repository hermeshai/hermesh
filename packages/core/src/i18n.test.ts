import { clone, create } from "@bufbuild/protobuf";
import { DiffStatSchema, FailureKind, SessionSchema, SessionStatus, ToolCallSchema } from "@hermesh/proto/session";
import { afterAll, beforeAll, describe, expect, test } from "vitest";
import { i18n, pickLanguage } from "./i18n";
import { noticeDiff, noticeSnapshots } from "./notices";
import { stepSummary, turnTimeLine } from "./steps";
import { agoText, failureTitle, minutesLeft, shortTime } from "./text";
import { at, now, progressOf, session } from "./testing";

describe("English", () => {
  beforeAll(async () => {
    await i18n.changeLanguage("en");
  });
  afterAll(async () => {
    await i18n.changeLanguage("zh-CN");
  });

  test("failures", () => {
    expect(failureTitle(FailureKind.CONTEXT_OVERFLOW)).toBe("Context full");
    expect(failureTitle(FailureKind.UNSPECIFIED)).toBe("Failed");
  });

  test("times", () => {
    expect(agoText(at(-30), now)).toBe("just now");
    expect(agoText(at(-12 * 60), now)).toBe("12 min ago");
    expect(agoText(at(-3 * 3600), now)).toBe("3 hr ago");
    expect(agoText(at(-86400), now)).toBe("Yesterday 15:42");
    expect(agoText(at(-4 * 86400), now)).toBe("Oct 15");
    expect(shortTime(at(-3 * 86400), now)).toBe("Fri");
    expect(minutesLeft(at(21 * 60 + 5), now)).toBe("22 min left");
  });

  test("notices", () => {
    const running = session("s", SessionStatus.RUNNING, { title: "Fix sign-in" });
    const done = clone(SessionSchema, running);
    done.status = SessionStatus.IDLE;
    progressOf(done).turnCount = 1;
    progressOf(done).diffStat = create(DiffStatSchema, { filesChanged: 1, insertions: 5, deletions: 2 });
    expect(noticeDiff(noticeSnapshots([running]), [done], () => "Grok Bot")).toEqual([
      { kind: "turnDone", sessionId: "s", title: "“Fix sign-in” finished turn 1", body: "1 file +5 −2" },
    ]);
    const crashed = clone(SessionSchema, running);
    crashed.status = SessionStatus.CRASHED;
    expect(noticeDiff(noticeSnapshots([running]), [crashed], () => "Grok Bot")[0]).toMatchObject({
      title: "“Fix sign-in” failed: Failed",
      body: "Grok Bot · Claude Code · app.",
    });
    expect(noticeDiff(new Map(), [running], () => "Grok Bot", at(-600))).toEqual([
      { kind: "started", sessionId: "s", title: "Fix sign-in", body: "Grok Bot started it · app · Claude Code" },
    ]);
  });

  test("steps", () => {
    const call = (kind: string) => ({ call: create(ToolCallSchema, { kind }) });
    expect(stepSummary([call("read"), call("read"), call("shell")])).toBe("Read 2 files · Ran 1 command");
    expect(turnTimeLine(new Date(2026, 9, 19, 9, 5), undefined, now)).toBe("Today 09:05");
  });
});

test("the language for the system's preferred languages", () => {
  const cases: [string[], string][] = [
    [["zh-Hans-CN"], "zh-CN"],
    [["zh-CN"], "zh-CN"],
    [["zh"], "zh-CN"],
    [["zh-Hant-TW", "en"], "en"],
    [["zh-HK"], "en"],
    [["en-GB"], "en"],
    [["fr-FR", "zh-Hans"], "zh-CN"],
    [["fr"], "en"],
    [[], "en"],
  ];
  for (const [preferred, want] of cases) expect(pickLanguage(preferred), preferred.join(",")).toBe(want);
});
