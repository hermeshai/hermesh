import { FailureKind, SessionStatus } from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import { agoText, clockText, elapsedSeconds, failureHeadline, listTime, minutesLeft, phaseText, shortTime, tokensText, toolSummary } from "./text";
import { at, now, progressOf, session } from "./testing";

describe("text", () => {
  test("times", () => {
    expect(shortTime(at(-3600), now)).toBe("14:42");
    expect(shortTime(at(-86400), now)).toBe("昨天");
    expect(shortTime(at(-3 * 86400), now)).toBe("周五");
    expect(shortTime(at(-20 * 86400), now)).toBe("9月29日");
    expect(agoText(at(-30), now)).toBe("刚刚");
    expect(agoText(at(-12 * 60), now)).toBe("12 分钟前");
    expect(agoText(at(-3 * 3600), now)).toBe("3 小时前");
    expect(agoText(at(-86400), now)).toBe("昨天 15:42");
    expect([clockText(252), clockText(3723), clockText(-5)]).toEqual(["4:12", "1:02:03", "0:00"]);
    // Counted in wall-clock seconds: two clocks started at different milliseconds change on the same boundary.
    const t0 = (ms: number) => new Date(Date.UTC(2026, 9, 26, 8, 0, 0) + ms);
    expect([elapsedSeconds(t0(900), t0(1_999)), elapsedSeconds(t0(100), t0(1_999))]).toEqual([1, 1]);
    expect([elapsedSeconds(t0(900), t0(2_000)), elapsedSeconds(t0(100), t0(2_000))]).toEqual([2, 2]);
    expect([tokensText(412_000), tokensText(1_800_000), tokensText(12)]).toEqual(["412K", "1.8M", "12"]);
    expect(minutesLeft(at(21 * 60 + 5), now)).toBe("还剩 22 分钟");
  });

  test("a failure is told of the coding agent, every kind in its own words", () => {
    expect(failureHeadline(FailureKind.START_FAILED, "Codex")).toBe("Codex 没能启动");
    expect(failureHeadline(FailureKind.AUTH, "Claude Code")).toBe("Claude Code 没有登录");
    expect(failureHeadline(FailureKind.UNSPECIFIED, "Codex")).toBe("Codex 出错了");
    const kinds = Object.values(FailureKind).filter((k): k is FailureKind => typeof k === "number" && k !== FailureKind.UNSPECIFIED && k !== FailureKind.INTERRUPTED && k !== FailureKind.UNKNOWN);
    expect(kinds.filter((k) => failureHeadline(k, "Codex") === "Codex 出错了")).toEqual([]);
  });

  test("the session list's time is relative today and short after", () => {
    expect(listTime(at(-30), now)).toBe("刚刚");
    expect(listTime(at(-12 * 60), now)).toBe("12 分钟前");
    expect(listTime(at(-3 * 3600), now)).toBe("3 小时前");
    expect(listTime(at(-86400), now)).toBe("昨天");
    expect(listTime(at(-3 * 86400), now)).toBe("周五");
    expect(listTime(at(-20 * 86400), now)).toBe("9月29日");
  });

  test("a morning hour has two digits", () => {
    expect(shortTime(new Date(2026, 9, 19, 9, 5), now)).toBe("09:05");
  });

  test("tokens round as printf does", () => {
    expect([tokensText(1_250_000n), tokensText(1_750_000n), tokensText(1_050_000n), tokensText(999_999)]).toEqual([
      "1.2M",
      "1.8M",
      "1.1M",
      "999K",
    ]);
  });

  test("phase", () => {
    const s = session("s", SessionStatus.RUNNING);
    expect(phaseText(s)).toBe("思考中");
    progressOf(s).currentTool = "Edit: /tmp/wt/s/internal/session/store_test.go";
    progressOf(s).phase = "editing";
    expect(phaseText(s)).toBe("编辑 internal/session/store_test.go");
    progressOf(s).currentTool = "Bash: go test ./...";
    progressOf(s).phase = "running_shell";
    expect(phaseText(s)).toBe("执行 go test ./...");
    expect(phaseText(session("q", SessionStatus.QUEUED))).toBe("排队中");
  });

  test("a tool with nothing after its name", () => {
    const s = session("s", SessionStatus.RUNNING);
    progressOf(s).currentTool = "Bash:";
    progressOf(s).phase = "running_shell";
    expect(phaseText(s)).toBe("执行 Bash");
    progressOf(s).currentTool = "Read: a:b";
    progressOf(s).phase = "";
    expect(phaseText(s)).toBe("Read a:b");
  });

  test("a command run through a shell", () => {
    expect(toolSummary("/bin/zsh -lc 'go test ./...'", "s")).toBe("go test ./...");
    expect(toolSummary("bash -c 'ls /tmp/wt/s/docs'", "s")).toBe("ls docs");
  });
});
