import type { TaskScript } from "./script.ts";
import type { Avatar, AvatarHue, AvatarShape, BotSummary, LastMessage, RoutineSummary, SessionState, SessionSummary, TranscriptItem, Usage } from "@hermesh/her-protocol";

// A heavy owner for the mock (frontend stress tests): many bots, many
// sessions, long conversations, and a stream of list changes. Everything is
// made from a seed, so two runs show the same lists; conversations are made
// when first opened, as her reads a session's file only then.

export interface StressOptions {
  /** Bots in Bot mode's list. */
  bots: number;
  /** Sessions in the session list. */
  sessions: number;
  /** Turns of each conversation, made when it is first opened (default 60). */
  turns?: number;
  /** Messages of each conversation instead (user, assistant and tool results), in whole turns: at least this many. */
  messages?: number;
  /** Project folders the sessions are spread over (default one for each 25 sessions, at least 20). */
  projects?: number;
  /** List changes pushed per second, bots and sessions together (default 0: none). */
  churn?: number;
  seed?: number;
}

/** `bots=1200,sessions=1500,turns=60,churn=30`: the form an environment variable or a URL gives. */
export function parseStress(text: string | null | undefined): StressOptions | undefined {
  if (!text) return undefined;
  const o: Record<string, number> = {};
  for (const part of text.split(/[,&]/)) {
    const [k, v] = part.split(/[=:]/);
    if (k && v && Number.isFinite(Number(v))) o[k.trim()] = Number(v);
  }
  return { bots: o.bots ?? 1000, sessions: o.sessions ?? 1000, turns: o.turns, messages: o.messages, projects: o.projects, churn: o.churn, seed: o.seed };
}

/** A seeded generator (mulberry32). */
export function rng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export type Rand = () => number;
const pick = <T>(r: Rand, list: readonly T[]): T => list[Math.floor(r() * list.length)]!;
const int = (r: Rand, lo: number, hi: number) => lo + Math.floor(r() * (hi - lo + 1));
const iso = (ms: number) => new Date(ms).toISOString();
const day = 86_400_000;

const hues: AvatarHue[] = ["slate", "sage", "clay", "ochre", "plum", "teal", "rose", "stone"];
const shapes: AvatarShape[] = ["tall", "dot", "smile", "wink", "lid", "bar", "side", "odd"];
const roles = ["研究", "日报", "读书", "周报", "翻译", "客服", "运维", "招聘", "法务", "采购", "竞品", "舆情", "健身", "数据", "代码", "测试", "设计", "文案", "会议", "差旅"];
const owners = ["小助手", "分析师", "秘书", "巡检员", "观察员", "管家", "专员", "顾问", "记录员", "整理员"];
const projects = [
  "hermesh", "calc-lib", "web-console", "billing", "infra", "ml-pipeline", "mobile-app", "docs-site", "data-lake", "search",
  "auth", "gateway", "payments", "crm", "analytics", "notebook", "scheduler", "etl", "cli", "sdk-js",
];
const topics = [
  "修复登录页在 Safari 下的白屏", "给支付回调加幂等校验", "把日志从 console 迁到结构化输出", "重写缓存失效策略", "补齐 API 文档的错误码",
  "排查内存泄漏", "升级 React 到 19", "数据库慢查询优化", "CI 缓存命中率太低", "拆分单体服务的用户模块", "图表组件改成虚拟滚动",
  "导出 Excel 时中文乱码", "接入新的模型供应商", "统一时间格式", "首页首屏性能", "重构权限模型", "补测试覆盖率到 80%", "实现断点续传",
  "迁移到 pnpm workspace", "清理废弃的 feature flag",
];
const botLines = [
  "今天的数据已经整理好了，主要变化有三点：", "收到，我先看一下再回复你。", "已完成，结果放在附件里。", "**提醒**：明天上午 10 点有评审会。",
  "我查了一下，最近一周没有新的变化。", "有两个地方需要你确认：\n1. 预算上限\n2. 截止时间", "这个问题我建议分两步处理。", "失败了，原因是对方接口超时，稍后我会重试。",
  "- 收回问卷 312 份\n- 满意度 4.3 分，比上次高 0.2\n- 最多的意见：签到等太久", "好的，已经记下了。", "> 引用：上次你说要优先处理客户 A 的需求。\n\n已经安排上了。",
  "报告写好了，[点这里查看](https://example.com/report)。", "`npm run build` 通过了，没有新的警告。",
];
const userLines = ["帮我看看今天的情况", "继续", "把结果整理成表格", "好的，谢谢", "这个再查一下", "发我一份周报", "为什么失败了？", "明天提醒我", "改成英文版", "列一下要点"];

const usage: Usage = { input: 1200, output: 400, cache_read: 8000, cache_write: 0, cost: { currency: "CNY", input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } };

/** The bots of a heavy owner: a few pinned, most with a last message, some unread, some with routines. */
export function stressBots(o: StressOptions, now: number): BotSummary[] {
  const r = rng(o.seed ?? 7);
  const out: BotSummary[] = [];
  for (let i = 0; i < o.bots; i++) {
    const created = now - int(r, 1, 365) * day;
    const lastAt = Math.min(now - 1000, created + r() * (now - created));
    const avatar: Avatar = r() < 0.5 ? { kind: "char", hue: pick(r, hues), shape: pick(r, shapes) } : { kind: "mono", hue: pick(r, hues) };
    const from = r() < 0.7 ? "bot" : "user";
    const last: LastMessage | undefined = r() < 0.92 ? { from, text: (from === "bot" ? pick(r, botLines) : pick(r, userLines)).repeat(r() < 0.1 ? 8 : 1).slice(0, 1000), ts: iso(lastAt), images: r() < 0.05 ? 2 : undefined } : undefined;
    const routines: RoutineSummary[] | undefined =
      r() < 0.15
        ? Array.from({ length: int(r, 1, 3) }, (_, k) => ({ id: `r_s${i}_${k}`, name: `${pick(r, roles)}巡检`, when: "每天 9:00", state: r() < 0.2 ? "paused" : "active", next_run: iso(now + int(r, 1, 24) * 3_600_000) }))
        : undefined;
    out.push({
      id: `stress-bot-${i}`,
      name: `${pick(r, roles)}${pick(r, owners)}${i}`.slice(0, 20),
      avatar,
      created_at: iso(created),
      pinned: i < 8 || undefined,
      last,
      unread: r() < 0.2 ? int(r, 1, 30) : undefined,
      routines,
      muted: r() < 0.05 || undefined,
    });
  }
  return out;
}

/** The sessions of a heavy owner: across many projects and three months, a few running or waiting, some archived. */
export function stressSessions(o: StressOptions, now: number): SessionSummary[] {
  const r = rng((o.seed ?? 7) * 31 + 1);
  const folders = Array.from({ length: o.projects ?? Math.max(20, Math.floor(o.sessions / 25)) }, (_, k) => `~/CodeSpace/${projects[k % projects.length]}${k >= projects.length ? `-${Math.floor(k / projects.length)}` : ""}`);
  const out: SessionSummary[] = [];
  for (let i = 0; i < o.sessions; i++) {
    const updated = now - Math.floor(Math.pow(r(), 2) * 90 * day);
    const created = updated - int(r, 1, 600) * 60_000;
    const x = r();
    const state: SessionState = x < 0.01 ? "awaiting_approval" : x < 0.03 ? "running" : x < 0.05 ? "failed" : "idle";
    const noProject = r() < 0.1;
    const topic = pick(r, topics);
    out.push({
      session_id: `stress-session-${i}`,
      cwd: noProject ? `~/.her/chats/stress-${i}` : pick(r, folders),
      no_project: noProject || undefined,
      created_at: iso(created),
      updated_at: iso(updated),
      loaded: false,
      busy: state === "running",
      attached_clients: 0,
      state,
      task_started_at: state === "running" || state === "awaiting_approval" ? iso(now - int(r, 5, 900) * 1000) : undefined,
      model: r() < 0.6 ? "deepseek/deepseek-flash" : "deepseek/deepseek-pro",
      title: r() < 0.7 ? `${topic}${r() < 0.3 ? `（第 ${int(r, 2, 9)} 轮）` : ""}` : undefined,
      preview: `${topic}，需要先看一下现状再动手。`.slice(0, 120),
      archived: r() < 0.08,
    });
  }
  return out;
}

const fence = "```";

/**
 * A long coding conversation: each turn a question, thinking, tool calls with their results, and a Markdown answer.
 * `messages`, when given, takes the place of `turns`: whole turns until there are at least that many messages.
 */
export function stressTranscript(seed: number, turns: number, start: number, messages?: number): TranscriptItem[] {
  const r = rng(seed);
  const items: TranscriptItem[] = [];
  let ts = start;
  let n = 0;
  let said = 0;
  const id = () => `m${seed}_${n++}`;
  for (let t = 0; messages ? said < messages : t < turns; t++) {
    const before = items.length;
    const topic = pick(r, topics);
    items.push({ type: "message", message_id: id(), message: { role: "user", content: [{ type: "text", text: `${topic}。${pick(r, userLines)}` }], ts: (ts += 20_000) } });
    const calls = int(r, 1, 3);
    const callIds = Array.from({ length: calls }, () => `call_${seed}_${n++}`);
    items.push({
      type: "message",
      message_id: id(),
      message: {
        role: "assistant", api: "openai-completions", provider: "deepseek", model: "deepseek-flash", stop_reason: "tool_use", usage, ts: (ts += 3000),
        content: [
          { type: "thinking", text: `先读一下相关文件，确认${topic}涉及哪些模块。`.repeat(int(r, 1, 4)) },
          ...callIds.map((cid, k) => ({ type: "tool_call" as const, id: cid, name: k % 2 ? "read" : "bash", arguments: k % 2 ? { path: `src/module${k}.ts` } : { command: `rg -n "${topic.slice(0, 4)}" src | head -50` } })),
        ],
      },
    });
    for (const [k, cid] of callIds.entries()) {
      const out = Array.from({ length: int(r, 5, 60) }, (_, l) => `src/module${k}.ts:${l + 1}:  const value${l} = compute(${l}, options);`).join("\n");
      items.push({ type: "tool_result", call_id: cid, result: { call_id: cid, tool_name: k % 2 ? "read" : "bash", content: [{ type: "text", text: out }], is_error: r() < 0.05, ts: (ts += 400), duration_ms: 120 } });
    }
    const answer = [
      `## ${topic}`,
      "",
      "改动如下：",
      "",
      ...Array.from({ length: int(r, 2, 6) }, (_, l) => `${l + 1}. 调整了 \`module${l}\` 的**边界条件**，避免空值时报错`),
      "",
      `${fence}ts`,
      ...Array.from({ length: int(r, 4, 30) }, (_, l) => `export function step${l}(input: number): number {\n  return input * ${l + 1};\n}`),
      fence,
      "",
      r() < 0.4 ? "| 指标 | 改前 | 改后 |\n| --- | --- | --- |\n| 耗时 | 320ms | 120ms |\n| 内存 | 80MB | 52MB |\n" : "",
      "测试全部通过。",
    ].join("\n");
    items.push({ type: "message", message_id: id(), message: { role: "assistant", api: "openai-completions", provider: "deepseek", model: "deepseek-flash", stop_reason: "stop", usage, ts: (ts += 5000), content: [{ type: "text", text: answer }] } });
    said += items.length - before;
    items.push({ type: "task_end", task_id: `t${seed}_${t}`, outcome: "completed", reason: "done", ts, usage: { per_model: { "deepseek/deepseek-flash": usage } } });
  }
  return items;
}

/** What a stress test's session answers: a long Markdown reply, written out over about 20 seconds, nothing to approve. */
export function stressReply(prompt: string): TaskScript {
  const rows = Array.from({ length: 8 }, (_, i) => `| 模块 ${i + 1} | ${120 + i * 7}ms | ${80 - i * 3}ms |`).join("\n");
  const code = Array.from({ length: 12 }, (_, i) => `export function step${i}(input: number): number {\n  return input * ${i + 1};\n}`).join("\n");
  return {
    turns: [
      { think: `用户说「${prompt.slice(0, 40)}」。先看相关模块，再给出改动和数据。`.repeat(3), tools: [{ name: "bash", args: { command: "rg -n cache src | head" }, output: "src/cache.ts:12: export const cache = new Map();", ms: 300 }] },
      {
        say: [
          "## 分析结果", "", "改动分三步：", "", "1. 先把缓存的**失效时间**统一到一处", "2. 再给慢查询加索引", "3. 最后补测试", "",
          "```ts", code, "```", "", "| 模块 | 改前 | 改后 |", "| --- | --- | --- |", rows, "",
          "- 内存占用下降约 30%\n- 首屏时间下降约 40%\n- 测试全部通过", "", "> 注意：上线前要在预发环境跑一次完整回归。", "", "以上。",
        ].join("\n"),
      },
    ],
  };
}

/** A bot's long conversation: the owner asks, the bot answers through send_to_user; `messages` as for sessions. */
export function stressBotTranscript(seed: number, turns: number, start: number, messages?: number): TranscriptItem[] {
  const r = rng(seed);
  const items: TranscriptItem[] = [];
  let ts = start;
  let n = 0;
  const id = () => `b${seed}_${n++}`;
  let said = 0;
  for (let t = 0; messages ? said < messages : t < turns; t++) {
    const before = items.length;
    items.push({ type: "message", message_id: id(), message: { role: "user", content: [{ type: "text", text: pick(r, userLines) }], ts: (ts += 60_000) } });
    const sends = int(r, 1, 3);
    for (let k = 0; k < sends; k++) {
      const cid = `send_${seed}_${n++}`;
      const text = Array.from({ length: int(r, 1, 4) }, () => pick(r, botLines)).join("\n\n");
      const end = k === sends - 1;
      items.push({
        type: "message",
        message_id: id(),
        message: { role: "assistant", api: "openai-completions", provider: "deepseek", model: "deepseek-flash", stop_reason: "tool_use", usage, ts: (ts += 4000), content: [{ type: "tool_call", id: cid, name: "send_to_user", arguments: end ? { text, end_turn: true } : { text } }] },
      });
      items.push({ type: "tool_result", call_id: cid, result: { call_id: cid, tool_name: "send_to_user", content: [{ type: "text", text: "Sent." }], is_error: false, ts: (ts += 50), duration_ms: 5 } });
    }
    said += items.length - before;
    items.push({ type: "task_end", task_id: `bt${seed}_${t}`, outcome: "completed", reason: "done", ts, usage: { per_model: { "deepseek/deepseek-flash": usage } } });
  }
  return items;
}

/** One list change of the churn: a bot starts or stops answering, says something, or a session moves. */
export function churnBot(r: Rand, b: BotSummary, now: number): BotSummary {
  const x = r();
  if (x < 0.4) return { ...b, typing: !b.typing };
  return { ...b, typing: false, last: { from: "bot", text: pick(r, botLines), ts: iso(now) }, unread: (b.unread ?? 0) + 1 };
}

export function churnSession(r: Rand, s: SessionSummary, now: number): SessionSummary {
  const running = s.state !== "running";
  return { ...s, state: running ? "running" : "idle", busy: running, task_started_at: running ? iso(now) : undefined, updated_at: iso(now) };
}
