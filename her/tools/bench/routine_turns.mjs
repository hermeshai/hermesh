#!/usr/bin/env node
// How a real model handles routines (docs/kernel/routine-design.md §12). Each scenario runs N times
// in a fresh bot over `her serve` and counts:
//   explicit, watch:  a routine was made when asked for one, with the right schedule; whether a
//                     message went with the call that made it or after its result, whether one
//                     after it names a time, whether any says the computer has to be on (the
//                     chat's own line under the call shows the next run either way);
//   implied:          it offered and did not make one when it only seemed useful;
//   system:           it reached for crontab, launchctl or a long sleep instead (should be 0);
//   quiet:            a run with nothing to say replied NOTHING_TO_REPORT, and what the bot did
//                     when it did not;
//   relay:            a run with something to say reached the owner, without talking about runs,
//                     and whether that took the reminder;
//   nothing:          a run told to answer in words of its own wakes the bot with nothing to tell:
//                     that should not fail, and should not reach the owner.
// It prints each saved schedule and prompt, to read by hand, and what each kind of run cost.
//
//   DEEPSEEK_API_KEY=… node tools/bench/routine_turns.mjs [runs] [scenario…]
//   HER_MODEL=<provider/model>   another model
//   HER_MODELS_TOML=<file>       a models.toml for it, when it is not a built-in one
//
// It uses a home of its own, so the person's ~/.her is not touched.
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import readline from "node:readline";

const root = path.resolve(import.meta.dirname, "../..");
const her = process.env.HER_BIN ?? path.join(root, "target/debug/her");
const runs = Number(process.argv[2] ?? 12);
const only = process.argv.slice(3);
const home = fs.mkdtempSync(path.join(os.tmpdir(), "fxr-"));
fs.mkdirSync(path.join(home, "user"), { recursive: true });
fs.writeFileSync(path.join(home, "config.toml"), `model = "${process.env.HER_MODEL ?? "deepseek/deepseek-flash"}"\npermission_mode = "bypass-permissions"\n`);
if (process.env.HER_MODELS_TOML) fs.copyFileSync(process.env.HER_MODELS_TOML, path.join(home, "models.toml"));

const server = spawn(her, ["serve", "--stdio"], { env: { ...process.env, HER_HOME: home, HOME: path.join(home, "user") }, stdio: ["pipe", "pipe", "inherit"] });
let next = 1;
const waiting = new Map();
const listeners = new Set();
readline.createInterface({ input: server.stdout }).on("line", (line) => {
  const msg = JSON.parse(line);
  if (msg.id !== undefined && waiting.has(msg.id)) {
    const w = waiting.get(msg.id);
    waiting.delete(msg.id);
    msg.error ? w.reject(new Error(msg.error.message)) : w.resolve(msg.result);
  } else if (msg.method) for (const f of listeners) f(msg);
});
const call = (method, params = {}) =>
  new Promise((resolve, reject) => {
    const id = next++;
    waiting.set(id, { resolve, reject });
    server.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
  });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Events of `session` until it is idle, or `ms` pass. */
function collect(session, ms) {
  const events = [];
  return {
    events,
    idle: new Promise((resolve) => {
      const timer = setTimeout(() => (listeners.delete(on), resolve(false)), ms);
      const on = (msg) => {
        if (msg.method !== "session/event" || msg.params.session_id !== session) return;
        events.push(msg.params.event);
        if (msg.params.event.type === "idle") {
          clearTimeout(timer);
          listeners.delete(on);
          resolve(true);
        }
      };
      listeners.add(on);
    }),
  };
}

async function freshBot(run) {
  const { bot } = await call("bots/create", { name: `测试${run + 1}`, avatar: { kind: "mono", hue: "slate" } });
  const opened = await call("bots/open", { bot_id: bot.id, events: { exclude: ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"] } });
  return { id: bot.id, session: opened.session_id, folder: path.join(home, "bots", bot.id, "files") };
}

/** Says something and waits until it is done with it. */
async function ask(bot, text) {
  const c = collect(bot.session, 240_000);
  await call("session/op", { session_id: bot.session, op: { type: "prompt", input: [{ type: "text", text }] } });
  const quiet = await c.idle;
  report(c.events);
  return { events: c.events, quiet };
}

/** Why a task did not end well: what these counts leave out. */
function report(events) {
  for (const e of events) {
    if (e.type === "task_failed") console.log(`   (task failed: ${e.error?.kind}: ${String(e.error?.message).slice(0, 200)})`);
    // A key that is refused makes every count that follows meaningless.
    if (e.type === "task_failed" && e.error?.kind === "auth") {
      console.log("the key was refused: stopping");
      server.kill();
      process.exit(2);
    }
    if (e.type === "stream_retry") console.log(`   (retrying: ${String(e.error?.message ?? e.message ?? "").slice(0, 120)})`);
    if (e.type === "warning" && !/^Permission mode/.test(e.message)) console.log(`   (warning: ${String(e.message).slice(0, 160)})`);
    if (e.type === "tool_completed" && e.result?.is_error) console.log(`   (${e.result.tool_name} failed: ${(e.result.content ?? []).map((c) => c.text ?? "").join("").slice(0, 200)})`);
  }
  const ended = events.filter((e) => e.type === "task_completed" || e.type === "task_failed" || e.type === "task_interrupted").map((e) => e.type);
  if (process.env.HER_BENCH_VERBOSE) console.log(`   (events: ${events.map((e) => e.type).join(" ")})`);
  return ended;
}

const sends = (events) => events.filter((e) => e.type === "tool_started" && e.name === "send_to_user").map((e) => String(e.args.text ?? ""));
const makes = (events) => events.filter((e) => e.type === "tool_started" && e.name === "manage_routine" && e.args.action === "create");
const system = (events) => events.filter((e) => e.type === "tool_started" && e.name === "bash" && /crontab|launchctl|launchd|\bsleep\s+\d{2,}/.test(String(e.args.command ?? ""))).length;
/** The bot's replies, each as the calls it made. */
const replies = (events) => events.filter((e) => e.type === "message_completed" && e.message?.role === "assistant").map((e) => e.message.content.filter((x) => x.type === "tool_call"));
const texts = (calls) => calls.filter((x) => x.name === "send_to_user").map((x) => String(x.arguments?.text ?? ""));
/** Replies as they came, to read by hand: `!` on a message that ends the turn, `-` for a reply with no calls. */
const shape = (events) => replies(events).map((calls) => calls.map((x) => x.name + (x.arguments?.end_turn ? "!" : "")).join("+") || "-").join(" | ");
/** Words written after the last send, which stay private: lost if they were meant for the owner. */
function lastWords(events) {
  const all = events.filter((e) => e.type === "message_completed" && e.message?.role === "assistant").map((e) => e.message.content);
  const last = all.at(-1) ?? [];
  if (last.some((x) => x.type === "tool_call") || !all.slice(0, -1).some((c) => c.some((x) => x.type === "tool_call" && x.name === "send_to_user"))) return "";
  return last.filter((x) => x.type === "text").map((x) => x.text).join("").trim();
}
/** What its last reply wrote, whatever came before. */
function wrote(events) {
  const last = events.filter((e) => e.type === "message_completed" && e.message?.role === "assistant").at(-1);
  return (last?.message.content ?? []).filter((x) => x.type === "text").map((x) => x.text).join("").trim();
}
/** What it ran in the shell, to read by hand. */
const shells = (events) => events.filter((e) => e.type === "tool_started" && e.name === "bash").map((e) => String(e.args.command ?? ""));
/** A reply that ends the turn while it also calls something else, whose result the model then never reads. */
const endsBlind = (events) => replies(events).some((calls) => calls.length > 1 && calls.some((x) => x.arguments?.end_turn));
const makesOne = (calls) => calls.some((x) => x.name === "manage_routine" && x.arguments?.action === "create");
/** What the bot sent in the replies after the one that made a routine: it had the result by then. */
function sentAfterSaving(events) {
  const all = replies(events);
  const made = all.findIndex(makesOne);
  return made < 0 ? [] : all.slice(made + 1).flatMap(texts);
}
/** Whether a message tells a time. */
const tellsTime = (texts) => texts.some((t) => /\d{1,2}\s*[:：点]|\d{1,2}\s*(am|pm)|明天|明早|今晚|下次|第一次|首次/i.test(t));
/** Whether a message says the computer has to be on. */
const tellsAwake = (texts) => texts.some((t) => /电脑|Mac|开机|开着|醒着|睡着|合盖|休眠|睡眠/i.test(t));
/** Words that give away how it works, in a message to the owner. */
const leaks = /运行记录|这次运行|定时运行|运行结果|汇报|routine_report|run\b|NOTHING_TO_REPORT/i;

async function routinesOf(bot) {
  const listed = await call("bots/subscribe", {});
  return listed.bots.find((b) => b.id === bot.id)?.routines ?? [];
}

async function detail(bot, id) {
  const { routine } = await call("bots/routine", { bot_id: bot.id, routine_id: id });
  return routine;
}

const asks = {
  explicit: { text: "每天早上 8 点提醒我喝水", schedule: /^0 8 \* \* \*$/ },
  evening: { text: "每个工作日晚上 9 点半提醒我写日报", schedule: /^30 21 \* \* 1-5$/ },
  once: { text: "20 分钟后提醒我关火", schedule: /T.*[+-]\d\d:\d\d$/ },
  watch: { text: "特斯拉股价跌破 300 美元的话告诉我", schedule: /CRON_TZ=America\/New_York|\S/ },
};

const totals = {};
const costs = { quiet: [], reported: [], relay: [], chat: [] };
const tokens = (usage) => Object.values(usage?.per_model ?? {}).reduce((n, m) => n + (m.input ?? 0) + (m.output ?? 0) + (m.cache_read ?? 0) + (m.cache_write ?? 0), 0);

await call("initialize", { protocol_version: 2, client_info: { name: "routine-turns" } });
await call("bots/subscribe", {});

for (const [name, want] of Object.entries(asks)) {
  if (only.length && !only.includes(name)) continue;
  const t = (totals[name] = { runs: 0, made: 0, right: 0, sentWithCall: 0, endedBlind: 0, sentAfter: 0, timeAfter: 0, awake: 0, lostWords: 0, system: 0, timedOut: 0 });
  for (let run = 0; run < runs; run++) {
    const bot = await freshBot(run);
    const { events, quiet } = await ask(bot, want.text);
    t.runs++;
    t.timedOut += quiet ? 0 : 1;
    t.system += system(events);
    const made = makes(events).length > 0;
    t.made += made ? 1 : 0;
    const said = sends(events);
    const after = sentAfterSaving(events);
    if (replies(events).some((calls) => makesOne(calls) && texts(calls).length)) t.sentWithCall++;
    if (endsBlind(events)) t.endedBlind++;
    if (after.length) t.sentAfter++;
    if (tellsTime(after)) t.timeAfter++;
    if (tellsAwake(said)) t.awake++;
    const lost = lastWords(events);
    if (lost) t.lostWords++;
    const routines = await routinesOf(bot);
    if (routines[0]) {
      const d = await detail(bot, routines[0].id);
      if (want.schedule.test(d.schedule)) t.right++;
      console.log(`${name} #${run + 1}: [${d.schedule}] when="${routines[0].when}"\n   prompt: ${d.prompt.replace(/\s+/g, " ").slice(0, 300)}`);
    } else console.log(`${name} #${run + 1}: no routine`);
    console.log(`   replies: ${shape(events)}`);
    for (const s of shells(events)) console.log("   $", s.replace(/\s+/g, " ").slice(0, 120));
    for (const s of said) console.log("   ·", s.replace(/\s+/g, " ").slice(0, 160));
    if (lost) console.log("   (never reached the owner)", lost.replace(/\s+/g, " ").slice(0, 200));
    await call("bots/delete", { bot_id: bot.id });
  }
}

// Only useful, not asked for: it should offer and wait.
if (!only.length || only.includes("implied")) {
  const t = (totals.implied = { runs: 0, made: 0, offered: 0, system: 0 });
  for (let run = 0; run < runs; run++) {
    const bot = await freshBot(run);
    const { events } = await ask(bot, "我每周一都要看一遍这张销售表，这周的数字是 A 100、B 200、C 150，帮我算一下总数。");
    t.runs++;
    t.system += system(events);
    if (makes(events).length) t.made++;
    const task = events.find((e) => e.type === "task_completed");
    if (task?.usage) costs.chat.push(tokens(task.usage));
    const said = sends(events);
    if (said.some((s) => /定时|每周一|每周|提醒|自动/.test(s) && /[?？吗]|要不要|需要的话/.test(s))) t.offered++;
    console.log(`implied #${run + 1}: ${makes(events).length ? "made one" : "did not make one"}`);
    for (const s of said) console.log("   ·", s.replace(/\s+/g, " ").slice(0, 120));
    await call("bots/delete", { bot_id: bot.id });
  }
}

/** A routine that looks at a file in the bot's folder, so that the test decides what it finds. */
async function watching(bot) {
  await ask(bot, "每小时看一下你文件夹里的 status.txt：里面有 ALERT 就告诉我写了什么，否则不用说。");
  const routines = await routinesOf(bot);
  return routines[0];
}

async function runNow(bot, routine, ms = 240_000) {
  const c = collect(bot.session, ms);
  await call("bots/routine_run_now", { bot_id: bot.id, routine_id: routine.id });
  // Wait for its record: a quiet run leaves the session alone.
  const end = Date.now() + ms;
  let record;
  for (;;) {
    await sleep(500);
    const { runs: all } = await call("bots/routine_runs", { bot_id: bot.id, routine_id: routine.id, limit: 1 });
    record = all[0];
    if (record?.outcome || Date.now() > end) break;
  }
  return { c, record };
}

if (!only.length || only.includes("quiet")) {
  const t = (totals.quiet = { runs: 0, quiet: 0, reported: 0, failed: 0, woke: 0, toldAnyway: 0 });
  for (let run = 0; run < runs; run++) {
    const bot = await freshBot(run);
    const routine = await watching(bot);
    if (!routine) {
      console.log(`quiet #${run + 1}: no routine was made`);
      continue;
    }
    const saved = await detail(bot, routine.id);
    fs.writeFileSync(path.join(bot.folder, "status.txt"), "一切正常\n");
    const { c, record } = await runNow(bot, routine);
    t.runs++;
    t[record?.outcome === "quiet" ? "quiet" : record?.outcome === "failed" ? "failed" : "reported"]++;
    console.log(`quiet #${run + 1}: ${record?.outcome}: ${(record?.note ?? record?.report ?? "").replace(/\s+/g, " ").slice(0, 160)}`);
    console.log(`   prompt: ${saved.prompt.replace(/\s+/g, " ").slice(0, 300)}`);
    if (record?.outcome === "quiet") {
      await sleep(1000);
      if (record?.usage) costs.quiet.push(tokens(record.usage));
    } else {
      // A forgotten phrase wakes the bot: what it then does with nothing to tell.
      await c.idle;
      report(c.events);
      console.log(`   replies: ${shape(c.events)}`);
      console.log(`   wrote: ${wrote(c.events).replace(/\s+/g, " ").slice(0, 160)}`);
      for (const s of sends(c.events)) console.log("   · (told the owner)", s.replace(/\s+/g, " ").slice(0, 160));
      if (sends(c.events).length) t.toldAnyway++;
    }
    if (c.events.some((e) => e.type === "task_started")) t.woke++;
    await call("bots/delete", { bot_id: bot.id });
  }
}

// Rare by itself: a saved instruction that names its own words for "nothing", which the run follows.
if (!only.length || only.includes("nothing")) {
  const t = (totals.nothing = { runs: 0, quiet: 0, woke: 0, reminded: 0, told: 0, failed: 0 });
  for (let run = 0; run < runs; run++) {
    const bot = await freshBot(run);
    await ask(bot, "每小时看一下你文件夹里的 status.txt：里面有 ALERT 就告诉我写了什么。没有的话让它在报告里只写“无 ALERT”，你看到这句就不用告诉我。");
    const routine = (await routinesOf(bot))[0];
    if (!routine) {
      console.log(`nothing #${run + 1}: no routine was made`);
      continue;
    }
    const saved = await detail(bot, routine.id);
    fs.writeFileSync(path.join(bot.folder, "status.txt"), "一切正常\n");
    const { c, record } = await runNow(bot, routine);
    t.runs++;
    console.log(`nothing #${run + 1}: ${record?.outcome}: ${(record?.note ?? record?.report ?? "").replace(/\s+/g, " ").slice(0, 160)}`);
    console.log(`   prompt: ${saved.prompt.replace(/\s+/g, " ").slice(0, 300)}`);
    if (record?.outcome !== "reported") {
      t.quiet++;
    } else {
      t.woke++;
      await c.idle;
      report(c.events);
      console.log(`   replies: ${shape(c.events)}`);
      console.log(`   wrote: ${wrote(c.events).replace(/\s+/g, " ").slice(0, 160)}`);
      if (replies(c.events).slice(0, -1).some((calls) => !calls.length)) t.reminded++;
      if (c.events.some((e) => e.type === "task_failed")) t.failed++;
      const said = sends(c.events);
      if (said.length) t.told++;
      for (const s of said) console.log("   · (told the owner)", s.replace(/\s+/g, " ").slice(0, 160));
    }
    await call("bots/delete", { bot_id: bot.id });
  }
}

if (!only.length || only.includes("relay")) {
  const t = (totals.relay = { runs: 0, reported: 0, missed: 0, reached: 0, reminded: 0, lostWords: 0, failed: 0, leaked: 0, delivered: 0 });
  for (let run = 0; run < runs; run++) {
    const bot = await freshBot(run);
    const routine = await watching(bot);
    if (!routine) {
      console.log(`relay #${run + 1}: no routine was made`);
      continue;
    }
    const saved = await detail(bot, routine.id);
    fs.writeFileSync(path.join(bot.folder, "status.txt"), "ALERT 服务器磁盘只剩 3%\n");
    const { c, record } = await runNow(bot, routine);
    t.runs++;
    if (record?.outcome !== "reported") {
      // It found the alert and did not say so: the bot hears nothing, unless the hour comes round.
      t.missed++;
      console.log(`relay #${run + 1}: ${record?.outcome}: ${(record?.note ?? "").replace(/\s+/g, " ").slice(0, 200)}`);
      console.log(`   prompt: ${saved.prompt.replace(/\s+/g, " ").slice(0, 400)}`);
      await call("bots/delete", { bot_id: bot.id });
      continue;
    }
    t.reported++;
    await c.idle;
    report(c.events);
    const said = sends(c.events);
    if (said.length) t.reached++;
    if (c.events.some((e) => e.type === "task_failed")) t.failed++;
    // A reply with no calls ends the task, unless the reminder asked it to go on.
    if (replies(c.events).slice(0, -1).some((calls) => !calls.length)) t.reminded++;
    if (said.some((s) => leaks.test(s))) t.leaked++;
    const { runs: after } = await call("bots/routine_runs", { bot_id: bot.id, routine_id: routine.id, limit: 1 });
    if (after[0]?.delivered) t.delivered++;
    if (record?.usage) costs.reported.push(tokens(record.usage));
    const task = c.events.find((e) => e.type === "task_completed");
    if (task?.usage) costs.relay.push(tokens(task.usage));
    console.log(`relay #${run + 1}: ${record?.outcome}: ${said.length} messages`);
    console.log(`   replies: ${shape(c.events)}`);
    const lost = lastWords(c.events);
    if (lost) {
      t.lostWords++;
      console.log("   (never reached the owner)", lost.replace(/\s+/g, " ").slice(0, 200));
    }
    for (const s of said) console.log("   ·", s.replace(/\s+/g, " ").slice(0, 160));
    await call("bots/delete", { bot_id: bot.id });
  }
}

const mean = (list) => (list.length ? Math.round(list.reduce((a, b) => a + b, 0) / list.length) : 0);
console.log("\n" + JSON.stringify(totals, null, 1));
console.log(`\ntokens, mean: a plain reply ${mean(costs.chat)}; a run, quiet ${mean(costs.quiet)}, with something to say ${mean(costs.reported)}; the bot passing it on ${mean(costs.relay)}`);
server.kill();
fs.rmSync(home, { recursive: true, force: true });
// A quiet run's wait for the bot's session has nothing to end it.
process.exit(0);
