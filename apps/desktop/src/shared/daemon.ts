import type { Session } from "@hermesh/proto/session";
import type { GetStatusResponse, Machine, PairingDetails } from "@hermesh/proto/local";

// Numeric SessionStatus proto values (avoids runtime TS enum import under Node strip-only tests).
const STATUS_QUEUED = 1;
const STATUS_PREPARING = 2;
const STATUS_RUNNING = 3;
const STATUS_ARCHIVED = 5;
const STATUS_WAITING_INPUT = 9;

export type DaemonState = "running" | "stopped" | "stopping" | "starting" | "unavailable";

export type DaemonInfo = {
  state: DaemonState;
  /** True when this app manages the background daemon (packaged installation). */
  manageable: boolean;
};

export type DaemonWorld = {
  status: GetStatusResponse | null;
  sessions: Session[];
  machines: Machine[];
  pairings: PairingDetails[];
};

/** Everything on this computer that stopping the background service affects right now. */
export type DaemonImpact = {
  /** Local tasks currently queued, preparing, or running (their turn will be interrupted). */
  runningTasks: number;
  /** Local tasks waiting for an answer (their current turn will end). */
  waitingTasks: number;
  /** Up to 3 titles of affected local tasks, to make the warning concrete. */
  taskTitles: string[];
  /** Device currently controlling this computer's screen, "" when none. */
  screenViewer: string;
  /** Whether Her is running on this computer. */
  herRunning: boolean;
  /** Pending pairing requests or an active signing prompt. */
  pendingApprovals: number;
  /** Whether this computer is online as a machine or initialized node that other devices/agents reach. */
  localMachineOnline: boolean;
};

const activeTurn = new Set<number>([STATUS_QUEUED, STATUS_PREPARING, STATUS_RUNNING]);

/** Summarizes what stopping hermeshd will interrupt or disconnect right now. */
export function assessDaemonImpact(world: DaemonWorld, signerBusy: boolean): DaemonImpact {
  const st = world.status;
  const localMachine = world.machines.find((m) => m.local);
  const localId = st?.localMachineId || localMachine?.machineId || "";
  const localSessions = localId ? world.sessions.filter((s) => s.machineId === localId && s.status !== STATUS_ARCHIVED) : [];
  const running = localSessions.filter((s) => activeTurn.has(s.status));
  const waiting = localSessions.filter((s) => s.status === STATUS_WAITING_INPUT);
  const taskTitles = [...running, ...waiting]
    .map((s) => s.title.trim())
    .filter(Boolean)
    .slice(0, 3);
  const screenViewer = localMachine?.status?.screen?.viewer ?? "";
  const herRunning = !!(localMachine?.status?.her?.running || (localMachine?.her?.enabled && localMachine.online));
  const pendingApprovals = world.pairings.length + (signerBusy ? 1 : 0);
  const localMachineOnline = !!(st && (st.localMachineEnabled || st.machineOnly || st.initialized));
  return {
    runningTasks: running.length,
    waitingTasks: waiting.length,
    taskTitles,
    screenViewer,
    herRunning,
    pendingApprovals,
    localMachineOnline,
  };
}

/** True when stopping the daemon would interrupt active work, screen control, or an approval in progress. */
export function hasActiveWork(impact: DaemonImpact): boolean {
  return impact.runningTasks > 0 || impact.waitingTasks > 0 || impact.screenViewer !== "" || impact.pendingApprovals > 0;
}

export type Translate = (key: string, values?: Record<string, unknown>) => string;

/** Localized bullet lines explaining every consequence of stopping the background service now. */
export function impactLines(impact: DaemonImpact, t: Translate): string[] {
  const lines: string[] = [];
  const sep = t("daemon.titleSeparator");
  if (impact.runningTasks > 0) {
    const titles = impact.taskTitles.slice(0, Math.min(3, impact.runningTasks)).join(sep);
    lines.push(titles ? t("daemon.impactRunningNamed", { count: impact.runningTasks, titles }) : t("daemon.impactRunning", { count: impact.runningTasks }));
  }
  if (impact.waitingTasks > 0) {
    lines.push(t("daemon.impactWaiting", { count: impact.waitingTasks }));
  }
  if (impact.screenViewer) {
    lines.push(t("daemon.impactScreen", { viewer: impact.screenViewer }));
  }
  if (impact.herRunning) {
    lines.push(t("daemon.impactHer"));
  }
  if (impact.pendingApprovals > 0) {
    lines.push(t("daemon.impactApprovals", { count: impact.pendingApprovals }));
  }
  if (impact.localMachineOnline || lines.length === 0) {
    lines.push(t("daemon.impactOffline"));
  }
  return lines;
}

/** Resolves the background service's current state from user intent, transitions, and connectivity. */
export function resolveDaemonState(connected: boolean, userStopped: boolean, transition: "starting" | "stopping" | null): DaemonState {
  if (transition === "stopping") return "stopping";
  if (transition === "starting") return "starting";
  if (connected) return "running";
  return userStopped ? "stopped" : "unavailable";
}
