import type { TaskEndItem } from "@hermesh/her-protocol";
import { saidTo, startedSession, type StartedSession } from "./botChat.ts";
import { type AgentView, type SessionView } from "./session.ts";
import { conversation, taskGists, type AgentLaunch, type Step, type ToolView } from "./steps.ts";
import { plain } from "./bots.ts";
import { totals } from "./text.ts";

// The work a bot sent out, as the Progress page shows it (docs/her-team.md §15): under each thing the owner
// said to it, the sub-agents it started for that and the coding sessions it started on the mesh. Worked out
// from the conversation the page already follows, sub-agents' own sessions included (their agent_events), so
// the page asks her for nothing more.

/** How a sub-agent stands, as its card says it. waiting: it asks the owner to approve a call. */
export type AgentState = "starting" | "running" | "waiting" | "done" | "failed" | "stopped";

export type ProgressTask =
  /** A sub-agent the bot started: its spawn call, and the agent as it is now. */
  | { kind: "agent"; key: string; launch: AgentLaunch }
  /** A coding session the bot started through the mesh; how it stands is the app's to say, from the sessions it holds. */
  | { kind: "session"; key: string; started: StartedSession };

/** What set a group's work off. earlier: the calls that started these agents are on a page of the history not read yet. */
export type ProgressParent = { kind: "user"; text: string; ts: number } | { kind: "routine"; name: string; ts: number } | { kind: "earlier" };

export interface ProgressGroup {
  key: string;
  parent: ProgressParent;
  /** What the bot last told the owner since then. */
  said?: string;
  tasks: ProgressTask[];
}

/** Each view's steps, worked out once: a view is replaced, never changed, when its session moves. */
const steps = new WeakMap<SessionView, Step[]>();

/** A session's conversation as steps (steps.ts), kept for as long as the view is. */
export function stepsOf(v: SessionView): Step[] {
  let s = steps.get(v);
  if (!s) steps.set(v, (s = conversation(v)));
  return s;
}

/** The groups, oldest first; only what sent work out is one. */
export function progressGroups(v: SessionView): ProgressGroup[] {
  const out: ProgressGroup[] = [];
  let current: ProgressGroup | undefined;
  const launched = new Set<string>();
  // Work with nothing before it in what the view holds belongs to a message on an earlier page.
  const group = () => (current ??= { key: "earlier", parent: { kind: "earlier" }, tasks: [] });
  const begin = (g: ProgressGroup) => {
    if (current?.tasks.length) out.push(current);
    current = g;
  };
  for (const s of stepsOf(v)) {
    switch (s.kind) {
      case "user":
        begin({ key: s.key, parent: { kind: "user", text: s.text, ts: s.ts }, tasks: [] });
        break;
      case "report":
        // A routine's run is the bot's to act on, as the owner's message is.
        if (s.routine) begin({ key: s.key, parent: { kind: "routine", name: s.name, ts: s.ts }, tasks: [] });
        break;
      case "agents":
        for (const l of s.launches) {
          launched.add(l.call_id);
          group().tasks.push({ kind: "agent", key: l.call_id, launch: l });
        }
        break;
      case "work":
        for (const item of s.items) {
          const started = startedSession(item);
          if (started) group().tasks.push({ kind: "session", key: item.key, started });
          const said = saidTo(item);
          if (said !== null && current) current.said = said;
        }
        break;
    }
  }
  if (current?.tasks.length) out.push(current);
  // Agents whose spawn calls the view does not hold: started before its first item.
  const orphans = v.agents.filter((a) => !launched.has(a.call_id));
  if (orphans.length) {
    const tasks = orphans.map((a): ProgressTask => ({ kind: "agent", key: a.call_id, launch: { call_id: a.call_id, name: a.name, description: a.description, task: "", agent: a, starting: false } }));
    const first = out[0]?.parent.kind === "earlier" ? out.shift() : undefined;
    out.unshift({ key: "earlier", parent: { kind: "earlier" }, tasks: [...tasks, ...(first?.tasks ?? [])] });
  }
  return out;
}

/** A sub-agent's card, by the task's key (its spawn call) or the agent's id. */
export function findAgentTask(groups: ProgressGroup[], id: string): { group: ProgressGroup; task: Extract<ProgressTask, { kind: "agent" }> } | undefined {
  for (const group of groups) {
    for (const task of group.tasks) if (task.kind === "agent" && (task.key === id || task.launch.agent?.id === id)) return { group, task };
  }
  return undefined;
}

export function agentState(l: AgentLaunch): AgentState {
  const a = l.agent;
  if (!a) return "starting";
  switch (a.status) {
    case "running":
      return a.view.live?.approvals.length ? "waiting" : "running";
    case "completed":
      return "done";
    case "failed":
      return "failed";
    default:
      return "stopped";
  }
}

/** What each of a group's sub-agents is called: what the bot called the work, else a line of its task that tells it from the others, else its handle. */
export function agentTitles(tasks: ProgressTask[]): Map<string, string> {
  const agents = tasks.flatMap((t) => (t.kind === "agent" ? [t.launch] : []));
  const gists = taskGists(agents.map((l) => l.task));
  return new Map(agents.map((l, i) => [l.call_id, l.description || gists[i] || l.name]));
}

const firstLine = (text: string) => plain(text.split("\n").map((l) => l.trim()).find((l) => l !== "") ?? "");

/**
 * A card's one line: what the agent does now (its latest call, else its latest words or thought), what it
 * asks, how it ended (the start of its report), or why it failed. Null when there is nothing to say yet.
 */
export function agentLine(l: AgentLaunch): { kind: "tool"; tool: ToolView } | { kind: "text"; text: string } | null {
  const a = l.agent;
  if (!a) return null;
  const text = (t: string | undefined) => (t && firstLine(t) !== "" ? { kind: "text" as const, text: firstLine(t) } : null);
  const state = agentState(l);
  if (state === "waiting") return text(a.view.live?.approvals[0]?.summary);
  if (state === "failed") return text(a.error?.message ?? lastEnd(a)?.error?.message);
  const all = stepsOf(a.view);
  if (state === "done") return text(a.summary) ?? text(all.findLast((s) => s.kind === "text")?.text);
  if (state !== "running") return null;
  const last = all.at(-1);
  if (last?.kind === "text") return text(last.text);
  if (last?.kind !== "work") return null;
  const item = last.items.at(-1);
  return item?.kind === "tool" ? { kind: "tool", tool: item.tool } : text(item?.text);
}

function lastEnd(a: AgentView): TaskEndItem | undefined {
  return a.view.entries.findLast((e): e is TaskEndItem => e.type === "task_end");
}

/** The tokens an agent used: its ended tasks', and its running task's turns so far. */
export function agentTokens(a: AgentView): number {
  let n = a.view.live ? totals(a.view.live.usage).tokens : 0;
  for (const e of a.view.entries) if (e.type === "task_end" && e.usage) n += totals(e.usage).tokens;
  return n;
}

/**
 * How long an agent has worked, in ms: a running one, since its round began; an ended one, its last round, as
 * her timed it, else from its first message to its last task's end (an agent of an earlier run of her).
 */
export function agentElapsed(a: AgentView, now: number): number {
  if (a.status === "running") return Math.max(0, now - a.since);
  if (a.elapsed_ms !== undefined) return a.elapsed_ms;
  const first = a.view.entries.find((e) => e.type === "message");
  const end = lastEnd(a);
  return first?.type === "message" && end ? Math.max(0, end.ts - first.message.ts) : 0;
}

/** How many calls an agent made: its record's length, as the page counts it. */
export function agentCalls(a: AgentView): number {
  return stepsOf(a.view).reduce((n, s) => n + (s.kind === "work" ? s.items.filter((i) => i.kind === "tool").length : 0), 0);
}
