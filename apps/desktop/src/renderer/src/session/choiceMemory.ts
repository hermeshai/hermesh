import type { RuntimeMode } from "@hermesh/proto/identity";
import type { ModelSelection } from "@hermesh/proto/session";

// Kept free of runtime proto enum imports so Node's strip-only test runner runs choice tests directly.

const scratchProject = "scratch";

export interface MachineChoice {
  projectId?: string;
  sel?: Pick<ModelSelection, "instanceId" | "model" | "options">;
  /** Last explicit model/effort chosen for Her on this machine, kept separate from coding agents' `sel`. */
  herSel?: Pick<ModelSelection, "instanceId" | "model" | "options">;
  mode?: RuntimeMode;
  /** Per-project working copy choice on this machine: true = separate worktree, false = current checkout. */
  worktrees?: Record<string, boolean>;
}

export interface Remembered {
  machineId?: string;
  byMachine: Record<string, MachineChoice>;
}

const key = "session-composer";

export interface ChoiceStore {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
}

const defaultStore = (): ChoiceStore | undefined => (typeof localStorage === "undefined" ? undefined : localStorage);

function writeRemembered(r: Remembered, store = defaultStore()) {
  try {
    store?.setItem(key, JSON.stringify(r, (_, v) => (typeof v === "bigint" ? v.toString() : v)));
  } catch {
    // A convenience: without storage it just doesn't stick.
  }
}

export function remembered(store = defaultStore()): Remembered {
  try {
    const raw = store?.getItem(key) ?? store?.getItem("thread-composer") ?? "{}";
    const v = JSON.parse(raw) as Partial<Remembered>;
    return { machineId: v.machineId, byMachine: v.byMachine ?? {} };
  } catch {
    return { byMachine: {} };
  }
}

export function rememberedWorktree(machineId: string, projectId: string, store = defaultStore()): boolean | undefined {
  if (!machineId || !projectId || projectId === scratchProject) return undefined;
  const v = remembered(store).byMachine[machineId]?.worktrees?.[projectId];
  return typeof v === "boolean" ? v : undefined;
}

export function rememberWorktree(machineId: string, projectId: string, worktree: boolean, store = defaultStore()) {
  if (!machineId || !projectId || projectId === scratchProject) return;
  const r = remembered(store);
  const prev = r.byMachine[machineId];
  r.byMachine[machineId] = { ...prev, worktrees: { ...prev?.worktrees, [projectId]: worktree } };
  writeRemembered(r, store);
}

export function forgetWorktree(machineId: string, projectId: string, store = defaultStore()) {
  if (!machineId || !projectId) return;
  const r = remembered(store);
  const prev = r.byMachine[machineId];
  if (!prev?.worktrees || !(projectId in prev.worktrees)) return;
  const worktrees = { ...prev.worktrees };
  delete worktrees[projectId];
  r.byMachine[machineId] = { ...prev, worktrees: Object.keys(worktrees).length > 0 ? worktrees : undefined };
  writeRemembered(r, store);
}

export function remember(machineId: string, choice: Omit<MachineChoice, "worktrees"> & { worktree?: boolean }, store = defaultStore()) {
  if (!machineId) return;
  const r = remembered(store);
  const prev = r.byMachine[machineId];
  const sel = choice.sel && { instanceId: choice.sel.instanceId, model: choice.sel.model, options: choice.sel.options };
  const worktrees = { ...prev?.worktrees };
  if (choice.projectId && choice.projectId !== scratchProject && choice.worktree !== undefined) {
    worktrees[choice.projectId] = choice.worktree;
  }
  r.machineId = machineId;
  r.byMachine[machineId] = {
    ...prev,
    projectId: choice.projectId ?? prev?.projectId,
    sel: sel ?? prev?.sel,
    mode: choice.mode ?? prev?.mode,
    worktrees: Object.keys(worktrees).length > 0 ? worktrees : undefined,
  };
  writeRemembered(r, store);
}

export function rememberedHer(machineId: string, store = defaultStore()): MachineChoice["herSel"] {
  if (!machineId) return undefined;
  return remembered(store).byMachine[machineId]?.herSel;
}

export function rememberHer(machineId: string, sel: Pick<ModelSelection, "instanceId" | "model" | "options">, store = defaultStore()) {
  if (!machineId || !sel.model) return;
  const r = remembered(store);
  const prev = r.byMachine[machineId];
  r.byMachine[machineId] = { ...prev, herSel: { instanceId: sel.instanceId, model: sel.model, options: sel.options } };
  writeRemembered(r, store);
}

export function forgetHer(machineId: string, store = defaultStore()) {
  if (!machineId) return;
  const r = remembered(store);
  const prev = r.byMachine[machineId];
  if (!prev?.herSel) return;
  const next = { ...prev };
  delete next.herSel;
  r.byMachine[machineId] = next;
  writeRemembered(r, store);
}
