import type { Machine } from "@hermesh/proto/local";
import { MachineProblemKind, type MachineProblem } from "@hermesh/proto/machine";
import { i18n } from "../../shared/i18n";
import { Icon } from "./icons";

// What keeps a machine from working (MachineStatus.problems), in words the
// person acts on: what is wrong, what it breaks, how it recovers. The same
// for this Mac and for the owner's other machines, on every screen that
// shows a machine: needs-you on the overview, its node on the map, its page
// in settings.

export const problemsOf = (m: Machine): MachineProblem[] => m.status?.problems ?? [];

/** Out of processes altogether: nothing starts; the rest only warns. */
export const severe = (p: MachineProblem) => p.kind === MachineProblemKind.PROCESSES && p.used >= p.limit;

const gb = (bytes: bigint) => `${(Number(bytes) / 2 ** 30).toFixed(1)} GB`;

/** The machine as the text names it: this Mac, or its name. */
export const machineLabel = (m: Machine) => (m.local ? i18n.t("common:health.thisMac") : m.machineName);

export function problemTitle(m: Machine, p: MachineProblem): string {
  const name = machineLabel(m);
  switch (p.kind) {
    case MachineProblemKind.PROCESSES:
      return i18n.t(severe(p) ? "common:health.processesTitle" : "common:health.processesTitleNear", { name });
    case MachineProblemKind.DISK:
      return i18n.t("common:health.diskTitle", { name });
    case MachineProblemKind.FILES:
      return i18n.t("common:health.filesTitle", { name });
    default:
      return i18n.t("common:health.shellPathTitle", { name });
  }
}

export function problemBody(p: MachineProblem): string {
  const n = { used: String(p.used), limit: String(p.limit), culprit: p.culprit, count: p.culpritCount };
  switch (p.kind) {
    case MachineProblemKind.PROCESSES:
      if (severe(p)) return i18n.t(p.culprit ? "common:health.processesBodyCulprit" : "common:health.processesBody", n);
      return i18n.t(p.culprit ? "common:health.processesBodyNearCulprit" : "common:health.processesBodyNear", n);
    case MachineProblemKind.DISK:
      return i18n.t("common:health.diskBody", { free: gb(p.used) });
    case MachineProblemKind.FILES:
      return i18n.t("common:health.filesBody", n);
    default:
      return i18n.t("common:health.shellPathBody", { detail: p.detail });
  }
}

/** A few words for the machine's node on the map. */
export function problemShort(p: MachineProblem): string {
  switch (p.kind) {
    case MachineProblemKind.PROCESSES:
      return i18n.t(severe(p) ? "common:health.shortProcesses" : "common:health.shortProcessesNear");
    case MachineProblemKind.DISK:
      return i18n.t("common:health.shortDisk");
    case MachineProblemKind.FILES:
      return i18n.t("common:health.shortFiles");
    default:
      return i18n.t("common:health.shortShellPath");
  }
}

/** On this Mac, out of processes: Activity Monitor shows what holds them. */
export const opensActivityMonitor = (m: Machine, p: MachineProblem) => m.local && p.kind === MachineProblemKind.PROCESSES;

/** A machine's problems at the top of its page in settings. */
export function Problems({ m }: { m: Machine }) {
  const ps = problemsOf(m);
  if (ps.length === 0) return null;
  return (
    <div className="health-list" role="list" aria-label={i18n.t("common:health.listLabel")}>
      {ps.map((p) => (
        <div key={p.kind} className={`health ${severe(p) ? "bad" : "warn"}`} role="listitem">
          <Icon name="warn" />
          <div>
            <b>{problemTitle(m, p)}</b>
            <p>{problemBody(p)}</p>
            {opensActivityMonitor(m, p) && (
              <div className="health-acts">
                <button className="btn sm" onClick={() => window.hermesh?.openActivityMonitor()}>{i18n.t("common:health.openActivityMonitor")}</button>
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
