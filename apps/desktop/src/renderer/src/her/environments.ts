import type { Machine } from "@hermesh/proto/local";

// Her's environments as the Mac app shows them (docs/her-server.md §9): the
// machines Her is on for, each as its Machine.her last said, and the machines
// the first run offers. Worked out from Watch's machines alone, so it can be
// checked without a window.

type Env = Pick<Machine, "machineId" | "machineName" | "local" | "online" | "her">;

/** The environments: the machines Her is on for, this Mac first, then by name. */
export function environments<M extends Env>(machines: M[]): M[] {
  return machines.filter((m) => m.her?.enabled).sort((a, b) => Number(b.local) - Number(a.local) || a.machineName.localeCompare(b.machineName));
}

/** Reached now: this Mac in process, another machine while it is online. */
export const reachable = (m: Pick<Machine, "local" | "online">) => m.local || m.online;

/** The environment to show: the one remembered while it is one, else the first. */
export function pickEnvironment<M extends Env>(envs: M[], remembered: string | undefined): M | undefined {
  return envs.find((m) => m.machineId === remembered) ?? envs[0];
}

/** Unread messages and approvals, over an environment's assistants (her counts them). */
export function unreadCount(envs: Pick<Machine, "her">[]): number {
  return envs.reduce((n, m) => n + (m.her?.enabled ? m.her.unread : 0), 0);
}

/**
 * Her on without her grant (docs/her-mesh.md §3.4): her machine keeps a key for her (its software knows
 * the mesh), and the owner has not let her use the computers yet, or revoked it.
 */
export const lacksGrant = (her: Machine["her"]): boolean => !!her?.enabled && !her.mesh && her.clientPublicKey.length > 0;

/**
 * Her was on once and her data is still on the machine (docs/her-server.md §5.6): as the machine says while
 * it answers, else as last seen. A machine older than HerStatus.data never says so.
 */
export const herDataKept = (m: Pick<Machine, "status" | "her">): boolean => (m.status?.her ?? m.her)?.data ?? false;

/** What an environment's Her is doing, for its page. */
export type EnvState =
  | { kind: "offline"; seen?: Date }
  | { kind: "starting" }
  | { kind: "failed"; error: string }
  /** Running, with no model connection that works: the console is where one is added. */
  | { kind: "noModel" }
  | { kind: "ready" };

export function envState(m: Pick<Machine, "local" | "online" | "lastSeen" | "her">): EnvState {
  if (!reachable(m)) return { kind: "offline", seen: m.lastSeen ? new Date(Number(m.lastSeen.seconds) * 1000 + Math.floor(m.lastSeen.nanos / 1e6)) : undefined };
  const her = m.her;
  if (her?.error) return { kind: "failed", error: her.error };
  if (!her?.running) return { kind: "starting" };
  if (!her.modelReady) return { kind: "noModel" };
  return { kind: "ready" };
}

/**
 * A server added since the first run's New Server was chosen (before: the machines then), once Her can be
 * turned on there: offered first, as the owner added it for Her.
 */
export function newServer<M extends Env & Pick<Machine, "status">>(machines: M[], before: ReadonlySet<string>): M | undefined {
  return herChoices(machines).find((c) => !c.why && !before.has(c.machine.machineId))?.machine;
}

/** A machine the first run (and Add Environment) offers, and why it can't be chosen when it can't. */
export type Choice<M> = { machine: M; why: null | "windows" | "offline" | "unsupported"; detail?: string };

/**
 * Where Her can run (docs/her-server.md §9.2): this Mac, then the other
 * machines of the owner, those online first. Windows machines show, not to be
 * chosen yet; a machine offline can't be asked now. Those Her is on for
 * already are environments, not choices.
 */
export function herChoices<M extends Env & Pick<Machine, "status">>(machines: M[]): Choice<M>[] {
  return machines
    .filter((m) => !m.her?.enabled)
    .map((m): Choice<M> => {
      const unsupported = m.her?.unsupported ?? "";
      if (unsupported === "windows" || (m.status?.platform ?? "").split("/")[0] === "windows") return { machine: m, why: "windows" };
      if (unsupported !== "") return { machine: m, why: "unsupported", detail: unsupported };
      if (!reachable(m)) return { machine: m, why: "offline" };
      return { machine: m, why: null };
    })
    .sort((a, b) => Number(b.machine.local) - Number(a.machine.local) || Number(a.why !== null) - Number(b.why !== null) || a.machine.machineName.localeCompare(b.machine.machineName));
}
