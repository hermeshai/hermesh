import { fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";
import type { RuntimeMode } from "@hermesh/proto/identity";
import { ProjectSchema, type Project } from "@hermesh/proto/local";
import { ListProvidersResponseSchema, type ListProvidersResponse, type ModelSelection } from "@hermesh/proto/thread";

// What a new session used last, kept on this device (docs/thread-composer.md
// §4.1): the machine, and on each machine its project, model and permission
// mode. Never on the machine, and never a choice made for the owner.

export interface MachineChoice {
  projectId?: string;
  sel?: Pick<ModelSelection, "instanceId" | "model" | "options">;
  mode?: RuntimeMode;
}

export interface Remembered {
  machineId?: string;
  byMachine: Record<string, MachineChoice>;
}

const key = "thread-composer";

export function remembered(): Remembered {
  try {
    const v = JSON.parse(localStorage.getItem(key) ?? "{}") as Partial<Remembered>;
    return { machineId: v.machineId, byMachine: v.byMachine ?? {} };
  } catch {
    return { byMachine: {} };
  }
}

/** After a session starts: its machine, and what it used there. */
export function remember(machineId: string, choice: MachineChoice) {
  const r = remembered();
  const sel = choice.sel && { instanceId: choice.sel.instanceId, model: choice.sel.model, options: choice.sel.options };
  r.machineId = machineId;
  r.byMachine[machineId] = { ...choice, sel };
  try {
    localStorage.setItem(key, JSON.stringify(r, (_, v) => (typeof v === "bigint" ? v.toString() : v)));
  } catch {
    // A convenience: without storage it just doesn't stick.
  }
}

// What each machine had when it was last read, for a new session aimed at it
// while it is offline (docs/thread-composer.md §5.3): its projects, and its
// providers and models. Kept on this device only; read again whenever the
// machine answers.

const lastKey = "thread-last-known";

interface LastKnown {
  projects: Record<string, JsonValue[]>;
  providers: Record<string, JsonValue>;
}

function lastKnown(): LastKnown {
  try {
    const v = JSON.parse(localStorage.getItem(lastKey) ?? "{}") as Partial<LastKnown>;
    return { projects: v.projects ?? {}, providers: v.providers ?? {} };
  } catch {
    return { projects: {}, providers: {} };
  }
}

function keep(v: LastKnown) {
  try {
    localStorage.setItem(lastKey, JSON.stringify(v));
  } catch {
    // A convenience: without storage it just doesn't stick.
  }
}

/** Keeps the projects of the machines that answered (those listed), each machine's as a whole. */
export function keepProjects(projects: readonly Project[], answered: readonly string[]) {
  const v = lastKnown();
  for (const id of answered) v.projects[id] = projects.filter((p) => p.machineId === id).map((p) => toJson(ProjectSchema, p));
  keep(v);
}

export function lastProjects(machineId: string): Project[] {
  return (lastKnown().projects[machineId] ?? []).map((j) => fromJson(ProjectSchema, j, { ignoreUnknownFields: true }));
}

export function keepProviders(machineId: string, res: ListProvidersResponse) {
  const v = lastKnown();
  v.providers[machineId] = toJson(ListProvidersResponseSchema, res);
  keep(v);
}

export function lastProviders(machineId: string): ListProvidersResponse | undefined {
  const j = lastKnown().providers[machineId];
  return j === undefined ? undefined : fromJson(ListProvidersResponseSchema, j, { ignoreUnknownFields: true });
}
