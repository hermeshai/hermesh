import { Code, ConnectError } from "@connectrpc/connect";
import type { Machine } from "@hermesh/proto/local";

// A machine whose Hermesh is older than this app (docs/version-skew.md).

/**
 * The error says the machine's Hermesh is older than this app (§6.3): her lacks the method, or
 * hermeshd or its services don't know the call.
 */
export function needsUpdate(e: unknown): boolean {
  if (e instanceof ConnectError) return e.code === Code.Unimplemented;
  return (e as { kind?: unknown } | null)?.kind === "method_not_found";
}

/**
 * Whether the owner's devices can call `method` of her's protocol on this machine (§6.1): undefined
 * while unknown (her not running there, or a machine too old to list its methods), which callers
 * take as yes.
 */
export function herAnswers(m: Machine, method: string): boolean | undefined {
  const methods = m.her?.methods ?? [];
  return methods.length ? methods.includes(method) : undefined;
}

/**
 * Whether hermeshd on this machine answers `method` of its MachineAdminService
 * (MachineStatus.methods, docs/settings-environments.md §5.7.6): undefined for a machine
 * from before the list, which callers take as yes and the safety net catches.
 */
export function machineAnswers(m: Machine, method: string): boolean | undefined {
  const methods = m.status?.methods ?? [];
  return methods.length ? methods.includes(method) : undefined;
}

/**
 * Older hermeshd versions rejected archiving a session with `workspace kept: … (!! path)`.
 * Returns `{ path }` when matched (path is "" when none was named), or undefined otherwise.
 */
export function workspaceKeptError(raw: string): { path: string } | undefined {
  if (!raw.startsWith("workspace kept:")) return undefined;
  const m = raw.match(/\((?:!!\s+|\?\?\s+)?([^()]+)\)\s*$/);
  return { path: m?.[1]?.trim() ?? "" };
}
