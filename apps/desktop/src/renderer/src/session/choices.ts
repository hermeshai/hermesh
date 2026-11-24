import { fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";
import type { RuntimeMode } from "@hermesh/proto/identity";
import { ProjectSchema, type Project } from "@hermesh/proto/local";
import { ListProvidersResponseSchema, type ListProvidersResponse, type ModelSelection } from "@hermesh/proto/session";

export interface MachineChoice {
  projectId?: string;
  sel?: Pick<ModelSelection, "instanceId" | "model" | "options">;
  mode?: RuntimeMode;
}

export interface Remembered {
  machineId?: string;
  byMachine: Record<string, MachineChoice>;
}

const key = "session-composer";

export function remembered(): Remembered {
  try {
    const raw = localStorage.getItem(key) ?? localStorage.getItem("thread-composer") ?? "{}";
    const v = JSON.parse(raw) as Partial<Remembered>;
    return { machineId: v.machineId, byMachine: v.byMachine ?? {} };
  } catch {
    return { byMachine: {} };
  }
}

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

const lastKey = "session-last-known";

interface LastKnown {
  projects: Record<string, JsonValue[]>;
  providers: Record<string, JsonValue>;
}

function lastKnown(): LastKnown {
  try {
    const raw = localStorage.getItem(lastKey) ?? localStorage.getItem("thread-last-known") ?? "{}";
    const v = JSON.parse(raw) as Partial<LastKnown>;
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
