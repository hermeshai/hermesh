import assert from "node:assert/strict";
import test from "node:test";
import type { BotSummary } from "@hermesh/her-protocol";
import { badgeCount, botAlerts, sessionAlerts, targetOf, type AlertSession } from "./alerts.ts";

const bot = (over: Partial<BotSummary> = {}): BotSummary => ({ id: "b", name: "小研", avatar: { kind: "mono", hue: "slate" }, created_at: "2026-11-04T00:00:00Z", ...over });
const said = (text: string, ts = "2026-11-04T01:00:00.000Z", from: "bot" | "user" = "bot") => ({ from, text, ts });
const session = (state: AlertSession["state"], archived = false): AlertSession => ({ session_id: "s", state, archived, cwd: "/work/her", title: "修登录", no_project: false });
const kinds = (c: { alerts: { kind: string }[] }) => c.alerts.map((n) => n.kind);

test("a new message from the bot is a alert with its first plain line", () => {
  const c = botAlerts(bot(), bot({ last: said("## 标题\n正文"), unread: 1 }));
  assert.deepEqual(kinds(c), ["bot_message"]);
  assert.equal(c.alerts[0]!.group, "bot:b");
  assert.equal((c.alerts[0] as { text: string }).text, "标题");
});

test("the owner's own message is no alert", () => {
  assert.deepEqual(kinds(botAlerts(bot(), bot({ last: said("你好", undefined, "user") }))), []);
});

test("a row changing for another reason repeats nothing", () => {
  const before = bot({ last: said("一"), unread: 1 });
  assert.deepEqual(kinds(botAlerts(before, { ...before, typing: true })), []);
  assert.deepEqual(kinds(botAlerts(before, { ...before, name: "新名字" })), []);
});

test("a message arriving with a read still counts: the unread count falls yet the message is new", () => {
  const before = bot({ last: said("一", "2026-11-04T01:00:00.000Z"), unread: 3 });
  const c = botAlerts(before, bot({ last: said("二", "2026-11-04T01:00:01.000Z"), unread: 1 }));
  assert.deepEqual(kinds(c), ["bot_message"]);
});

test("two messages in the same instant are both news", () => {
  const before = bot({ last: said("一"), unread: 1 });
  assert.deepEqual(kinds(botAlerts(before, bot({ last: said("二"), unread: 2 }))), ["bot_message"]);
});

test("messages of the same second and length stay separate notifications", () => {
  const one = botAlerts(bot(), bot({ last: said("春天的诗") })).alerts[0]!;
  const two = botAlerts(bot({ last: said("春天的诗") }), bot({ last: said("夏天的诗") })).alerts[0]!;
  assert.notEqual(one.id, two.id);
});

test("a message that is only marks still says something", () => {
  const c = botAlerts(bot(), bot({ last: said("**"), unread: 1 }));
  assert.equal((c.alerts[0] as { text: string }).text, "**");
});

test("an approval is a alert, gone when answered, replaced by the next", () => {
  const asked = botAlerts(bot(), bot({ waiting: "运行 ls" }));
  assert.deepEqual(kinds(asked), ["bot_approval"]);
  assert.equal(asked.alerts[0]!.id, "bot:b:approval");
  const answered = botAlerts(bot({ waiting: "运行 ls" }), bot());
  assert.deepEqual(answered.clear, ["bot:b:approval"]);
  assert.deepEqual(kinds(answered), []);
  const next = botAlerts(bot({ waiting: "运行 ls" }), bot({ waiting: "写文件" }));
  assert.deepEqual(kinds(next), ["bot_approval"]);
  assert.deepEqual(next.clear, ["bot:b:approval"]);
});

test("a failure out of work is a alert; an old one found on loading is not", () => {
  assert.deepEqual(kinds(botAlerts(bot({ typing: true }), bot({ failed: true }))), ["bot_failed"]);
  assert.deepEqual(kinds(botAlerts(bot(), bot({ failed: true }))), []);
  assert.deepEqual(kinds(botAlerts(bot({ failed: true }), bot({ failed: true }))), []);
});

test("a failure stays until the bot works again: unloading is not an answer to it", () => {
  assert.deepEqual(botAlerts(bot({ failed: true }), bot()).clear, []);
  assert.deepEqual(botAlerts(bot({ failed: true }), bot({ typing: true })).clear, ["bot:b:failed"]);
});

test("reading clears what the bot has showing", () => {
  assert.deepEqual(botAlerts(bot({ unread: 2 }), bot()).clearGroups, ["bot:b"]);
  assert.deepEqual(botAlerts(bot(), bot()).clearGroups, []);
});

test("a muted bot alerts about nothing, but what it had showing still goes", () => {
  const c = botAlerts(bot({ unread: 1, waiting: "运行 ls", muted: true }), bot({ muted: true, last: said("一"), failed: false }));
  assert.deepEqual(kinds(c), []);
  assert.deepEqual(c.clearGroups, ["bot:b"]);
  assert.deepEqual(c.clear, ["bot:b:approval"]);
  assert.deepEqual(kinds(botAlerts(bot({ typing: true, muted: true }), bot({ failed: true, muted: true }))), []);
});

test("a session: waiting, failing and ending are alerts", () => {
  assert.deepEqual(kinds(sessionAlerts(session("running"), session("awaiting_approval"))), ["session_approval"]);
  assert.deepEqual(kinds(sessionAlerts(session("running"), session("failed"))), ["session_failed"]);
  assert.deepEqual(kinds(sessionAlerts(session("awaiting_approval"), session("failed"))), ["session_failed"]);
  assert.deepEqual(kinds(sessionAlerts(session("running"), session("idle"))), ["session_ended"]);
  assert.equal((sessionAlerts(session("running"), session("idle")).alerts[0] as { title: string }).title, "修登录");
});

test("a session first seen waiting is news; first seen otherwise is not", () => {
  assert.deepEqual(kinds(sessionAlerts(undefined, session("awaiting_approval"))), ["session_approval"]);
  assert.deepEqual(kinds(sessionAlerts(undefined, session("idle"))), []);
  assert.deepEqual(kinds(sessionAlerts(undefined, session("failed"))), []);
});

test("an approval refused with the task stopped is not called an ending", () => {
  const c = sessionAlerts(session("awaiting_approval"), session("idle"));
  assert.deepEqual(kinds(c), []);
  assert.deepEqual(c.clear, ["session:s:approval"]);
});

test("a session working again takes down what it said about the last task", () => {
  assert.deepEqual(sessionAlerts(session("idle"), session("running")).clear, ["session:s:ended"]);
  assert.deepEqual(sessionAlerts(session("awaiting_approval"), session("running")).clear, ["session:s:approval", "session:s:ended"]);
});

test("an archived session alerts about nothing and the same state repeats nothing", () => {
  assert.deepEqual(kinds(sessionAlerts(session("running", true), session("idle", true))), []);
  assert.deepEqual(kinds(sessionAlerts(session("awaiting_approval"), session("awaiting_approval"))), []);
});

test("the icon's number leaves muted bots and archived sessions out", () => {
  const bots = [bot({ unread: 2 }), bot({ id: "m", unread: 5, muted: true }), bot({ id: "z" })];
  const sessions = [session("awaiting_approval"), session("awaiting_approval", true), session("running")];
  assert.equal(badgeCount(bots, sessions), 3);
  assert.equal(badgeCount([], []), 0);
});

test("a group opens its bot or session; anything else opens nothing", () => {
  assert.deepEqual(targetOf("bot:a:b"), { kind: "bot", id: "a:b" });
  assert.deepEqual(targetOf("session:x"), { kind: "session", id: "x" });
  assert.equal(targetOf("test"), null);
  assert.equal(targetOf("bot:"), null);
});

test("files alone say what they are, and a second one in the same second is still news", () => {
  const files = { from: "bot" as const, text: "", file: "报告.pdf", ts: "2026-11-04T10:00:00Z" };
  const first = botAlerts(bot({}), bot({ last: files, unread: 1 })).alerts[0];
  assert.ok(first?.kind === "bot_message");
  assert.equal(first.text, "");
  assert.equal(first.file, "报告.pdf");
  const images = botAlerts(bot({ last: files }), bot({ last: { ...files, file: undefined, images: 2 }, unread: 2 })).alerts[0];
  assert.ok(images?.kind === "bot_message");
  assert.equal(images.images, 2);
  assert.notEqual(images.id, first.id);
});
