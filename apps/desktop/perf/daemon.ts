import { clone, create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromMs } from "@bufbuild/protobuf/wkt";
import type { ConnectRouter } from "@connectrpc/connect";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import { RuntimeMode } from "@hermesh/proto/identity";
import { LocalService, QueryWorkResponseSchema, WatchResponseSchema, WatchSessionsResponseSchema, type WatchResponse, type WatchSessionsResponse } from "@hermesh/proto/local";
import {
  GetSessionResponseSchema, ListProvidersResponseSchema, ProviderInstanceSchema,
  SessionCursorSchema, SessionEventSchema, SessionSchema, SessionService, TurnItemKind, TurnItemSchema,
  type Session, type SessionCursor, type SessionEvent, type SessionProjection,
} from "@hermesh/proto/session";
import fs from "node:fs";
import http from "node:http";
import { ids, smallConversation, sizes, world, type World } from "./world";

// A stand-in for hermeshd on a Unix socket, serving the benchmark's world
// (world.ts) with the calls the main window makes (Watch + SessionService).

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
  const projection = (id: string): SessionProjection => {
    let p = w.projections.get(id);
    if (!p) w.projections.set(id, (p = smallConversation(w.sessions.get(id)!)));
    return p;
  };

  const page = (limit: number, before?: SessionCursor) => {
    const all = [...w.sessions.values()].sort(newestFirst);
    const from = before ? all.findIndex((s) => newestFirst(s, cursorSession(before)) > 0) : 0;
    const list = from < 0 ? [] : all.slice(from, from + (limit || 50));
    const last = list.at(-1);
    const more = from >= 0 && from + list.length < all.length;
    return { sessions: list, unreachable: [], next: more && last ? create(SessionCursorSchema, { updatedAt: last.updatedAt, sessionId: last.sessionId }) : undefined };
  };

  const providers = create(ListProvidersResponseSchema, {
    maxRuntimeMode: RuntimeMode.FULL_ACCESS,
    providers: [
      create(ProviderInstanceSchema, {
        instanceId: "claude",
        displayName: "Claude Code",
        driver: "claude",
        enabled: true,
        installed: true,
        authenticated: true,
        models: [{ slug: "claude-sonnet-4-6", name: "Claude Sonnet 4.6", shortName: "Sonnet 4.6", isDefault: true }],
      }),
    ],
  });

  const routes = (router: ConnectRouter) => {
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
      queryWork: (req) => {
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
    router.service(SessionService, {
      listProviders: () => providers,
      getSession: (req) => create(GetSessionResponseSchema, { projection: projection(req.sessionId) }),
      async *subscribeSession(req, ctx) {
        const ch = channel<SessionEvent>(ctx.signal);
        const set = followers.get(req.sessionId) ?? new Set();
        followers.set(req.sessionId, set);
        set.add(ch.push);
        ctx.signal.addEventListener("abort", () => set.delete(ch.push));
        yield* ch.drain();
      },
    });
    return router;
  };

  fs.rmSync(socket, { force: true });
  const server = http.createServer(connectNodeAdapter({ routes }));
  await new Promise<void>((r) => server.listen(socket, r));

  // Live changes.
  const running = [...w.sessions.values()].filter((s) => s.progress?.turnStartedAt && s.sessionId !== ids.bigLive).map((s) => s.sessionId);
  let timers: NodeJS.Timeout[] = [];
  let turn = 0;
  let step = 0;
  let draftId = "";
  let draftText = "";
  let pieces = 0;
  const writing = () => step % 40 >= 20;
  const touch = (id: string, seq?: bigint) => {
    const next = clone(SessionSchema, w.sessions.get(id)!);
    next.updatedAt = timestampFromMs(Date.now());
    next.progress!.lastEventSeq = seq ?? next.progress!.lastEventSeq + 1n;
    next.progress!.currentTool = `step ${step}`;
    w.sessions.set(id, next);
    const p = w.projections.get(id);
    if (p) p.session = next;
    return next;
  };
  const dirty = new Set<string>();
  const live = (on: boolean) => {
    timers.forEach(clearInterval);
    timers = [];
    if (!on) return;
    timers.push(
      setInterval(() => {
        dirty.add(running[turn++ % running.length]!);
        const changed = [...dirty].map((id) => touch(id, id === ids.bigLive ? projection(id).items.at(-1)!.seq : undefined));
        dirty.clear();
        const m = create(WatchSessionsResponseSchema, { sessions: changed });
        watchers.forEach((f) => f(m));
        const sm = create(WatchResponseSchema, { sessions: changed });
        screens.forEach((f) => f(sm));
      }, 100),
      setInterval(() => {
        if (!writing()) return;
        const proj = projection(ids.bigLive);
        const piece = [`检查 \`sessions.ts\` 的第 ${step} 处，`, "**这里** 要改成按块渲染：\n\n", "| 文件 | 改动 |\n|---|---|\n| a.ts | 新增 |\n\n", "然后继续。\n\n"][pieces++ % 4]!;
        if (!draftId) {
          const seq = (proj.items.at(-1)?.seq ?? 0n) + 1n;
          draftId = `i-live-draft-${seq}`;
          draftText = piece;
          const item = create(TurnItemSchema, {
            itemId: draftId,
            sessionId: ids.bigLive,
            runId: `r-${sizes.bigTurns}`,
            seq,
            kind: TurnItemKind.ASSISTANT_MESSAGE,
            text: draftText,
            streaming: true,
            status: "running",
            createdAt: timestampFromMs(Date.now()),
            updatedAt: timestampFromMs(Date.now()),
          });
          proj.items.push(item);
          const ev = create(SessionEventSchema, { seq, sessionId: ids.bigLive, type: "item.updated", item, occurredAt: timestampFromMs(Date.now()) });
          followers.get(ids.bigLive)?.forEach((f) => f(ev));
          return;
        }
        draftText += piece;
        const last = proj.items.find((x) => x.itemId === draftId);
        if (last) last.text = draftText;
        const seq = (proj.items.at(-1)?.seq ?? 0n) + 1n;
        const ev = create(SessionEventSchema, { seq, sessionId: ids.bigLive, type: "item.delta", targetItemId: draftId, textDelta: piece, occurredAt: timestampFromMs(Date.now()) });
        followers.get(ids.bigLive)?.forEach((f) => f(ev));
      }, 100),
      setInterval(() => {
        const proj = projection(ids.bigLive);
        step++;
        if (writing()) return;
        const seq = (proj.items.at(-1)?.seq ?? 0n) + 1n;
        if (draftId) {
          const last = proj.items.find((x) => x.itemId === draftId);
          draftId = "";
          draftText = "";
          if (last) {
            last.streaming = false;
            last.status = "completed";
            last.updatedAt = timestampFromMs(Date.now());
            const ev = create(SessionEventSchema, { seq, sessionId: ids.bigLive, type: "item.updated", item: last, occurredAt: timestampFromMs(Date.now()) });
            followers.get(ids.bigLive)?.forEach((f) => f(ev));
            dirty.add(ids.bigLive);
            return;
          }
        }
        const now = timestampFromMs(Date.now());
        const item = create(TurnItemSchema, {
          itemId: `i-live-${seq}`,
          sessionId: ids.bigLive,
          runId: `r-${sizes.bigTurns}`,
          seq,
          kind: step % 20 === 0 ? TurnItemKind.ASSISTANT_MESSAGE : TurnItemKind.TOOL_CALL,
          toolName: step % 20 === 0 ? "" : "Read",
          text: step % 20 === 0 ? `进度：已经处理了 **${step}** 个文件，继续检查 \`sessions.ts\`。` : `Read packages/core/src/live${step}.ts`,
          output: step % 20 === 0 ? "" : "ok\n".repeat(3),
          status: "completed",
          createdAt: now,
          updatedAt: now,
        });
        proj.items.push(item);
        const ev = create(SessionEventSchema, { seq, sessionId: ids.bigLive, type: "item.updated", item, occurredAt: now });
        followers.get(ids.bigLive)?.forEach((f) => f(ev));
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
