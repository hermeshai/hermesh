import assert from "node:assert/strict";
import { test } from "node:test";
import type { AssistantContent, Event, EventMsg, SessionInfo, Usage } from "@hermesh/her-protocol";
import { applyEvents, callTarget, fromSnapshot, type SessionView } from "./session.ts";
import { approvalPreview, attachedBlock, docQuoteBlock, docViewingBlock, presentationViewingBlock, sheetViewingBlock, webCommentBlock, conversation, describe, settledSteps, withStreaming, toolObject, userText, workSummary } from "./steps.ts";
import { clockText, costText, tokensText } from "./text.ts";

const info: SessionInfo = { session_id: "s", cwd: "/w/app", session_file: "/f.jsonl", model: "p/m", thinking: "off", permission_mode: "manual", context_window: 1000, archived: false };
const usage: Usage = { input: 1, output: 1, cache_read: 0, cache_write: 0, cost: { currency: "USD", input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } };

function play(msgs: EventMsg[]): SessionView {
  const events = msgs.map((m, i) => ({ seq: i + 1, ts: 1000 + i, ...m }) as Event);
  return applyEvents(fromSnapshot({ session: info, last_seq: 0, transcript: { items: [] }, usage: { per_model: {} } }), events);
}

const assistant = (id: string, content: AssistantContent[]): EventMsg => ({
  type: "message_completed",
  message_id: id,
  message: { role: "assistant", content, provider: "p", api: "a", model: "m", usage, stop_reason: "stop", ts: 1 },
});
const result = (id: string, text: string, is_error = false): EventMsg => ({
  type: "tool_completed",
  call_id: id,
  result: { call_id: id, tool_name: "bash", content: [{ type: "text", text }], is_error, ts: 2 },
});

test("thinking and tool calls between the model's words become one step; words and messages stay apart", () => {
  const v = play([
    { type: "task_started", task_id: "t1" },
    { type: "message_completed", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "hi" }], ts: 1 } },
    assistant("a1", [
      { type: "thinking", text: "look first" },
      { type: "text", text: "Looking." },
      { type: "tool_call", id: "c1", name: "bash", arguments: { command: "ls -la\nmore" } },
    ]),
    { type: "tool_started", call_id: "c1", name: "bash", args: { command: "ls -la" }, category: "execute", title: "ls -la" },
    { type: "tool_running", call_id: "c1" },
    result("c1", "a\nb"),
    assistant("a2", [
      { type: "tool_call", id: "c2", name: "read", arguments: { path: "/w/app/src/main.rs" } },
      { type: "tool_call", id: "c3", name: "edit", arguments: { path: "/w/app/src/main.rs" } },
    ]),
    result("c2", "fn main() {}"),
    { type: "message_started", message_id: "a3", role: "assistant" },
    { type: "message_delta", message_id: "a3", delta: { type: "text", index: 0, text: "Do" } },
    { type: "message_delta", message_id: "a3", delta: { type: "text", index: 0, text: "ne" } },
  ]);
  const steps = conversation(v);
  assert.deepEqual(steps.map((s) => s.kind), ["user", "work", "text", "work", "text"]);
  const work = steps[3];
  assert.ok(work.kind === "work");
  assert.deepEqual(work.items.map((i) => (i.kind === "tool" ? `${i.tool.name}:${i.tool.state}` : "thinking")), ["bash:done", "read:done", "edit:pending"]);
  assert.equal(work.live, true);
  const draft = steps.at(-1);
  assert.ok(draft?.kind === "text" && draft.live && draft.text === "Done");
  // The edit is still to run in the task in progress; once the task ends without running it, it was not run.
  const ended = applyEvents(v, [{ seq: 100, ts: 9, type: "task_interrupted", task_id: "t1", returned_input: [], usage: { per_model: {} } }]);
  const w = conversation(ended).find((s) => s.kind === "work" && s.items.length === 3);
  assert.ok(w?.kind === "work" && w.items[2].kind === "tool" && w.items[2].tool.state === "cancelled");
});

test("what is being written goes after the settled steps, which stay the same objects", () => {
  const before: EventMsg[] = [
    { type: "task_started", task_id: "t1" },
    { type: "message_completed", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "hi" }], ts: 1 } },
    assistant("a1", [{ type: "thinking", text: "first" }]),
  ];
  const v = play([...before, { type: "message_started", message_id: "a2", role: "assistant" }, { type: "message_delta", message_id: "a2", delta: { type: "thinking", index: 0, text: "more" } }]);
  const settled = settledSteps(v);
  const [, group] = settled;
  assert.equal(group.kind === "work" && group.items.length, 1);
  const steps = withStreaming(settled, v);
  // The thinking joins the group before it, in a copy.
  assert.equal(steps.length, settled.length);
  assert.ok(steps[0] === settled[0]);
  const joined = steps.at(-1)!;
  assert.deepEqual(joined.kind === "work" && [joined.items.length, joined.live], [2, true]);
  assert.deepEqual(group.kind === "work" && [group.items.length, group.live], [1, false]);
  assert.deepEqual(steps, conversation(v));
  // Nothing being written: the very same list.
  const quiet = play(before);
  const still = settledSteps(quiet);
  assert.equal(withStreaming(still, quiet), still);
});

test("a group's line counts its calls kind by kind; thinking counts only alone", () => {
  const tool = (category: "read" | "execute", state: "done" | "denied" = "done") =>
    ({ kind: "tool", key: Math.random().toString(), tool: { call_id: "", name: "", category, title: "", args: {}, state } }) as const;
  assert.deepEqual(workSummary([tool("read"), tool("execute"), tool("read"), { kind: "thinking", key: "k", text: "", redacted: false, live: false }]), [
    { kind: "read", count: 2 },
    { kind: "execute", count: 1 },
  ]);
  assert.deepEqual(workSummary([tool("execute", "denied")]), [{ kind: "denied", count: 1 }]);
  assert.deepEqual(workSummary([{ kind: "thinking", key: "k", text: "", redacted: false, live: false }]), [{ kind: "thinking", count: 1 }]);
});

test("the built-in tools are described from their arguments, paths relative to the project", () => {
  assert.deepEqual(describe("bash", { command: "cargo test\n--nocapture" }, "/w"), { category: "execute", title: "cargo test …" });
  assert.deepEqual(describe("read", { path: "/w/src/a.rs" }, "/w"), { category: "read", title: "read src/a.rs" });
  assert.deepEqual(describe("write", { path: "/elsewhere/b" }, "/w"), { category: "edit", title: "write /elsewhere/b" });
  assert.equal(toolObject({ name: "read", title: "read src/a.rs", category: "read" }), "src/a.rs");
  assert.equal(toolObject({ name: "bash", title: "read me", category: "execute" }), "read me");
});

test("an approval previews the command, the change, or what is written", () => {
  assert.deepEqual(approvalPreview("bash", { command: "rm -rf build" }, "x"), { kind: "command", text: "rm -rf build" });
  assert.deepEqual(approvalPreview("edit", { path: "a", edits: [{ old_text: "x\ny\n", new_text: "z" }] }, ""), {
    kind: "diff",
    lines: [{ sign: "-", text: "x" }, { sign: "-", text: "y" }, { sign: "+", text: "z" }],
    more: 0,
  });
  const long = approvalPreview("write", { path: "a", content: "1\n2\n3\n4" }, "", 3);
  assert.deepEqual(long, { kind: "content", lines: ["1", "2", "3"], more: 1 });
  assert.deepEqual(approvalPreview("fetch", { url: "u" }, "fetch u"), { kind: "args", text: "fetch u" });
  assert.deepEqual(approvalPreview("browser_eval", { expression: "document.title" }, "run a script on https://a.example"), {
    kind: "content",
    lines: ["document.title"],
    more: 0,
  });
});

test("a message's attached files go to the model as a block of paths and come back as files, not words", () => {
  const content = [{ type: "text", text: "look at these" }, { type: "image", mime_type: "image/png", data: "AAAA" }, attachedBlock(["/w/a.rs", "/w/docs/"])] as const;
  assert.deepEqual(userText([...content]), { text: "look at these", files: ["/w/a.rs", "/w/docs/"], comments: [], quotes: [], tabs: [] });
  assert.deepEqual(userText([attachedBlock(["/w/b"])]), { text: "", files: ["/w/b"], comments: [], quotes: [], tabs: [] });
  const web = { url: "https://example.com/", title: "Example", tag: "h1", text: "Hello", selector: "#h", comment: "shorter" };
  assert.deepEqual(userText([{ type: "text", text: "see" }, webCommentBlock([web])]), { text: "see", files: [], comments: [web], quotes: [], tabs: [] });
  const v = play([{ type: "message_completed", message_id: "u1", message: { role: "user", content: [...content], ts: 1 } }]);
  const [u] = conversation(v);
  assert.ok(u.kind === "user");
  assert.equal(u.text, "look at these");
  assert.deepEqual(u.images, [{ mime_type: "image/png", data: "AAAA" }]);
  assert.deepEqual(u.files, ["/w/a.rs", "/w/docs/"]);
});

test("an attached workbook tells the model to use Her document tools and remains a file chip", () => {
  const workbook = attachedBlock(["/w/model.XLSX"]);
  assert.equal(workbook.type, "text");
  assert.match(workbook.text, /doc_open, then doc_read/);
  assert.match(workbook.text, /instead of .*shell\/Python/);
  assert.deepEqual(userText([workbook]), { text: "", files: ["/w/model.XLSX"], comments: [], quotes: [], tabs: [] });

  // Messages written before the workbook hint was added still render as file chips.
  assert.deepEqual(userText([{ type: "text", text: "Attached files:\n/w/old.xlsx" }]), {
    text: "",
    files: ["/w/old.xlsx"],
    comments: [],
    quotes: [],
    tabs: [],
  });
});

test("numbers as the window shows them", () => {
  assert.equal(clockText(252), "4:12");
  assert.equal(clockText(3723), "1:02:03");
  assert.equal(tokensText(950), "950");
  assert.equal(tokensText(1500), "1.5K");
  assert.equal(tokensText(412_000), "412K");
  assert.equal(tokensText(1_800_000), "1.8M");
  assert.equal(costText({ currency: "CNY", total: 0.42 }), "¥0.42");
  assert.equal(costText({ currency: "USD", total: 0.004 }), "$0.0040");
});

test("a coded warning becomes a notice, the latest per code, and leaving bypass-permissions drops its notice", () => {
  const bypass: EventMsg = { type: "warning", message: "Permission mode bypass-permissions: …", code: "bypass_permissions" };
  const mode = (permission_mode: "manual" | "bypass-permissions"): EventMsg => ({ type: "settings_changed", model: "p/m", thinking: "off", permission_mode, context_window: 1000 });
  const v = play([bypass, { type: "warning", message: "nothing to compact" }, bypass]);
  assert.deepEqual(v.notices.map((n) => [n.code, n.ts]), [["bypass_permissions", 1002]]);
  assert.deepEqual(v.entries.map((e) => (e.type === "note" && e.note === "warning" ? e.message : e.type)), ["nothing to compact"]);
  assert.equal(play([bypass, mode("bypass-permissions")]).notices.length, 1);
  assert.deepEqual(play([bypass, mode("manual")]).notices, []);
});

test("a call through call_tool is told as the tool it runs", () => {
  assert.deepEqual(callTarget("call_tool", { name: "cua_snapshot", arguments: { app: "com.apple.finder" } }), { name: "cua_snapshot", args: { app: "com.apple.finder" } });
  assert.deepEqual(callTarget("read", { path: "a" }), { name: "read", args: { path: "a" } });
  assert.deepEqual(callTarget("call_tool", {}), { name: "call_tool", args: {} });
  assert.equal(describe("call_tool", { name: "read", arguments: { path: "/p/a.md" } }, "/p").title, "read a.md");
  assert.deepEqual(describe("call_tool", { name: "web_search", arguments: { query: "贵州茅台 三季报" } }, "/p"), { category: "search", title: "贵州茅台 三季报" });
});

test("quoted parts of documents come back as quotes, and what the column shows is not shown", () => {
  const quote = { path: "/w/合同.pdf", doc_id: "0123456789abcdef", page: 3, text: "违约金为合同总额的 20%", block: "p3.b12" };
  const content = [{ type: "text" as const, text: "这条合理吗" }, docQuoteBlock([quote]), docViewingBlock("/w/合同.pdf", "0123456789abcdef", 3, 12)];
  assert.deepEqual(userText(content), { text: "这条合理吗", files: [], comments: [], quotes: [quote], tabs: [] });
  assert.match((content[2] as { text: string }).text, /page 3 of 12/);
});

test("a workbook range comes back as a quote, and its live selection is not shown", () => {
  const quote = { path: "/w/model.xlsx", doc_id: "fedcba9876543210", sheet: "问卷", range: "B3:D6", text: "| | B | C | D |" };
  const content = [{ type: "text" as const, text: "分析这个区域" }, docQuoteBlock([quote]), sheetViewingBlock(quote.path, quote.doc_id, quote.sheet, quote.range, "1,344.00")];
  assert.deepEqual(userText(content), { text: "分析这个区域", files: [], comments: [], quotes: [quote], tabs: [] });
});

test("the open presentation goes along with a message but is not shown", () => {
  const block = presentationViewingBlock("/w/deck.pptx", false);
  assert.match((block as { text: string }).text, /deck\.pptx/);
  assert.equal(userText([{ type: "text", text: "当前打开的ppt内容是啥" }, block]).text, "当前打开的ppt内容是啥");
});
