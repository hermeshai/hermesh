#!/usr/bin/env node
// How a real model keeps a bot's turn rules (docs/kernel/bot-design.md §8, bot-turn-design §11,
// agent-messaging-design §13):
// each scenario runs N times in a fresh bot over `her serve`, and counts
//   - first:      the first call after the owner's message is send_to_user (not the aim in `stop`:
//                 there the stop comes first);
//   - missed:     a task the owner started ended without sending anything;
//   - failed:     a task failed for another reason (a model error, say): the run says nothing
//                 about its turns;
//   - late:       a task sent only after the kernel's reminder: its first reply had no call at all;
//   - unanswered, undelivered: the kernel's reminders, from the session's log;
//   - leaked:     a message names a tool or carries an id;
//   - asides:     a whole message in brackets, the bot talking to itself;
//   - internal:   a message speaks of sub-agents (it should only when asked how it works);
//   - delegated:  long work went to a sub-agent;
//   - delivered:  of the runs with a report, those with a message sent after the last one;
//   - firstMs:    from the owner's first message to the first bubble, median and worst;
//   - nextMs:     from each later message to the next bubble, median and worst;
//   - during:     later messages answered while a sub-agent still worked, of those asked then;
//   - ordered:    `poems`, the four poems came back in the order asked, one message each;
//   - attached:   a file went out as an attachment message; linked: only as a link in words;
//   - handed, pointed, kept: `report`, a file the run made went out in files; was only named in
//                 words; was made and never named; alsoLinked: handed, and named in words too;
//                 noFile: the run made no file;
//   - asked:     a choice went out as a question with options; prose: only as words ending in a
//                 question;
//   - rejected:   a send the kernel refused (printed with why): it costs a round trip, the owner
//                 sees nothing;
//   - stopFirst, stopped, told, restarted: `stop` and `session`, the agents stopped before the bot
//                 said so, stopped at all, it said so, it started the work again;
//   - checked:    `progress` and `session`, list_agents ran after the question;
//   - lastAutumn, spring: `change`, the last message is the poem asked for last; the first one
//                 went out too, which is acceptable;
//   - agents:     `fan`, `apart`, `whole`, `follow`, how many runs started how many sub-agents:
//                 separate requests get one each, one job or a correction to it gets one;
//   - redirected: `follow`, the correction went to the agent at work;
//   - notes:      what sub-agents sent the main agent on the way, each printed; a job that can be
//                 split should need none;
//   - wokenByNote: tasks those messages started: what they cost;
//   - answered:   the main agent's send_message after a note, to see that nobody goes back and forth;
//   - peerNotes:  what sub-agents sent one another;
//   - updates:    what sub-agents told the owner themselves (send_to_user), each printed;
//   - updateRuns: runs with at least one; updateLeaks, updateInternal: of them, naming a tool or
//                 the machinery; updateOtherLanguage: with no Chinese, when the owner writes Chinese;
//   - updateFiller: of them, ones that only say what it is about to do or is doing;
//   - firstUpdateMs: from the first agent's start to the first update, median and worst;
//   - gapMs:      the longest the owner went without a word while agents worked, per run; workMs: how
//                 long they worked; updateChars: how long an update is;
//   - botSteps:   the requests the bot made: an update must not add any;
//   - repeated:   the bot's messages that say again what an update said;
//   - covered:    `ten`, runs in which the owner heard of all ten companies;
//   - early:      `blocker`, `milestone`: the owner heard before the last report;
//   - inTask:     `milestone`, the bot asked for the step in the agent's task;
//   - alone:      the first reply was one send_to_user and nothing else;
//   - thinking:   characters of thinking before the first reply's calls, median and worst;
//   - ownRounds:  `complex`, `medium`, `simple`, `lookup`, steps of tool calls the bot ran itself
//                 before it started an agent, or in all when it started none.
// Messages are printed, to read for the rest: does an acknowledgement name its first step, does
// what it says of the progress match.
//
//   DEEPSEEK_API_KEY=… node tools/bench/bot_turns.mjs [runs] [scenario…]
//   HER_BOT_PROMPT=<file>     tries another bot.md (bot_prompt_file)
//   HER_BENCH_THINKING=<lvl>  the bots' thinking level
//   HER_BENCH_PARALLEL=<n>    runs at a time, 3 by default
//   HER_BENCH_MODELS=<file>   a models.toml, with HER_MODEL: another provider, or her-mock to try
//                              the script itself
//   HER_BENCH_KEEP=1          keeps the home, with every session's log, to read the thinking
//   HER_BENCH_SEED=<file>     each run's bot starts with this conversation (a session log with
//                              __FILES__ for the bot's folder and __USER__ for the home folder):
//                              a bot with a long history behaves unlike a new one
//   HER_BENCH_APP=1           connects as the desktop app does, so the bots get its section of the
//                              prompt; browser tools then hang, as nothing here serves them
//   HER_BENCH_ASK=<text>      `ask`, `report`: the owner's words
//   HER_BENCH_FILES=<dir>     `report`: what the bot's folder starts with, the report already written
//
// It uses a home of its own, so the person's ~/.her is not touched.
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import readline from "node:readline";

const root = path.resolve(import.meta.dirname, "../..");
const her = process.env.HER_BIN ?? path.join(root, "target/debug/her");
const runs = Number(process.argv[2] ?? 12);
const only = process.argv.slice(3);
const parallel = Number(process.env.HER_BENCH_PARALLEL ?? 3);
const home = fs.mkdtempSync(path.join(os.tmpdir(), "fxb-"));
const config = [`model = "${process.env.HER_MODEL ?? "deepseek/deepseek-flash"}"`, `permission_mode = "accept-edits"`];
if (process.env.HER_BENCH_THINKING) config.push(`thinking = "${process.env.HER_BENCH_THINKING}"`);
if (process.env.HER_BOT_PROMPT) config.push(`bot_prompt_file = "${path.resolve(process.env.HER_BOT_PROMPT)}"`);
fs.writeFileSync(path.join(home, "config.toml"), config.join("\n") + "\n");
if (process.env.HER_BENCH_MODELS) fs.copyFileSync(process.env.HER_BENCH_MODELS, path.join(home, "models.toml"));

fs.mkdirSync(path.join(home, "user"), { recursive: true });
// Bots with a conversation already: the service reads them when it starts.
const seeded = [];
if (process.env.HER_BENCH_SEED) {
  const seed = fs.readFileSync(process.env.HER_BENCH_SEED, "utf8");
  const header = JSON.parse(seed.slice(0, seed.indexOf("\n")));
  const oldSession = header.id;
  const scenariosRun = Math.max(1, only.length);
  for (let i = 0; i < runs * scenariosRun; i++) {
    const bot = randomUUID();
    const session = randomUUID();
    const folder = path.join(home, "bots", bot, "files");
    fs.mkdirSync(folder, { recursive: true });
    const now = new Date().toISOString().replace(/\.\d+Z$/, "Z");
    fs.writeFileSync(path.join(home, "bots", bot, "bot.json"), JSON.stringify({ version: 1, id: bot, name: header.bot.name, avatar: { kind: "mono", hue: "slate" }, default: false, pinned: false, muted: false, created_at: now, session_id: session }));
    const day = path.join(home, "sessions", "2026", "11", "05");
    fs.mkdirSync(day, { recursive: true });
    const text = seed.replaceAll(oldSession, session).replaceAll("__FILES__", folder).replaceAll("__USER__", path.join(home, "user")).replaceAll("__BOT__", bot);
    fs.writeFileSync(path.join(day, `20261105T000000-${session}.jsonl`), text);
    seeded.push(bot);
  }
}
const server = spawn(her, ["serve", "--stdio"], { env: { ...process.env, HER_HOME: home, HOME: path.join(home, "user") }, stdio: ["pipe", "pipe", "inherit"] });
let next = 1;
const waiting = new Map();
const listeners = new Set();
readline.createInterface({ input: server.stdout }).on("line", (line) => {
  const msg = JSON.parse(line);
  if (msg.id !== undefined && waiting.has(msg.id)) {
    const w = waiting.get(msg.id);
    waiting.delete(msg.id);
    msg.error ? w.reject(new Error(`${msg.error.message}`)) : w.resolve(msg.result);
  } else if (msg.method) for (const f of listeners) f(msg);
});
const call = (method, params = {}) =>
  new Promise((resolve, reject) => {
    const id = next++;
    waiting.set(id, { resolve, reject });
    server.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
  });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const median = (xs) => (xs.length ? [...xs].sort((a, b) => a - b)[xs.length >> 1] : null);

// A call that names the person's real home is refused: a seeded conversation may remember paths.
const realHome = os.homedir();
const allow = (e) => (JSON.stringify(e ?? "").includes(realHome) ? { type: "deny", feedback: "test run: stay in your own folder" } : { type: "approve" });
const deny = () => ({ type: "deny", feedback: "这个文件名不行，先别建" });

/** Everything `session` says, stamped on arrival; what it asks is answered with `decide`. */
function watch(session, decide = allow) {
  const w = { events: [], lastEvent: Date.now(), lastIdle: 0 };
  const answer = (op) => void call("session/op", { session_id: session, op });
  const on = (msg) => {
    if (msg.method !== "session/event" || msg.params.session_id !== session) return;
    const e = { ...msg.params.event, at: Date.now() };
    w.events.push(e);
    w.lastEvent = e.at;
    if (e.type === "idle") w.lastIdle = e.at;
    if (e.type === "approval_requested") {
      console.log(`   (asked: ${e.summary})`);
      answer({ type: "approval", call_id: e.call_id, decision: decide(e) });
    }
    if (e.type === "agent_event" && e.event.type === "approval_requested") {
      console.log(`   (a sub-agent asked: ${e.event.summary})`);
      answer({ type: "agent_approval", agent: e.agent_id, call_id: e.event.call_id, decision: decide(e.event) });
    }
  };
  listeners.add(on);
  w.close = () => listeners.delete(on);
  return w;
}

/** Quiet since an idle that came after `since`. An idle sent before a message can cross it on the
 * wire, so it has to stay the last thing said for a moment. */
async function settled(w, since, ms, done) {
  const end = Date.now() + ms;
  for (;;) {
    if (done?.(w)) return true;
    if (w.lastIdle > since && w.lastIdle === w.lastEvent && Date.now() - w.lastIdle > 1500) return true;
    if (Date.now() > end) return false;
    await sleep(200);
  }
}

/** The tool a call really runs, deferred or not. */
const toolOf = (e) => (e.name === "call_tool" ? e.args?.name : e.name);
const isSend = (e) => e.type === "tool_started" && e.name === "send_to_user";
/** A sub-agent's own message to the owner (agent-messaging-design §17). */
const isUpdate = (e) => e.type === "message_completed" && e.mail?.kind === "to_user";
const updateText = (e) => e.message.content.map((c) => c.text ?? "").join("").replace(/<\/?sent_to_user[^>]*>/g, "").trim();
/** What a bubble says, whichever agent sent it. */
const bubbleText = (e) => (isUpdate(e) ? updateText(e) : String(e.args?.text ?? ""));
const isBubble = (e) => isSend(e) || isUpdate(e);
/** An update with no news: it opens on what the agent is about to do or is doing, and holds no figure. */
const isFiller = (text) => /^\s*(我先|我来|我开始|我这就|我正在|我已经开始|先|开始|正在|接下来|现在开始|已经开始|长诗已经动笔|let me|i'm starting|i am starting|starting|first,? i)/i.test(text) && !/\d{2,}|缺失|发现|问题/.test(text);
const isCall = (e, name) => e.type === "tool_started" && toolOf(e) === name;
const firstAfter = (w, at, test) => w.events.find((e) => e.at >= at && test(e));
const long = "写一篇一万字的小作文，主题随便，写好整理成文件告诉我。";
const atWork = (w) => w.events.some((e) => e.type === "agent_started");
/** Long enough past the first agent for the others to have started, on a slow model too. */
const agentsSettled = (w) => w.events.some((e) => e.type === "agent_started" && Date.now() - e.at > 45_000);

/** `say`: [ms after the one before, text], or [ms, text, when]: `when` is awaited first.
 * `oneByOne`: each after the one before is answered.
 * `until`: ends the run early, the work still going. `check`: counts of its own. */
function dailySales() {
  const rows = ["date,region,revenue"];
  for (let d = new Date(Date.UTC(2026, 0, 1)); d.getUTCFullYear() === 2026; d.setUTCDate(d.getUTCDate() + 1)) {
    if (d.getUTCMonth() === 2) continue;
    for (const region of ["北区", "南区", "东区", "西区"]) rows.push(`${d.toISOString().slice(0, 10)},${region},${1000 + ((d.getUTCDate() * 37 + region.charCodeAt(0)) % 900)}`);
  }
  return rows.join("\n") + "\n";
}
/** A small table with a story to find, so an agent is done in a few minutes: the east grows, the
 * north dips in spring, the west drops after a price change in September. */
function monthlySales() {
  const series = {
    北区: [812, 798, 640, 655, 701, 790, 806, 821, 815, 830, 844, 902],
    南区: [655, 640, 662, 671, 668, 690, 702, 695, 710, 722, 731, 788],
    东区: [520, 548, 575, 610, 642, 668, 705, 741, 776, 812, 851, 913],
    西区: [701, 688, 712, 705, 721, 718, 730, 726, 612, 581, 575, 590],
  };
  const rows = ["month,north,south,east,west"];
  for (let m = 0; m < 12; m++) rows.push(`2026-${String(m + 1).padStart(2, "0")},${Object.values(series).map((s) => s[m]).join(",")}`);
  return rows.join("\n") + "\n";
}
// Which way a request goes. What needs research, data, files or long writing goes to an agent
// however it is worded; what takes one step does not. No need to wait for the work: the decision is
// made once an agent starts, or once the bot has run this many steps itself.
const complexAsks = ["民主党当选，为何港股大涨？", "中期选举之后还有什么选举流程，什么时候真正大选，之间应该还有变数吧", "香港的恒生科技股为什么持续下跌？", "帮我对比一下腾讯和阿里最近一年的财报，做成一份 Excel 发我。", "写一份两千字的港股科技行业分析报告，存成文件发我。", "消费板块最近有什么利好？帮我梳理一下。"];
const mediumAsks = ["帮我写个 Python 脚本，造一份 200 行、5 列的随机数据存成 data.csv，算每列的平均值，把结果告诉我。", "把 1 到 1000 里的质数算出来存成 primes.txt，发给我。"];
const simpleAsks = ["1 加 1 等于几？", "今天星期几？", "你好，你是谁？用一句话回答。", "谢谢！", "1 公里等于多少英里？", "你能帮我做什么？简单说。"];
const lookupAsks = ["你文件夹里现在有哪些文件？", "现在几点了？", "腾讯现在股价多少？"];
const isOwnStep = (e) => e.type === "message_completed" && e.message.role === "assistant" && e.message.content.some((x) => x.type === "tool_call" && !["send_to_user", "spawn_agent"].includes(x.name));
const routed = (w) => w.events.filter(isOwnStep).length >= 5 || w.events.some((e) => e.type === "agent_started" && Date.now() - e.at > 3000);
const route = (asks) => ({ ms: 300_000, route: true, say: (run) => [[0, asks[run % asks.length]]], until: routed });
const scenarios = {
  complex: route(complexAsks),
  medium: route(mediumAsks),
  simple: route(simpleAsks),
  lookup: route(lookupAsks),
  // One request of your own, HER_BENCH_ASK.
  ask: route([process.env.HER_BENCH_ASK ?? "你好"]),
  poems: { say: () => ["写一首春天的诗", "写一首夏天的诗", "写一首秋天的诗", "写一首冬天的诗"].map((t) => [400, t]) },
  // One at a time, each answered before the next: a short answer is where a bot forgets to send.
  quick: { oneByOne: true, say: () => ["用一句话介绍你自己。", "今天星期几？", "1 加 1 等于几？"].map((t) => [0, t]) },
  hello: { oneByOne: true, say: () => ["你好，你是谁？用一句话回答。", "你能帮我做什么？简单说。", "谢谢！"].map((t) => [0, t]) },
  // The message types (bot-design §3.12): is a file sent as one, and a choice asked as one?
  files: { say: () => [[0, "写一份 100 字的周报，存成 report.md，再用 Python 把 1 到 5 的平方写进 squares.csv，都发给我。"]] },
  // A question, not a request for a file: the owner gets a conclusion, and the long version the bot
  // chose to write is the file. Is it handed over, or only named at the end of the words?
  report: {
    ms: 600_000,
    files: process.env.HER_BENCH_FILES
      ? Object.fromEntries(fs.readdirSync(process.env.HER_BENCH_FILES).map((f) => [f, fs.readFileSync(path.join(process.env.HER_BENCH_FILES, f), "utf8")]))
      : { "regions.csv": monthlySales() },
    say: () => [[0, process.env.HER_BENCH_ASK ?? "我文件夹里的 regions.csv 是公司今年四个区的月度收入，今年情况怎么样？帮我详细分析一下，要写得全面一点。"]],
    check(t, w, at, bot) {
      const before = new Set(Object.keys(scenarios.report.files));
      const folder = path.join(home, "bots", bot.id, "files");
      // With HER_BENCH_FILES the report is already written: a seeded conversation cut just before
      // it is sent, the moment this measures.
      const made = process.env.HER_BENCH_FILES ? [...before] : fs.readdirSync(folder, { recursive: true, withFileTypes: true }).filter((f) => f.isFile()).map((f) => f.name).filter((f) => !before.has(f));
      const failed = new Set(w.events.filter((e) => e.type === "tool_completed" && e.result.is_error).map((e) => e.call_id));
      const sends = w.events.filter((e) => isSend(e) && !failed.has(e.call_id));
      const inFiles = sends.flatMap((e) => (e.args.files ?? []).map((f) => path.basename(f.path)));
      const words = sends.map((e) => String(e.args.text ?? "")).join("\n");
      const handed = made.some((f) => inFiles.includes(f));
      const named = made.some((f) => words.includes(f));
      if (!made.length) t.noFile++;
      else if (handed) t.handed++;
      else if (named) t.pointed++;
      else t.kept++;
      if (handed && named) t.alsoLinked++;
      console.log(`   (made: ${made.join(", ") || "none"}; in files: ${inFiles.join(", ") || "none"})`);
    },
  },
  // The answers can be listed: a question with options fits.
  choice: { say: () => [[0, "我有两份周报草稿：A 版详细，B 版简洁。你不知道我要哪版，先问我一下，我选了你再往下写。"]] },
  // The answers cannot all be listed (a name, a date): words may fit better.
  open: { say: () => [[0, "帮我写一封请假邮件。收件人和请几天你都不知道，也猜不到，要问我的话一次只问一个。"]] },
  // One short question per bot: runs do not lean on each other, which suits comparing prompts.
  one: { say: (run) => [[0, ["用一句话介绍你自己。", "今天星期几？", "1 加 1 等于几？", "你好，你是谁？用一句话回答。", "你能帮我做什么？简单说。", "谢谢！"][run % 6]]] },
  mixed: {
    say: () =>
      [
        "帮我写个 Python 脚本，统计你文件夹里新建的 data.csv 每列的平均值：先造一份 200 行、5 列的随机数据，跑完把结果告诉我",
        "1 公里等于多少英里？",
        "再列一下 macOS 上常见的三种自动更新方案，各一句话",
      ].map((t) => [400, t]),
  },
  // How soon the first bubble comes when the work is long; no need to wait for the essay.
  // A moment past the handing over, to see what else it says.
  ack: { ms: 60_000, say: () => [[0, long]], until: (w) => w.events.some((e) => e.type === "agent_started" && Date.now() - e.at > 8000) },
  // The timeline of bot-turn-design §3.
  // Who gets what (bot-turn-design §7.2): alike but separate, unrelated, one job in parts, a
  // correction. No need to wait for the writing.
  fan: { ms: 240_000, say: () => ["春", "夏", "秋", "冬"].map((s) => [400, `写一首一万字的诗，关于${s}天`]), until: agentsSettled },
  apart: { ms: 240_000, say: () => [[0, long], [400, "再调研一下 macOS 上常见的三种自动更新方案，写一份两千字的比较"]], until: agentsSettled },
  whole: { ms: 240_000, say: () => [[0, "写一篇一万字的小作文，分成春夏秋冬四章，写好整理成文件告诉我。"]], until: agentsSettled },
  follow: {
    ms: 240_000,
    say: () => [[0, long], [3000, "写短一点，三千字就够。", atWork]],
    until: (w, at) => at.length === 2 && Date.now() - at[1] > 45_000,
    check(t, w, at) {
      t.redirected += firstAfter(w, at[1], (e) => e.type === "tool_started" && e.name === "send_message") ? 1 : 0;
    },
  },
  // When to speak up (agent-messaging-design §13). Ten separate pieces: ten agents, each with its
  // report, and no messages on the way.
  ten: {
    ms: 600_000,
    say: () => [[0, `查一下这十家公司的主营业务和最新一年的营收，每查完一家就告诉我：${companies.join("、")}。`]],
    check(t, w) {
      const said = w.events.filter(isBubble).map(bubbleText).join("\n");
      t.covered += companies.every((c) => said.includes(c)) ? 1 : 0;
    },
  },
  // Steps that can't be split, with one the owner wants to hear about.
  milestone: {
    ms: 300_000,
    say: () => [[0, "请交给子智能体去做：先在你的文件夹里造一份 data.csv（1000 行随机数据），备份到 backup/，备份好了先告诉我一声，再把它转成 JSON 存到 migrated/，全部做完再告诉我。"]],
    check(t, w) {
      const task = w.events.filter((e) => isCall(e, "spawn_agent")).map((e) => String(e.args?.task ?? "")).join("\n");
      t.inTask += /send_message|告诉|通知|tell|notify/i.test(task) ? 1 : 0;
      t.early += beforeLastReport(w, /备份(已经|已)?(好|完|成)|backup.*(done|complete)/i, (e) => e.at > (w.events.find((x) => x.type === "agent_started")?.at ?? Infinity)) ? 1 : 0;
    },
  },
  // Long work with nothing asked along the way.
  quiet: { ms: 600_000, say: () => [[0, long.replace("一万字", "两千字")]] },
  // Work in stages, with nothing asked along the way and nothing wrong: what the owner hears while
  // it runs is what updates are for.
  stages: {
    ms: 600_000,
    files: { "sales.csv": dailySales() },
    say: () => [[0, "请交给子智能体去做：把我文件夹里的 sales.csv 先核对数据质量，再按月和按地区汇总，用 Python 画两张图，最后写一份一千字的分析报告，都存成文件。"]],
  },
  // A problem only working through the data shows, the case the plan is for (§3.1): the file has a
  // row per day and looks fine at a glance, but March is not in it. The owner should not wait for
  // the whole job to learn of it.
  blocker: {
    ms: 420_000,
    files: { "sales.csv": dailySales() },
    say: () => [[0, "请交给子智能体去做：把我文件夹里的 sales.csv 按月汇总，做成图表和一份两千字的分析报告，存成文件。"]],
    check(t, w) {
      t.early += beforeLastReport(w, /3 ?月|三月|march/i, (e) => e.at > (w.events.find((x) => x.type === "agent_started")?.at ?? Infinity)) ? 1 : 0;
    },
  },
  // One agent needs what the other finds.
  peer: { ms: 300_000, say: () => [[0, "开两个子智能体同时做：一个写 api.py，放一个加法函数和一个减法函数，函数名由它定；另一个写 client.py 调用这两个函数。第一个定好函数名要告诉第二个。"]] },
  // Two thousand characters: the timeline is the same as for ten thousand, at a fraction of the cost.
  long: { ms: 600_000, say: () => [[0, long.replace("一万字", "两千字")], [2000, "刚才给你发啥了你知道么", atWork], [3000, "为什么你在写的时候我还能给你发消息"]] },
  stop: {
    ms: 180_000,
    say: (run) => [[0, long], [5000, ["算了不用了", "这个先别弄了", "Never mind, drop it."][run % 3], atWork]],
    check(t, w, at) {
      const told = firstAfter(w, at[1], isSend);
      const stop = firstAfter(w, at[1], (e) => isCall(e, "stop_agents"));
      const interrupted = firstAfter(w, at[1], (e) => e.type === "agent_status" && e.status === "interrupted");
      t.stopFirst += stop && (!told || stop.at <= told.at) ? 1 : 0;
      t.stopped += interrupted ? 1 : 0;
      t.told += told ? 1 : 0;
      t.restarted += firstAfter(w, at[1], (e) => e.type === "tool_started" && (e.name === "spawn_agent" || (e.name === "send_message" && !e.args?.interrupt))) ? 1 : 0;
    },
  },
  progress: {
    ms: 180_000,
    say: () => [[0, long], [10_000, "写到哪了", atWork]],
    // Answered: there is no need to wait for the essay.
    until: (w, at) => at.length === 2 && !!firstAfter(w, at[1] + 1, (e) => isSend(e) && !isAck(e)),
    check(t, w, at) {
      t.checked += firstAfter(w, at[1], (e) => isCall(e, "list_agents")) ? 1 : 0;
    },
  },
  change: {
    say: () => [[0, "写一首春天的诗"], [1000, "算了写秋天的"]],
    check(t, w) {
      const sent = w.events.filter(isSend).map((e) => String(e.args?.text ?? ""));
      t.lastAutumn += /秋/.test(sent.at(-1) ?? "") ? 1 : 0;
      t.spring += sent.some((s) => /春/.test(s) && s.length > 20) ? 1 : 0;
    },
  },
  // What a sub-agent asks, in Ask Permissions (subagent-design §10): the result must not get lost.
  allow: { manual: true, say: () => [[0, "请交给子智能体去做：在你的文件夹里运行命令 touch real.txt，做完告诉我结果。"]] },
  deny: { manual: true, decide: deny, say: () => [[0, "请交给子智能体去做：在你的文件夹里运行命令 touch real.txt，做完告诉我结果。"]] },
  // The tools for checking and stopping are the sessions' too.
  session: {
    plain: true,
    ms: 240_000,
    say: () => [[0, `请交给子智能体去做：${long}`], [15_000, "进展如何？", atWork], [20_000, "算了，停下吧。"]],
    check(t, w, at) {
      t.checked += firstAfter(w, at[1], (e) => isCall(e, "list_agents")) ? 1 : 0;
      t.stopped += firstAfter(w, at[2], (e) => e.type === "agent_status" && e.status === "interrupted") ? 1 : 0;
      t.stopFirst += firstAfter(w, at[2], (e) => isCall(e, "stop_agents")) ? 1 : 0;
    },
  },
};
const companies = ["苹果", "微软", "谷歌", "亚马逊", "特斯拉", "英伟达", "阿里巴巴", "腾讯", "比亚迪", "台积电"];
/** A message that says this reached the owner before the last report came. */
function beforeLastReport(w, words, also = () => true) {
  const last = w.events.filter((e) => e.type === "message_completed" && e.mail?.kind === "report").at(-1);
  return w.events.some((e) => isBubble(e) && also(e) && (!last || e.at < last.at) && words.test(bubbleText(e)));
}
const isAck = (e) => /我看|我查|我去|稍等|看一下|查一下|let me|checking/i.test(String(e.args?.text ?? "")) && String(e.args?.text ?? "").length < 30;
const tools = /\b(send_to_user|spawn_agent|send_message|end_turn|list_agents|stop_agents|tool_search|call_tool|bash|read|write|edit|tool_call|call_[a-z0-9]+)\b/i;
const internal = /子智能体|sub-?agent|agent_report|agent_message|汇报/i;
const aside = /^\s*[（(][^（()）]*[）)]\s*$/;
// What each poem has to mention, in order. A poem often names no season, or names another in
// passing, so its images count too; a lost or merged poem leaves too few messages to get through.
const seasons = ["春|柳|桃|杏|燕|芽|草|绿|花|融|东风|风筝", "夏|蝉|荷|瓜|槐|暑|雷|午|烈日|树影", "秋|枫|雁|稻|桂|柿|菊|叶|梧桐|西风", "冬|雪|寒|霜|炉|梅|冰|朔|北风"].map((s) => new RegExp(s));

function logOf(session) {
  const walk = (dir) =>
    fs.readdirSync(dir, { withFileTypes: true }).flatMap((f) => (f.isDirectory() ? walk(path.join(dir, f.name)) : f.name.endsWith(".jsonl") ? [path.join(dir, f.name)] : []));
  const file = walk(path.join(home, "sessions")).find((f) => f.includes(session));
  return file ? fs.readFileSync(file, "utf8") : "";
}

async function oneRun(name, s, run, t) {
  let bot = null;
  let session;
  if (s.plain) {
    const cwd = path.join(home, "work", `${name}-${run}`);
    fs.mkdirSync(cwd, { recursive: true });
    session = (await call("session/new", { cwd })).session_id;
  } else {
    bot = seeded.length ? { id: seeded.pop() } : (await call("bots/create", { name: `测试${run + 1}`, avatar: { kind: "mono", hue: "slate" } })).bot;
    for (const [file, text] of Object.entries(s.files ?? {})) {
      const folder = path.join(home, "bots", bot.id, "files");
      fs.mkdirSync(folder, { recursive: true });
      fs.writeFileSync(path.join(folder, file), text);
    }
    const opened = await call("bots/open", { bot_id: bot.id, events: { exclude: ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"] } });
    session = opened.session_id;
    if (s.manual) await call("session/op", { session_id: session, op: { type: "set_permission_mode", mode: "manual" } });
  }
  const w = watch(session, s.decide);
  const limit = s.ms ?? 240_000;
  const at = [];
  let quiet = true;
  for (const [gap, text, when] of s.say(run)) {
    if (s.oneByOne && at.length) quiet &&= await settled(w, at.at(-1), limit);
    // Not if it finished without: a bot may do short work itself.
    const finished = () => w.lastIdle > at.at(-1) && Date.now() - w.lastIdle > 1500;
    for (const end = Date.now() + limit; when && !when(w) && !finished() && Date.now() < end; ) await sleep(100);
    if (gap) await sleep(gap);
    at.push(Date.now());
    await call("session/op", { session_id: session, op: { type: "prompt", input: [{ type: "text", text }] } });
  }
  quiet &&= await settled(w, at.at(-1), limit, s.until && ((w) => s.until(w, at)));
  const log = logOf(session);
  w.close();
  const { events } = w;

  t.runs++;
  t.idle += quiet ? 1 : 0;
  // What the kernel refused never reached the owner: only the rest count.
  const refused = new Map(events.filter((e) => e.type === "tool_completed" && e.result.tool_name === "send_to_user" && e.result.is_error).map((e) => [e.call_id, e.result.content.map((x) => x.text ?? "").join("")]));
  const sends = events.filter(isSend);
  for (const e of sends) {
    if (!refused.has(e.call_id)) continue;
    t.rejected++;
    console.log(`   (rejected: ${refused.get(e.call_id).slice(0, 110)} | args: ${JSON.stringify(e.args).slice(0, 170)})`);
  }
  const calls = sends.filter((e) => !refused.has(e.call_id));
  const hasFiles = (e) => e.args.type === "attachment" || (!e.args.type && e.args.files?.length > 0);
  const isQuestion = (e) => e.args.type === "question" || (!e.args.type && e.args.options?.length > 0);
  const sent = calls.map((e) => (hasFiles(e) ? `[files${e.args.type ? "" : ", no type"}] ${(e.args.files ?? []).map((f) => f.path.split("/").at(-1)).join(", ")}${e.args.text ? ` | ${e.args.text}` : ""}` : isQuestion(e) ? `[question] ${e.args.text} / ${(e.args.options ?? []).map((o) => o.label).join(" / ")}` : String(e.args.text ?? "")));
  if (calls.some(hasFiles)) t.attached++;
  else if (calls.some((e) => /\]\([^)h][^)]*\)|\.(md|csv|png|pdf)\b/.test(String(e.args.text ?? "")))) t.linked++;
  if (calls.some(isQuestion)) t.asked++;
  else if (calls.some((e) => /[?？]\s*$/.test(String(e.args.text ?? "")))) t.prose++;
  t.sends += sent.length;
  t.leaked += sent.filter((x) => tools.test(x)).length;
  t.asides += sent.filter((x) => aside.test(x)).length;
  t.internal += sent.filter((x) => internal.test(x)).length;
  t.unanswered += (log.match(/"type":"unanswered"/g) ?? []).length;
  t.undelivered += (log.match(/"type":"undelivered"/g) ?? []).length;

  // Each task the owner started (a sub-agent's report needs no reply first): did its first reply
  // start with a send, and did it send at all?
  let inTask = false, first = null, sentInTask = false, replies = [];
  for (const e of s.plain ? [] : events) {
    if (e.type === "task_started") (inTask = e.trigger !== "mail"), (first = null), (sentInTask = false), (replies = []);
    if (inTask && e.type === "message_completed" && e.message.role === "assistant") {
      replies.push(e.message.content.map((x) => (x.type === "tool_call" ? `[${x.name}]` : x.type === "text" ? x.text : "")).join(" "));
      if (first === null) {
        first = e.message.content.find((x) => x.type === "tool_call")?.name ?? "(none)";
        t.tasks++;
        if (first === "send_to_user") t.first++;
      }
    }
    if (inTask && isSend(e)) sentInTask = true;
    if (inTask && (e.type === "task_completed" || e.type === "task_failed")) {
      inTask = false;
      if (e.type === "task_failed" && e.error?.kind !== "no_reply") {
        t.failed++;
        console.log(`   (failed: ${e.error?.kind}: ${String(e.error?.message).slice(0, 120)})`);
      } else if (!sentInTask) {
        t.missed++;
        // What it wrote instead, reply by reply.
        for (const r of replies) console.log(`   (missed: ${r.replace(/\s+/g, " ").slice(0, 120)})`);
      } else if (first === "(none)") t.late++;
    }
  }

  const delegated = events.some((e) => e.type === "agent_started");
  t.delegated += delegated ? 1 : 0;
  const started = events.filter((e) => e.type === "agent_started").length;
  t.agents[started] = (t.agents[started] ?? 0) + 1;
  // Reports: did anything reach the owner after the last one?
  const reports = events.filter((e) => e.type === "message_completed" && e.mail?.kind === "report");
  if (reports.length && !s.plain) {
    t.reported++;
    if (firstAfter(w, reports.at(-1).at, isSend)) t.delivered++;
    else console.log("   (nothing sent after the last report)");
  }

  // What agents said on the way, and what it cost.
  const notes = events.filter((e) => e.type === "message_completed" && e.mail?.kind === "message");
  t.notes += notes.length;
  for (const e of notes) console.log(`   (from ${e.mail.from.name}: ${e.message.content.map((c) => c.text ?? "").join("").replace(/<\/?agent_message[^>]*>/g, "").replace(/\s+/g, " ").trim().slice(0, 100)})`);
  if (notes.length) t.answered += events.filter((e) => e.at > notes[0].at && e.type === "tool_started" && e.name === "send_message").length;
  events.forEach((e, i) => {
    if (e.type !== "task_started" || e.trigger !== "mail") return;
    const mail = events.slice(i + 1).find((x) => x.type === "message_completed" && x.mail);
    if (mail?.mail.kind === "message") t.wokenByNote++;
  });
  t.peerNotes += events.filter((e) => e.type === "agent_event" && e.event.type === "message_completed" && e.event.mail?.kind === "message").length;

  // What agents told the owner themselves, what it cost, and how long the owner went without a word.
  const updates = events.filter(isUpdate);
  t.updates += updates.length;
  t.updateRuns += updates.length ? 1 : 0;
  for (const e of updates) {
    const text = updateText(e);
    console.log(`   (update from ${e.mail.from.name}: ${text.replace(/\s+/g, " ").slice(0, 400)})`);
    t.updateChars.push(text.length);
    if (isFiller(text)) t.updateFiller++;
    t.updateLeaks += tools.test(text) ? 1 : 0;
    t.updateInternal += internal.test(text) ? 1 : 0;
    t.updateOtherLanguage += /[\u4e00-\u9fff]/.test(text) ? 0 : 1;
  }
  const firstAgent = events.find((e) => e.type === "agent_started");
  if (updates.length && firstAgent) t.firstUpdateMs.push(updates[0].at - firstAgent.at);
  t.botSteps += events.filter((e) => e.type === "message_completed" && e.message.role === "assistant").length;
  if (firstAgent) {
    const lastStatus = events.filter((e) => e.type === "agent_status" && ["completed", "failed", "interrupted"].includes(e.status)).at(-1);
    // Up to the end of the work, or of the run when it was still going.
    const end = lastStatus?.at ?? events.at(-1).at;
    const moments = [firstAgent.at, ...events.filter(isBubble).map((e) => e.at).filter((at) => at > firstAgent.at && at < end), end];
    t.gapMs.push(Math.max(0, ...moments.slice(1).map((at, i) => at - moments[i])));
    t.workMs.push(end - firstAgent.at);
  }
  // The bot saying again what the owner has: most of an update's words in one of its later messages.
  for (const e of updates) {
    const words = updateText(e).replace(/[\s，。、,.!！:：;；]/g, "");
    const later = calls.filter((c) => c.at > e.at).map((c) => String(c.args.text ?? "").replace(/[\s，。、,.!！:：;；]/g, ""));
    if (words.length >= 12 && later.some((x) => x.includes(words.slice(0, 12)))) t.repeated++;
  }

  // How long the owner waits for a bubble, and whether the work went on meanwhile.
  at.forEach((time, i) => {
    const bubble = firstAfter(w, time, isSend);
    if (!bubble) return;
    (i === 0 ? t.firstMs : t.nextMs).push(bubble.at - time);
    if (i === 0) return;
    const started = events.find((e) => e.type === "agent_started");
    const working = (moment) =>
      started && started.at < moment && !events.some((e) => e.type === "agent_status" && e.at < moment && ["completed", "failed", "interrupted"].includes(e.status));
    if (working(time)) {
      t.askedDuring++;
      if (working(bubble.at)) t.during++;
    }
  });

  if (name === "poems") {
    let next = 0;
    for (const poem of sent) if (next < 4 && seasons[next].test(poem)) next++;
    if (next === 4) t.ordered++;
    else console.log(`   (order: got through ${next} of the 4 poems)`);
  }
  s.check?.(t, w, at, bot);
  // The first decision: what the first reply called, and how long it thought before.
  const opening = events.find((e) => e.type === "message_completed" && e.message.role === "assistant");
  const openingCalls = opening ? opening.message.content.filter((x) => x.type === "tool_call").map((x) => x.name) : [];
  if (openingCalls.length === 1 && openingCalls[0] === "send_to_user") t.alone++;
  if (opening) t.thinking.push(opening.message.content.filter((x) => x.type === "thinking" || x.type === "reasoning").reduce((n, x) => n + (x.thinking ?? x.text ?? "").length, 0));
  const spawned = events.find((e) => e.type === "agent_started");
  const own = events.filter((e) => isOwnStep(e) && (!spawned || e.at < spawned.at)).length;
  t.ownRounds.push(own);
  if (s.route) {
    const bubble = firstAfter(w, at[0], isSend);
    console.log(`   [${s.say(run)[0][1].slice(0, 24)}] first=${openingCalls.join("+") || "text"} own=${own} ${spawned ? "agent" : "no agent"} bubble=${bubble ? ((bubble.at - at[0]) / 1000).toFixed(1) + "s" : "none"}`);
  }
  console.log(`${name} #${run + 1}: ${sent.length} messages${quiet ? "" : " (timed out)"}${delegated ? ", delegated" : ""}`);
  for (const x of sent) console.log("   ·", x.replace(/\s+/g, " ").slice(0, 90));
  if (bot && !process.env.HER_BENCH_KEEP) await call("bots/delete", { bot_id: bot.id });
}

const totals = {};
await call("initialize", { protocol_version: 2, client_info: { name: "bot-turns" } });
await call("bots/subscribe", {});
if (process.env.HER_BENCH_APP) await call("browser/host");
for (const [name, s] of Object.entries(scenarios)) {
  if (only.length && !only.includes(name)) continue;
  const t = (totals[name] = {
    runs: 0, idle: 0, tasks: 0, first: 0, missed: 0, failed: 0, late: 0, unanswered: 0, undelivered: 0, sends: 0, leaked: 0, asides: 0, internal: 0,
    delegated: 0, agents: {}, redirected: 0, reported: 0, delivered: 0, firstMs: [], nextMs: [], askedDuring: 0, during: 0,
    ordered: 0, attached: 0, linked: 0, handed: 0, pointed: 0, kept: 0, alsoLinked: 0, noFile: 0, asked: 0, prose: 0, rejected: 0, stopFirst: 0, stopped: 0, told: 0, restarted: 0, checked: 0, lastAutumn: 0, spring: 0,
    notes: 0, wokenByNote: 0, answered: 0, peerNotes: 0, covered: 0, early: 0, inTask: 0, alone: 0, thinking: [], ownRounds: [],
    updates: 0, updateRuns: 0, updateFiller: 0, updateLeaks: 0, updateInternal: 0, updateOtherLanguage: 0, firstUpdateMs: [], gapMs: [], workMs: [], updateChars: [], botSteps: 0, repeated: 0,
  });
  for (let run = 0; run < runs; run += parallel) {
    const batch = Array.from({ length: Math.min(parallel, runs - run) }, (_, k) => oneRun(name, s, run + k, t).catch((e) => console.log(`${name} #${run + k + 1}: ${e.message}`)));
    await Promise.all(batch);
  }
  for (const key of ["firstMs", "nextMs", "thinking", "ownRounds", "firstUpdateMs", "gapMs", "workMs", "updateChars"]) t[key] = { median: median(t[key]), worst: t[key].length ? Math.max(...t[key]) : null };
}
console.log("\n" + JSON.stringify(totals, null, 1));
server.kill();
if (process.env.HER_BENCH_KEEP) console.log(`home kept: ${home}`);
else fs.rmSync(home, { recursive: true, force: true });
process.exit(0);
