import type { TaskEndItem } from "@hermesh/her-protocol";
import { saidTo } from "./botChat.ts";
import { type AgentView, type SessionView } from "./session.ts";
import { conversation, taskGists, toolObject, type AgentLaunch, type Step, type ToolView, type WorkItem } from "./steps.ts";
import { plain } from "./bots.ts";
import { totals } from "./text.ts";

// The work a bot sent out, as the Progress page shows it (docs/her-team.md §15): for each thing the owner
// said to it, the sub-agents it started for that, each a post of its own with what set it off at its foot; and
// what a sub-agent's post says. Coding sessions it started on the mesh are not its progress: the sessions'
// own pages hold them (§15, 2026-11-16). Worked out
// from the conversation the page already follows, sub-agents' own sessions included (their agent_events), so
// the page asks her for nothing more.

/** How a sub-agent stands, as its card says it. waiting: it asks the owner to approve a call. */
export type AgentState = "starting" | "running" | "waiting" | "done" | "failed" | "stopped";

/** A sub-agent the bot started: its spawn call, and the agent as it is now. */
export type ProgressTask = { kind: "agent"; key: string; launch: AgentLaunch };

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
export function findAgentTask(groups: ProgressGroup[], id: string): { group: ProgressGroup; task: ProgressTask } | undefined {
  for (const group of groups) {
    for (const task of group.tasks) if (task.key === id || task.launch.agent?.id === id) return { group, task };
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
  const agents = tasks.map((t) => t.launch);
  const gists = taskGists(agents.map((l) => l.task));
  return new Map(agents.map((l, i) => [l.call_id, l.description || gists[i] || l.name]));
}

const firstLine = (text: string) => plain(text.split("\n").map((l) => l.trim()).find((l) => l !== "") ?? "");

/** What a sub-agent's post in the feed shows, from one pass over its record. */
export interface AgentPost {
  /** A line of how it goes: what it asks, why it failed, the start of its report, else its latest words or thought. */
  words: string | null;
  /** The call it is on while it works; null while it thinks. */
  now: ToolView | null;
  /** How many calls it made. */
  calls: number;
  /** The files it wrote, in the order it first wrote them. */
  files: string[];
}

export function agentPost(l: AgentLaunch): AgentPost {
  const post: AgentPost = { words: null, now: null, calls: 0, files: [] };
  const a = l.agent;
  if (!a) return post;
  const line = (t: string | undefined) => (t && firstLine(t) !== "" ? firstLine(t) : null);
  let latest: string | undefined, said: string | undefined, last: WorkItem | Step | undefined;
  for (const s of stepsOf(a.view)) {
    if (s.kind === "text") {
      latest = said = s.text;
      last = s;
    } else if (s.kind === "work") {
      for (const it of s.items) {
        last = it;
        if (it.kind === "thinking") {
          if (!it.redacted) latest = it.text;
          continue;
        }
        post.calls++;
        const object = toolObject(it.tool);
        if (it.tool.category === "edit" && it.tool.state === "done" && object && !post.files.includes(object)) post.files.push(object);
      }
    } else {
      last = s;
    }
  }
  const state = agentState(l);
  post.words =
    state === "waiting" ? line(a.view.live?.approvals[0]?.summary)
    : state === "failed" ? line(a.error?.message ?? lastEnd(a)?.error?.message)
    : state === "done" ? (line(a.summary) ?? line(said))
    : line(latest);
  if ((state === "running" || state === "waiting") && last?.kind === "tool" && ["streaming", "pending", "awaiting_approval", "running"].includes(last.tool.state)) post.now = last.tool;
  return post;
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
