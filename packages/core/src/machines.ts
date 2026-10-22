import type { AgentStatus } from "@hermesh/proto/machine";

const osTitles: Record<string, string> = { darwin: "macOS", macos: "macOS", linux: "Linux", windows: "Windows", ios: "iOS" };

/** A machine's "darwin/arm64" as "macOS · arm64"; a trusted device's "macos" as "macOS". */
export function platformTitle(platform: string): string {
  const [os, arch] = platform.split("/");
  return [osTitles[os] ?? os, arch].filter(Boolean).join(" · ");
}

/** The coding agents Hermesh helps install: shown even when missing. */
const suggestedAgents = ["claude", "codex"];

/**
 * A machine's coding agents as tiles: ready ones first, then installed but
 * not logged in, then the suggested ones that are missing; by name within each.
 */
export function agentTiles(agents: AgentStatus[]): AgentStatus[] {
  const rank = (a: AgentStatus) => (a.available && a.loggedIn ? 0 : a.available ? 1 : 2);
  return agents.filter((a) => a.available || suggestedAgents.includes(a.name)).sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}
