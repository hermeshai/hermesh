import type { ComputerPermission } from "../../../shared/bridge";

// Her's computer use on this Mac, as the Her entry's settings show it
// (docs/her-server.md §4.4): her's desktop/status, and what each permission
// asks of the owner. macOS holds hermeshd responsible for the desktop tools,
// so the owner switches on hermeshd under Accessibility, and Hermesh, the app
// it is in, under Screen Recording.

/** her's DesktopStatus, desktop/status's result (@hermesh/her-protocol). */
export type DesktopStatus = {
  enabled: boolean;
  program?: string | null;
  accessibility: DesktopPermission;
  screen_recording: DesktopPermission;
};

export type DesktopPermission = "granted" | "denied" | "unknown" | "unsupported";

/** A permission, and its list's name in System Settings: Accessibility is Device Control and Data Access from macOS 27, Screen Recording is Screen & System Audio Recording from macOS 15. */
export type PermissionRow = {
  kind: ComputerPermission;
  list: "accessibility" | "deviceControl" | "screen" | "screenAudio";
  granted: boolean;
};

/** Nothing to show (not macOS, or her can't say), agent-desktop missing, or the permissions and whether the tools are on. */
export type ComputerUseView = { kind: "none" } | { kind: "missing" } | { kind: "permissions"; enabled: boolean; rows: PermissionRow[] };

export function computerUseView(status: DesktopStatus | null, macos: number): ComputerUseView {
  if (!status || status.accessibility === "unsupported") return { kind: "none" };
  if (!status.program) return { kind: "missing" };
  return {
    kind: "permissions",
    enabled: status.enabled,
    rows: [
      { kind: "accessibility", list: macos >= 27 ? "deviceControl" : "accessibility", granted: status.accessibility === "granted" },
      { kind: "screen", list: macos >= 15 ? "screenAudio" : "screen", granted: status.screen_recording === "granted" },
    ],
  };
}

/** desktop/status's result as hermeshd hands it over (a her Response's result, JSON), or null for her's error or nothing. */
export function parseDesktopStatus(result: Uint8Array | undefined, failed: boolean): DesktopStatus | null {
  if (failed || !result?.length) return null;
  try {
    const s = JSON.parse(new TextDecoder().decode(result)) as Partial<DesktopStatus>;
    return typeof s.enabled === "boolean" && typeof s.accessibility === "string" && typeof s.screen_recording === "string" ? (s as DesktopStatus) : null;
  } catch {
    return null;
  }
}
