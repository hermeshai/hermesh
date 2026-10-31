import { clone, create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampDate, timestampFromMs } from "@bufbuild/protobuf/wkt";
import type { ConnectRouter } from "@connectrpc/connect";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import { LocalService, QueryWorkResponseSchema, WatchResponseSchema, WatchSessionsResponseSchema, type WatchResponse, type WatchSessionsResponse } from "@hermesh/proto/local";
import { SessionCursorSchema, SessionEventSchema, SessionSchema, type Session, type SessionCursor, type SessionEvent } from "@hermesh/proto/session";
import fs from "node:fs";
import http from "node:http";
import { ids, smallConversation, world, type World } from "./world";

// A stand-in for hermeshd on a Unix socket, serving the benchmark's world
// (world.ts) with the calls the main window makes (Watch above all). While live, it changes
// sessions as a busy owner's machines do: a running session every 100 ms
// (hermeshd pushes at most that often), and the long running conversation
// grows: for 5 s by an event every 250 ms, then for 5 s by a reply written
// as drafts every 100 ms (docs/reply-streaming.md), stored when it is done.

export type Daemon = { socket: string; world: World; live: (on: boolean) => void; close: () => Promise<void> };

const tsMs = (s: Session) => (s.updatedAt ? timestampDate(s.updatedAt).getTime() : 0);
const newestFirst = (a: Session, b: Session) => tsMs(b) - tsMs(a) || (a.sessionId < b.sessionId ? 1 : -1);

/** Queue-backed stream: push() feeds the generator until the call ends. */
function channel<T>(signal: AbortSignal) {
  const queue: T[] = [];
  let wake: (() => void) | undefined;
  return {
    push(v: T) {
      queue.push(v);
      wake?.();
    },
    async *drain(): AsyncGenerator<T> {
      while (!signal.aborted) {
        while (queue.length) yield queue.shift()!;
        await new Promise<void>((r) => {
          wake = r;
          signal.addEventListener("abort", () => r(), { once: true });
        });
        wake = undefined;
      }
    },
  };
}

export async function startDaemon(socket: string): Promise<Daemon> {
  const w = world();
  const watchers = new Set<(m: WatchSessionsResponse) => void>();
  const screens = new Set<(m: WatchResponse) => void>();
  const followers = new Map<string, Set<(e: SessionEvent) => void>>();
  const events = (id: string) => {
    let evs = w.events.get(id);
    if (!evs) w.events.set(id, (evs = smallConversation(w.sessions.get(id)!)));
    return evs;
  };

  const page = (limit: number, before?: SessionCursor) => {
    const all = [...w.sessions.values()].sort(newestFirst);
    const from = before ? all.findIndex((s) => newestFirst(s, cursorSession(before)) > 0) : 0;
    const list = from < 0 ? [] : all.slice(from, from + (limit || 50));
    const last = list.at(-1);
    const more = from >= 0 && from + list.length < all.length;
    return { sessions: list, unreachable: [], next: more && last ? create(SessionCursorSchema, { updatedAt: last.updatedAt, sessionId: last.sessionId }) : undefined };
  };

  const routes = (router: ConnectRouter) =>
    router.service(LocalService, {
      getStatus: () => w.status,
      listMachines: () => ({ machines: w.machines }),
      listClients: () => ({ clients: w.clients }),
      listProjects: () => ({ projects: w.projects, unreachable: [] }),
      listDevices: () => ({ devices: w.devices }),
      listPairingRequests: () => ({ pairings: [] }),
      listSessions: (req) => page(req.limit, req.before),
      async *watch(_req, ctx) {
        const ch = channel<WatchResponse>(ctx.signal);
        screens.add(ch.push);
        ctx.signal.addEventListener("abort", () => screens.delete(ch.push));
        const first = page(50);
        yield create(WatchResponseSchema, {
          reset: true, status: w.status, machines: w.machines, clients: w.clients, projects: w.projects, devices: w.devices, sessions: first.sessions, next: first.next,
        });
        yield* ch.drain();
      },
      async *watchSessions(_req, ctx) {
        const ch = channel<WatchSessionsResponse>(ctx.signal);
        watchers.add(ch.push);
        ctx.signal.addEventListener("abort", () => watchers.delete(ch.push));
        yield create(WatchSessionsResponseSchema, { machinesChanged: true, unreachable: [] });
        yield* ch.drain();
      },
      async *streamSessionEvents(req, ctx) {
        const evs = events(req.sessionId);
        const ch = channel<SessionEvent>(ctx.signal);
        if (req.follow) {
          const set = followers.get(req.sessionId) ?? new Set();
          followers.set(req.sessionId, set);
          set.add(ch.push);
          ctx.signal.addEventListener("abort", () => set.delete(ch.push));
        }
        for (const e of evs) if (e.seq > req.afterSeq) yield e;
        if (req.follow) yield* ch.drain();
      },
      queryWork: (req) => {
        // By day and agent from the first day asked, as hermeshd counts (usageRequest).
        const [y, m, d] = req.firstDay.split("-").map(Number);
        const first = new Date(y, m - 1, d).getTime();
        const days = w.usage
          .map((x) => {
            const [a, b, c] = x.date.split("-").map(Number);
            return { ...x, bucket: Math.round((new Date(a, b - 1, c).getTime() - first) / 86_400_000) };
          })
          .filter((x) => x.bucket >= 0 && x.bucket < req.days);
        return create(QueryWorkResponseSchema, {
          turns: days.map((x) => ({ bucket: x.bucket, group: { agent: x.agent }, turns: x.sessions, inputTokens: x.inputTokens, outputTokens: x.outputTokens })),
          sessions: days.map((x) => ({ bucket: x.bucket, group: { agent: x.agent }, sessions: x.sessions })),
        });
      },
    });

  fs.rmSync(socket, { force: true });
  const server = http.createServer(connectNodeAdapter({ routes }));
  await new Promise<void>((r) => server.listen(socket, r));

  // Live changes.
  const running = [...w.sessions.values()].filter((s) => s.progress?.turnStartedAt && s.sessionId !== ids.bigLive).map((s) => s.sessionId);
  let timers: NodeJS.Timeout[] = [];
  let turn = 0;
  let step = 0;
  let draft = "";
  let pieces = 0;
  const writing = () => step % 40 >= 20;
  const touch = (id: string, seq?: bigint) => {
    const next = clone(SessionSchema, w.sessions.get(id)!);
    next.updatedAt = timestampFromMs(Date.now());
    next.progress!.lastEventSeq = seq ?? next.progress!.lastEventSeq + 1n;
    next.progress!.currentTool = `step ${step}`;
    w.sessions.set(id, next);
    return next;
  };
  const dirty = new Set<string>();
  const live = (on: boolean) => {
    timers.forEach(clearInterval);
    timers = [];
    if (!on) return;
    timers.push(
      setInterval(() => {
        dirty.add(running[turn++ % running.length]);
        const changed = [...dirty].map((id) => touch(id, id === ids.bigLive ? events(id).at(-1)!.seq : undefined));
        dirty.clear();
        const m = create(WatchSessionsResponseSchema, { sessions: changed });
        watchers.forEach((f) => f(m));
        const sm = create(WatchResponseSchema, { sessions: changed });
        screens.forEach((f) => f(sm));
      }, 100),
      setInterval(() => {
        if (!writing()) return;
        const piece = [`检查 \`sessions.ts\` 的第 ${step} 处，`, "**这里** 要改成按块渲染，", "| 文件 | 改动 |\n|---|---|\n| a.ts | 新增 |\n\n", "然后继续。\n\n"][pieces++ % 4];
        draft += piece;
        const e = create(SessionEventSchema, { kind: { case: "agentDraft", value: { text: piece } } });
        followers.get(ids.bigLive)?.forEach((f) => f(e));
      }, 100),
      setInterval(() => {
        const evs = events(ids.bigLive);
        const seq = evs.at(-1)!.seq + 1n;
        step++;
        if (writing()) return;
        const callId = `live-${step >> 1}`;
        const written = draft;
        draft = "";
        const kind: MessageInitShape<typeof SessionEventSchema>["kind"] =
          written
            ? { case: "agentMessage", value: { text: written } }
            : step % 2
              ? { case: "toolCall", value: { callId, kind: "read", name: "Read", summary: `Read packages/core/src/live${step}.ts` } }
              : step % 20 === 0
                ? { case: "agentMessage", value: { text: `进度：已经处理了 **${step}** 个文件，继续检查 \`sessions.ts\`。` } }
                : { case: "toolResult", value: { callId: `live-${(step - 1) >> 1}`, summary: `ok\n`.repeat(3) } };
        const e = create(SessionEventSchema, { seq, ts: timestampFromMs(Date.now()), turnId: "t", kind });
        evs.push(e);
        followers.get(ids.bigLive)?.forEach((f) => f(e));
        dirty.add(ids.bigLive);
      }, 250),
    );
  };

  return {
    socket,
    world: w,
    live,
    close: () => {
      live(false);
      server.closeAllConnections();
      return new Promise((r) => server.close(() => r()));
    },
  };
}

function cursorSession(c: SessionCursor): Session {
  return create(SessionSchema, { sessionId: c.sessionId, updatedAt: c.updatedAt });
}
