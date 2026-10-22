import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import {
  ClientSchema, DeviceSchema, GetStatusResponseSchema, MachineSchema, ProjectSchema,
  type Client, type Device, type GetStatusResponse, type Machine, type Project,
} from "@hermesh/proto/local";
import { AgentStatusSchema, MachineStatusSchema } from "@hermesh/proto/machine";
import {
  FailureKind, SessionEventSchema, SessionSchema, SessionStatus, UsageDaySchema,
  type Session, type SessionEvent, type UsageDay,
} from "@hermesh/proto/session";

// A heavy owner's world, the same on every run: a few machines and grants,
// a few hundred sessions over two months (a dozen live), a year of usage,
// and two long conversations. The benchmark's daemon (daemon.ts) serves it.

export const sizes = { sessions: 400, running: 10, waiting: 2, failed: 3, archived: 40, bigTurns: 40, callsPerTurn: 30 };

/** Ids of the sessions the benchmark opens. */
export const ids = { bigIdle: "s-big-idle", bigLive: "s-big-live", smallA: "s-0100", smallB: "s-0101" };
export const titles = { bigIdle: "重构会话同步层（长）", bigLive: "迁移构建系统（进行中）", smallA: "会话 100", smallB: "会话 101" };

/** Deterministic random numbers (mulberry32). */
export function rng(seed: number) {
  return () => {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const minute = 60_000;
const day = 86_400_000;
const ts = (ms: number) => timestampFromMs(Math.round(ms));

export type World = {
  status: GetStatusResponse;
  machines: Machine[];
  clients: Client[];
  projects: Project[];
  devices: Device[];
  sessions: Map<string, Session>;
  events: Map<string, SessionEvent[]>;
  usage: UsageDay[];
};

export function world(now = Date.now()): World {
  const r = rng(42);
  const pick = <T,>(xs: readonly T[]) => xs[Math.floor(r() * xs.length)];
  const agents = ["claude", "codex", "gemini"];
  const agentStatus = agents.map((name) => create(AgentStatusSchema, { name, available: true, loggedIn: true, version: "1.0.0" }));
  const machines = [
    { machineId: "m-local", machineName: "MacBook Pro", local: true, online: true, platform: "darwin/arm64" },
    { machineId: "m-build", machineName: "build-server", local: false, online: true, platform: "linux/amd64" },
    { machineId: "m-gpu", machineName: "gpu-box", local: false, online: true, platform: "linux/amd64" },
    { machineId: "m-mini", machineName: "Mac mini", local: false, online: false, platform: "darwin/arm64" },
  ].map((m) =>
    create(MachineSchema, {
      ...m,
      version: "0.9.0",
      lastSeen: ts(now),
      status: create(MachineStatusSchema, { name: m.machineName, platform: m.platform, agents: agentStatus, maxSessions: 8, version: "0.9.0" }),
    }),
  );
  const labels = ["Claude", "ChatGPT", "OpenClaw", "Muse", "夜间值班", "CI 机器人"];
  const clients = labels.map((label, i) =>
    create(ClientSchema, {
      clientId: `c-${i}`,
      grantId: `g-${i}`,
      label,
      fingerprint: `fp${i}`,
      online: i < 3,
      expiresAt: ts(now + (i === 4 ? 3 : 60) * day),
      spec: { label, machineIds: ["*"], projectIds: ["*"], agents },
    }),
  );
  const projectNames = ["hermesh", "web-console", "relay", "docs", "infra", "ml-pipeline", "mobile", "billing", "design-system", "data-lake", "search", "auth"];
  const projects = projectNames.map((p, i) => create(ProjectSchema, { machineId: machines[i % 3].machineId, projectId: p, defaultBaseRef: "main", agents }));
  const devices = [
    create(DeviceSchema, { keyId: "d-1", label: "MacBook Pro", platform: "darwin", thisDevice: true, machineId: "m-local", machineOnline: true }),
    create(DeviceSchema, { keyId: "d-2", label: "iPhone", platform: "ios" }),
  ];
  const status = create(GetStatusResponseSchema, {
    initialized: true,
    ownerId: "owner-1",
    relayUrl: "https://relay.example",
    relayConnected: true,
    localMachineEnabled: true,
    localMachineId: "m-local",
    localMachineName: "MacBook Pro",
    version: "0.9.0",
    connectAddress: "relay.example/owner-1",
    deviceKeyId: "d-1",
  });

  const sessions = new Map<string, Session>();
  const events = new Map<string, SessionEvent[]>();
  const verbs = ["修复", "重构", "补测试：", "调查", "优化", "实现", "迁移", "清理"];
  const things = ["登录流程", "会话列表分页", "构建缓存", "WebSocket 重连", "用量统计", "配对码校验", "日志轮转", "通知去重", "暗色模式", "导出 CSV"];
  for (let i = 0; i < sizes.sessions; i++) {
    const id = `s-${String(i).padStart(4, "0")}`;
    const project = projects[i % projects.length];
    let status = SessionStatus.IDLE;
    let updated = now - (i / sizes.sessions) * 60 * day - r() * 3 * 3600_000;
    if (i < sizes.running) status = i % 5 === 4 ? SessionStatus.PREPARING : SessionStatus.RUNNING;
    else if (i < sizes.running + sizes.waiting) status = SessionStatus.WAITING_INPUT;
    else if (i < sizes.running + sizes.waiting + sizes.failed) status = i % 2 ? SessionStatus.CRASHED : SessionStatus.IDLE;
    else if (i >= sizes.sessions - sizes.archived) status = SessionStatus.ARCHIVED;
    const live = i < sizes.running + sizes.waiting;
    if (live) updated = now - r() * 5 * minute;
    const created = updated - (1 + r() * 30) * minute;
    const failed = i >= sizes.running + sizes.waiting && i < sizes.running + sizes.waiting + sizes.failed;
    const turns = 1 + Math.floor(r() * 6);
    sessions.set(
      id,
      create(SessionSchema, {
        sessionId: id,
        machineId: project.machineId,
        projectId: project.projectId,
        agent: pick(agents),
        status,
        createdBy: clients[i % clients.length].clientId,
        title: i === 100 ? titles.smallA : i === 101 ? titles.smallB : `${pick(verbs)}${pick(things)} #${i}`,
        branch: `hermesh/${id}`,
        createdAt: ts(created),
        updatedAt: ts(updated),
        progress: {
          turnCount: turns,
          phase: live ? "running tool" : "",
          lastEventSeq: BigInt(turns * 12),
          diffStat: { filesChanged: Math.floor(r() * 9), insertions: Math.floor(r() * 300), deletions: Math.floor(r() * 120) },
          usage: { inputTokens: BigInt(Math.floor(r() * 2e6)), outputTokens: BigInt(Math.floor(r() * 2e5)), costUsd: r() * 4 },
          turnStartedAt: live ? ts(now - r() * 20 * minute) : undefined,
          lastFailure: failed ? { kind: i % 2 ? FailureKind.AGENT_CRASHED : FailureKind.RATE_LIMIT, message: "agent process exited unexpectedly: exit status 1" } : undefined,
          pendingQuestion:
            status === SessionStatus.WAITING_INPUT
              ? {
                  questionId: `q-${id}`,
                  turn: turns + 1,
                  askedAt: ts(now - minute),
                  expiresAt: ts(now + 30 * minute),
                  items: [{ id: "i1", header: "颜色", text: "选哪个配色方案？", options: [{ label: "暖色" }, { label: "冷色" }], allowOther: true }],
                }
              : undefined,
        },
      }),
    );
  }
  const big = (id: string, title: string, running: boolean) => {
    const evs = conversation(rng(id.length * 7), sizes.bigTurns, sizes.callsPerTurn, now - 2 * day, running);
    const last = evs.at(-1)!;
    sessions.set(
      id,
      create(SessionSchema, {
        sessionId: id,
        machineId: "m-local",
        projectId: "hermesh",
        agent: "claude",
        status: running ? SessionStatus.RUNNING : SessionStatus.IDLE,
        createdBy: "c-0",
        title,
        createdAt: ts(now - 2 * day),
        updatedAt: ts(running ? now : now - 3 * minute),
        progress: {
          turnCount: running ? sizes.bigTurns - 1 : sizes.bigTurns,
          lastEventSeq: last.seq,
          diffStat: { filesChanged: 42, insertions: 1830, deletions: 911 },
          usage: { inputTokens: 18_000_000n, outputTokens: 1_200_000n, costUsd: 31.5 },
          turnStartedAt: running ? ts(now - 4 * minute) : undefined,
        },
      }),
    );
    events.set(id, evs);
  };
  for (const s of sessions.values()) {
    const evs = smallConversation(s);
    events.set(s.sessionId, evs);
    s.progress!.lastEventSeq = evs.at(-1)!.seq;
  }
  big(ids.bigIdle, titles.bigIdle, false);
  big(ids.bigLive, titles.bigLive, true);

  const usage: UsageDay[] = [];
  for (let d = 0; d < 371; d++) {
    const date = new Date(now - d * day);
    const key = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
    for (const agent of agents) {
      if (r() < 0.35) continue;
      usage.push(create(UsageDaySchema, { date: key, agent, inputTokens: BigInt(Math.floor(r() * 5e6)), outputTokens: BigInt(Math.floor(r() * 4e5)), sessions: 1 + Math.floor(r() * 9) }));
    }
  }
  return { status, machines, clients, projects, devices, sessions, events, usage };
}

const reply = (r: () => number, turn: number) =>
  [
    `第 ${turn} 轮：我先看了一下 **会话同步层** 的现状，问题出在 \`mergeSessions\` 对旧状态的处理上。`,
    "",
    "改动如下：",
    "",
    "1. 把游标比较改成先比时间再比 id",
    "2. 补上 `relistedCursor` 的测试",
    "3. 清理了重复的 `Map` 构建",
    "",
    "```ts",
    "export function merge(list: Session[], changes: Session[]) {",
    "  const at = new Map(list.map((s, i) => [s.sessionId, i]));",
    `  return changes.reduce((out, c) => upsert(out, at, c), [...list]); // ${Math.floor(r() * 1e6)}`,
    "}",
    "```",
    "",
    "> 注意：这会改变分页时的顺序，但只在两个会话同一毫秒更新时。",
    "",
    "详见 [设计文档](https://example.com/design)，跑过的测试都通过了。",
  ].join("\n");

/** A long conversation: turns of a message, many tool calls with output, a reply; the last turn still going when running. */
export function conversation(r: () => number, turns: number, calls: number, start: number, running: boolean): SessionEvent[] {
  const out: SessionEvent[] = [];
  let seq = 0n;
  let t = start;
  const add = (kind: MessageInitShape<typeof SessionEventSchema>["kind"]) => {
    t += 2000 + r() * 8000;
    out.push(create(SessionEventSchema, { seq: ++seq, ts: ts(t), turnId: "t", kind }));
  };
  const kinds = [
    { kind: "read", name: "Read", summary: () => `Read packages/core/src/file${Math.floor(r() * 90)}.ts` },
    { kind: "search", name: "Grep", summary: () => `Grep "mergeSessions" in packages/` },
    { kind: "edit", name: "Edit", summary: () => `Edit packages/core/src/sessions.ts` },
    { kind: "shell", name: "Bash", summary: () => `Bash pnpm --filter @hermesh/core test -- --run sessions` },
  ];
  for (let turn = 1; turn <= turns; turn++) {
    add({ case: "turnStarted", value: { turn } });
    add({ case: "userMessage", value: { text: `第 ${turn} 轮：继续把会话同步层的边界情况处理完，顺便把测试补齐。` } });
    add({ case: "agentMessage", value: { text: "好的，我先读一下相关代码。" } });
    const last = running && turn === turns;
    for (let c = 0; c < calls; c++) {
      const k = kinds[Math.floor(r() * kinds.length)];
      const callId = `call-${turn}-${c}`;
      add({ case: "toolCall", value: { callId, kind: k.kind, name: k.name, summary: k.summary() } });
      if (last && c === calls - 1) break;
      add({ case: "toolResult", value: { callId, summary: `ok ${Math.floor(r() * 1e6)}\n`.repeat(1 + Math.floor(r() * 8)), isError: r() < 0.03 } });
      if (c % 10 === 9) add({ case: "agentMessage", value: { text: `已经处理了 ${c + 1} 处，继续。` } });
    }
    if (last) break;
    add({ case: "agentMessage", value: { text: reply(r, turn) } });
    add({
      case: "turnCompleted",
      value: { turn, isError: false, usage: { inputTokens: 400_000n, outputTokens: 30_000n }, diffStat: { filesChanged: 3, insertions: 40, deletions: 12 } },
    });
  }
  return out;
}

/** Events of a session not built in: a short conversation made from its id. */
export function smallConversation(s: Session): SessionEvent[] {
  const n = Number(s.sessionId.slice(2)) || 1;
  return conversation(rng(n), Math.max(1, s.progress?.turnCount ?? 1), 6, Number(s.createdAt?.seconds ?? 0n) * 1000, false);
}
