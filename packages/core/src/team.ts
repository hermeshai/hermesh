import { SessionStatus, type Question, type QuestionItem, type Session } from "@hermesh/proto/session";
import { createdDate, isActive, isFailed, needsAttention, updatedDate } from "./sessions";

// What the owner's agents are doing, as Her's name tag, the team panel and
// Work show it (docs/her-team.md §4.2, §4.3, §14.6): the sessions that wait
// for the owner, those that failed, those at work and those that are done.
// The same rules as HermeshKit's Team.swift, so both apps say the same.

/** What the name tag says beside Her's name, the first that holds. */
export type TeamTag =
  | { kind: "waiting"; agent: string } // one session waits for an answer: its coding agent
  | { kind: "needYou"; count: number } // several things wait, or a request to connect
  | { kind: "failed"; count: number }
  | { kind: "working"; count: number }
  | { kind: "quiet" };

export interface TeamState {
  /** Waiting for an answer, the latest first. */
  waiting: Session[];
  /** Requests to connect that wait. */
  requests: number;
  /** Stopped by a failure, the latest first. */
  failed: Session[];
  /** At work, the newest first. */
  running: Session[];
  /** Neither at work nor in need of the owner, the latest first. */
  finished: Session[];
  tag: TeamTag;
  /** The coding agents at work, each once, in the order of their sessions. */
  runningAgents: string[];
}

/** The team's state from the sessions the app holds (archived ones are no part of it) and the requests to connect. */
export function teamState(sessions: Session[], requests: number): TeamState {
  const visible = sessions.filter((s) => s.status !== SessionStatus.ARCHIVED);
  const latest = (list: Session[]) => list.sort((a, b) => updatedDate(b).getTime() - updatedDate(a).getTime());
  const waiting = latest(visible.filter((s) => s.status === SessionStatus.WAITING_INPUT));
  const failed = latest(visible.filter(isFailed));
  const running = visible.filter(isActive).sort((a, b) => createdDate(b).getTime() - createdDate(a).getTime());
  const finished = latest(visible.filter((s) => !isActive(s) && !needsAttention(s)));
  const tag: TeamTag =
    waiting.length === 1 && requests === 0 ? { kind: "waiting", agent: waiting[0].agent }
    : waiting.length + requests > 0 ? { kind: "needYou", count: waiting.length + requests }
    : failed.length > 0 ? { kind: "failed", count: failed.length }
    : running.length > 0 ? { kind: "working", count: running.length }
    : { kind: "quiet" };
  return { waiting, requests, failed, running, finished, tag, runningAgents: [...new Set(running.map((s) => s.agent))] };
}

/**
 * The sessions at hand by coding agent, as the sidebar's rail shows them (docs/her-team.md §16): those waiting for
 * the owner, then those that failed, then those at work, each kind in teamState's order; each agent once, in the
 * order of its first session, with its sessions in that order.
 */
export function liveByAgent(team: TeamState): { agent: string; sessions: Session[] }[] {
  const by = new Map<string, Session[]>();
  for (const s of [...team.waiting, ...team.failed, ...team.running]) {
    const list = by.get(s.agent);
    if (list) list.push(s);
    else by.set(s.agent, [s]);
  }
  return [...by].map(([agent, sessions]) => ({ agent, sessions }));
}

/**
 * The item one click can answer: a single, non-secret choice, with any other items optional. The item, not only
 * its options, so that an optional field before it cannot receive the answer.
 */
export function quickChoice(q: Question): QuestionItem | undefined {
  const items = q.items.length === 1 ? q.items : q.items.filter((i) => !i.optional);
  const item = items.length === 1 ? items[0] : undefined;
  return item && item.options.length > 0 && !item.multiSelect && !item.secret ? item : undefined;
}
