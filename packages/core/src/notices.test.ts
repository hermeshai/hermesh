import { clone, create } from "@bufbuild/protobuf";
import { PairingKind } from "@hermesh/proto/identity";
import { PairingDetailsSchema } from "@hermesh/proto/local";
import { DiffStatSchema, FailureKind, FailureSchema, SessionSchema, SessionStatus } from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import { filterNotices, noticeDiff, noticeSnapshot, noticeSnapshots, pairingNotice, type NoticeItem } from "./notices";
import { ask, at, progressOf, session } from "./testing";

const grant = () => "Grok Bot";

describe("session notices", () => {
  test("a new question is noticed once", () => {
    const s = session("s", SessionStatus.WAITING_INPUT, { title: "修复登录" });
    ask(s);
    const items = noticeDiff(new Map(), [s], grant);
    expect(items).toEqual([{ kind: "question", sessionId: "s", title: "Grok Bot 在等你回答", body: "修复登录：跳到哪里？" }]);
    expect(noticeDiff(new Map([["s", noticeSnapshot(s)]]), [s], grant)).toEqual([]);
  });

  test("a session created since the first look is new", () => {
    // Created a minute ago (testing.session), and running already.
    const s = session("s", SessionStatus.RUNNING, { title: "修复登录", updated: 0 });
    expect(noticeDiff(new Map(), [s], grant, at(-61))).toEqual([
      { kind: "started", sessionId: "s", title: "修复登录", body: "Grok Bot 发起 · app · Claude Code" },
    ]);
    // Seen before: no news again.
    expect(noticeDiff(noticeSnapshots([s]), [s], grant, at(-61))).toEqual([]);
    // Before the first look (since unset), or created before it: from
    // before, or a machine coming back.
    expect(noticeDiff(new Map(), [s], grant)).toEqual([]);
    expect(noticeDiff(new Map(), [s], grant, at(-59))).toEqual([]);
    // One that asks at once tells both.
    s.status = SessionStatus.WAITING_INPUT;
    ask(s);
    expect(noticeDiff(new Map(), [s], grant, at(-61)).map((i) => i.kind)).toEqual(["started", "question"]);
  });

  test("a failure and a finished turn", () => {
    const running = session("s", SessionStatus.RUNNING);
    const crashed = clone(SessionSchema, running);
    crashed.status = SessionStatus.CRASHED;
    progressOf(crashed).lastFailure = create(FailureSchema, { kind: FailureKind.CONTEXT_OVERFLOW });
    const f = noticeDiff(new Map([["s", noticeSnapshot(running)]]), [crashed], grant);
    expect(f.map((i) => i.kind)).toEqual(["failed"]);
    expect(f[0].title).toContain("上下文已满");
    // Already failed before: nothing new.
    expect(noticeDiff(new Map([["s", noticeSnapshot(crashed)]]), [crashed], grant)).toEqual([]);
    // Failures of sessions seen for the first time are not news.
    expect(noticeDiff(new Map(), [crashed], grant)).toEqual([]);

    const done = clone(SessionSchema, running);
    done.status = SessionStatus.IDLE;
    progressOf(done).turnCount = 1;
    progressOf(done).diffStat = create(DiffStatSchema, { filesChanged: 2, insertions: 5 });
    const d = noticeDiff(new Map([["s", noticeSnapshot(running)]]), [done], grant);
    expect(d).toEqual([{ kind: "turnDone", sessionId: "s", title: "「s」第 1 轮完成", body: "2 个文件 +5 −0" }]);
  });

  test("a crash without a failure, and a turn without changes", () => {
    const running = session("s", SessionStatus.RUNNING);
    const crashed = clone(SessionSchema, running);
    crashed.status = SessionStatus.CRASHED;
    const f = noticeDiff(noticeSnapshots([running]), [crashed], grant);
    expect(f).toEqual([
      { kind: "failed", sessionId: "s", title: "「s」出错了：失败", body: "Grok Bot · Claude Code · app。" },
    ]);

    const done = clone(SessionSchema, running);
    done.status = SessionStatus.IDLE;
    progressOf(done).turnCount = 1;
    expect(noticeDiff(noticeSnapshots([running]), [done], grant).map((i) => i.body)).toEqual(["没有改动文件"]);
  });

  test("a turn the owner interrupted is no news", () => {
    const running = session("s", SessionStatus.RUNNING);
    const stopped = clone(SessionSchema, running);
    stopped.status = SessionStatus.IDLE;
    progressOf(stopped).turnCount = 1;
    progressOf(stopped).lastFailure = create(FailureSchema, { kind: FailureKind.INTERRUPTED, message: "interrupted by owner" });
    expect(noticeDiff(noticeSnapshots([running]), [stopped], grant)).toEqual([]);
  });

  test("the owner's preferences", () => {
    const items: NoticeItem[] = (["started", "question", "failed", "turnDone"] as const).map((kind) => ({
      kind,
      sessionId: "s",
      title: "",
      body: "",
    }));
    const kinds = (list: NoticeItem[]) => list.map((i) => i.kind);
    // Finished turns are off until switched on.
    expect(kinds(filterNotices(items, {}))).toEqual(["started", "question", "failed"]);
    expect(kinds(filterNotices(items, { notifyStarted: false, notifyQuestion: false, notifyTurnDone: true }))).toEqual(["failed", "turnDone"]);
    expect(kinds(filterNotices(items, { notifyFailed: false }))).toEqual(["started", "question"]);
  });
});

describe("pairing notices", () => {
  test("each kind of request, with the code to check", () => {
    const p = (kind: PairingKind, verificationCode = "4F7-2K9") => create(PairingDetailsSchema, { kind, label: "gpu-box", code: "ABCD-EFGH", verificationCode });
    expect(pairingNotice(p(PairingKind.MACHINE))).toEqual({ title: "机器「gpu-box」想加入", body: "核对码 4F7-2K9。点这里核对并批准；不认识就拒绝。" });
    expect(pairingNotice(p(PairingKind.CLIENT)).title).toBe("「gpu-box」想连接");
    expect(pairingNotice(p(PairingKind.DEVICE)).title).toBe("「gpu-box」想成为你的可信设备");
    // Without a verification code, the pairing code is what both sides show.
    expect(pairingNotice(p(PairingKind.CLIENT, "")).body).toBe("核对码 ABCD-EFGH。点这里核对并批准；不认识就拒绝。");
  });
});
