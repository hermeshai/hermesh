import assert from "node:assert/strict";
import { test } from "node:test";
import type { Event, Op, SessionSnapshot, SessionSummary } from "@hermesh/her-protocol";
import { RpcClient } from "../rpc.ts";
import { applyEvent, fromSnapshot, statusOf, type SessionView } from "../session.ts";
import { conversation } from "../steps.ts";
import { mockTransport } from "./index.ts";
import { MockServer } from "./server.ts";

function client(server = new MockServer({ speed: 200 })) {
  return new RpcClient(mockTransport(server));
}

/** The parts of a state a client shows (ids of new objects aside). */
function shown(v: SessionView) {
  return { status: statusOf(v), steps: conversation(v).map((s) => ({ ...s, ts: undefined })), usage: v.usage, info: v.info, lastSeq: v.lastSeq };
}

test("the demo lists sessions in every state, newest first", async () => {
  const rpc = client(new MockServer({ still: true }));
  await rpc.request("initialize", { protocol_version: 1 });
  const { sessions } = await rpc.request("sessions/subscribe", {});
  const states = new Set(sessions.map((s: SessionSummary) => s.state));
  for (const st of ["idle", "running", "failed"] as const) assert.ok(states.has(st), `no ${st} session`);
  assert.ok(sessions.some((s) => s.archived));
  const times = sessions.map((s) => Date.parse(s.updated_at));
  assert.deepEqual(times, [...times].sort((a, b) => b - a));
});

test("a snapshot taken at any point, with the events after it, rebuilds what a client that saw every event has", async () => {
  const server = new MockServer({ speed: 400 });
  const watcher = client(server);
  // Full auto: the demo's reply asks before its edit in the other modes.
  const { session_id, snapshot } = await watcher.request("session/new", { cwd: "/tmp/demo", permission_mode: "bypass-permissions" });
  let full = fromSnapshot(snapshot);
  const done = new Promise<void>((resolve) => {
    watcher.on("session/event", ({ event }) => {
      full = applyEvent(full, event as Event);
      if (event.type === "task_completed") resolve();
    });
  });
  await watcher.request("session/op", { session_id, op: { type: "prompt", input: [{ type: "text", text: "看看这个项目" }] } });
  // Others attach at different moments, each from its own snapshot and then its own events.
  const late: { view?: SessionView }[] = [];
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => setTimeout(r, 15));
    const other = client(server);
    const l: { view?: SessionView } = {};
    other.on("session/event", ({ event }) => void (l.view &&= applyEvent(l.view, event as Event)));
    const { snapshot: s } = await other.request("session/attach", { session_id });
    l.view = fromSnapshot(s as SessionSnapshot);
    late.push(l);
  }
  await done;
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(late.length, 6);
  for (const l of late) assert.deepEqual(shown(l.view!), shown(full));
  assert.equal(statusOf(full), "idle");
});

test("an approval waits, and the task goes on once approved", async () => {
  const server = new MockServer({ speed: 300 });
  const rpc = client(server);
  const { sessions } = await rpc.request("sessions/subscribe", {});
  const asking = sessions.find((s) => s.title === "清理会话的临时输出")!;
  const { snapshot } = await rpc.request("session/attach", { session_id: asking.session_id });
  let v = fromSnapshot(snapshot);
  const ended = new Promise<void>((resolve) =>
    rpc.on("session/event", ({ event }) => {
      v = applyEvent(v, event as Event);
      if (event.type === "approval_requested") void rpc.request("session/op", { session_id: asking.session_id, op: { type: "approval", call_id: event.call_id, decision: { type: "approve" } } });
      if (event.type === "task_completed") resolve();
    }),
  );
  await ended;
  const tools = conversation(v).flatMap((s) => (s.kind === "work" ? s.items : [])).filter((i) => i.kind === "tool");
  assert.ok(tools.every((t) => t.kind === "tool" && t.tool.state === "done"));
  assert.equal(statusOf(v), "idle");
});

test("an interrupt ends the task, keeping what was written", async () => {
  const server = new MockServer({ speed: 50 });
  const rpc = client(server);
  const { session_id, snapshot } = await rpc.request("session/new", { cwd: "/tmp/demo" });
  let v = fromSnapshot(snapshot);
  rpc.on("session/event", ({ event }) => void (v = applyEvent(v, event as Event)));
  await rpc.request("session/op", { session_id, op: { type: "prompt", input: [{ type: "text", text: "hi" }] } });
  await new Promise((r) => setTimeout(r, 40));
  await rpc.request("session/op", { session_id, op: { type: "interrupt" } });
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(statusOf(v), "idle");
  assert.equal(v.entries.at(-1)?.type, "task_end");
  const end = v.entries.at(-1);
  assert.ok(end?.type === "task_end" && end.outcome === "interrupted");
});

test("input sent while the task runs waits in the queue and joins under its id: steering at the next step, a follow-up when the task would end", async () => {
  const server = new MockServer({ speed: 50 });
  const rpc = client(server);
  const { session_id, snapshot } = await rpc.request("session/new", { cwd: "/tmp/demo", permission_mode: "bypass-permissions" });
  let v = fromSnapshot(snapshot);
  const queues: string[][] = [];
  const idle = new Promise<void>((resolve) =>
    rpc.on("session/event", ({ event }) => {
      v = applyEvent(v, event as Event);
      if (event.type === "queue_changed") queues.push(event.queued.map((q) => `${q.mode}:${q.id}`));
      if (event.type === "idle") resolve();
    }),
  );
  const say = (text: string, mode?: "steer" | "follow_up") => rpc.request("session/op", { session_id, op: { type: "prompt", input: [{ type: "text", text }], mode } });
  await say("hi");
  await say("later", "follow_up");
  await say("now", "steer");
  await idle;
  const users = v.entries.flatMap((e) => (e.type === "message" && e.message.role === "user" ? [{ id: e.message_id, text: e.message.content.map((c) => (c.type === "text" ? c.text : "")).join("") }] : []));
  assert.deepEqual(users.map((u) => u.text), ["hi", "now", "later"]);
  // Announced with the ids they joined under, steering first; the queue ends empty.
  const [later, now] = [users[2].id, users[1].id];
  assert.ok(queues.some((q) => q.join() === `steer:${now},follow_up:${later}`), JSON.stringify(queues));
  assert.deepEqual(queues.at(-1), []);
  assert.equal(v.queued.length, 0);
  // One task: the follow-up went in before it ended.
  assert.equal(v.entries.filter((e) => e.type === "task_end").length, 1);
});

test("a stop hands what waits back, steering first, and a withdrawn message never joins", async () => {
  const server = new MockServer({ speed: 50 });
  const rpc = client(server);
  const { session_id, snapshot } = await rpc.request("session/new", { cwd: "/tmp/demo" });
  let v = fromSnapshot(snapshot);
  let asked!: () => void;
  const asking = new Promise<void>((resolve) => (asked = resolve));
  rpc.on("session/event", ({ event }) => {
    v = applyEvent(v, event as Event);
    if (event.type === "approval_requested") asked();
  });
  const op = (o: Op) => rpc.request("session/op", { session_id, op: o });
  await op({ type: "prompt", input: [{ type: "text", text: "hi" }] });
  await asking;
  for (const [text, mode] of [["after", "follow_up"], ["oops", "steer"], ["next", "steer"]] as const) await op({ type: "prompt", input: [{ type: "text", text }], mode });
  await op({ type: "withdraw", id: v.queued.find((q) => q.message.content[0].type === "text" && q.message.content[0].text === "oops")!.id });
  await new Promise((r) => setTimeout(r, 10));
  assert.ok(v.withdrawn);
  await op({ type: "interrupt" });
  await new Promise((r) => setTimeout(r, 20));
  assert.deepEqual(v.returned?.map((m) => (m.content[0].type === "text" ? m.content[0].text : "")), ["next", "after"]);
  assert.equal(v.queued.length, 0);
});

test("a resend stops the task, hands back what waits and goes back; a later attach sees the same", async () => {
  const server = new MockServer({ speed: 50 });
  const rpc = client(server);
  const { session_id, snapshot } = await rpc.request("session/new", { cwd: "/tmp/demo" });
  let v = fromSnapshot(snapshot);
  let asked!: () => void;
  const asking = new Promise<void>((resolve) => (asked = resolve));
  const kinds: string[] = [];
  rpc.on("session/event", ({ event }) => {
    v = applyEvent(v, event as Event);
    kinds.push(event.type);
    if (event.type === "approval_requested") asked();
  });
  const op = (o: Op) => rpc.request("session/op", { session_id, op: o });
  await op({ type: "prompt", input: [{ type: "text", text: "hi" }] });
  await asking;
  await op({ type: "prompt", input: [{ type: "text", text: "after" }], mode: "follow_up" });
  const first = v.entries.find((e) => e.type === "message" && e.message.role === "user");
  assert.ok(first?.type === "message");
  // A message that is not there: refused, nothing changes.
  await op({ type: "resend", message_id: "nothing-like-it", input: [{ type: "text", text: "no" }] });
  await new Promise((r) => setTimeout(r, 5));
  assert.ok(v.refused);
  kinds.length = 0;
  await op({ type: "resend", message_id: first.message_id, input: [{ type: "text", text: "说点别的" }] });
  assert.deepEqual(kinds.slice(0, 5), ["task_interrupted", "queue_changed", "session_rewound", "task_started", "message_completed"]);
  assert.deepEqual(v.returned?.map((m) => (m.content[0].type === "text" ? m.content[0].text : "")), ["after"]);
  assert.deepEqual(v.rewound?.gone, [first.message_id]);
  const said = v.entries.find((e) => e.type === "message" && e.message.role === "user");
  assert.ok(said?.type === "message" && said.message_id !== first.message_id);
  assert.deepEqual(said.message.content, [{ type: "text", text: "说点别的" }]);
  const attached = await rpc.request("session/attach", { session_id });
  const ids = (items: { type: string; message_id?: string }[]) => items.filter((e) => e.type === "message").map((e) => e.message_id);
  assert.deepEqual(ids(attached.snapshot.transcript.items), ids(v.entries));
});

test("bots: the demo's, a new one, and a conversation in messages that stays out of session lists", async () => {
  const server = new MockServer({ speed: 400 });
  const rpc = client(server);
  await rpc.request("initialize", { protocol_version: 2 });
  const { bots } = await rpc.request("bots/subscribe", {});
  assert.deepEqual(bots.map((b) => b.name), ["Her", "研究员", "运维", "写作助手", "分析师", "旅行规划"]);
  assert.equal(bots.find((b) => b.default)?.avatar.kind, "mark");
  assert.equal(bots.find((b) => b.name === "写作助手")?.unread, 2);
  const changed: string[] = [];
  rpc.on("bots/changed", ({ bots: rows }) => rows.forEach((b) => b.last && changed.push(`${b.last.from}:${b.last.text}`)));
  const { bot } = await rpc.request("bots/create", { name: " 诗人 ", avatar: { kind: "char", shape: "wink", hue: "teal" } });
  assert.equal(bot.name, "诗人");
  const { session_id, snapshot } = await rpc.request("bots/open", { bot_id: bot.id });
  let view = fromSnapshot(snapshot);
  const done = new Promise<void>((resolve) => {
    rpc.on("session/event", ({ event }) => {
      view = applyEvent(view, event as Event);
      if (event.type === "task_completed") resolve();
    });
  });
  await rpc.request("session/op", { session_id, op: { type: "prompt", input: [{ type: "text", text: "写一首冬天的诗" }] } });
  await done;
  const sent = view.entries.flatMap((e) => (e.type === "message" && e.message.role === "assistant" ? e.message.content : [])).filter((c) => c.type === "tool_call" && c.name === "send_to_user");
  assert.equal(sent.length, 1);
  assert.ok(changed.some((c) => c.startsWith("bot:**冬**")), JSON.stringify(changed));
  const { sessions } = await rpc.request("session/list", {});
  assert.ok(sessions.every((s) => s.session_id !== session_id));
  await assert.rejects(rpc.request("bots/delete", { bot_id: bots[0].id }), /default bot/);
  await rpc.request("bots/delete", { bot_id: bot.id });
  await assert.rejects(rpc.request("bots/open", { bot_id: bot.id }), /not found/);
  const made = await rpc.request("bots/create", { name: "选过", avatar: { kind: "mono", hue: "teal" }, model: "deepseek/deepseek-flash", thinking: "low" });
  const opened = await rpc.request("bots/open", { bot_id: made.bot.id });
  assert.equal(opened.snapshot.session.model, "deepseek/deepseek-flash");
  assert.equal(opened.snapshot.session.thinking, "low");
});
