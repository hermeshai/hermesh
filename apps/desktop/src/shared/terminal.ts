import path from "node:path";

export type MacTerminalCandidate = {
  name: string;
  bundleId: string;
  appName: string;
  systemPath?: string;
};

export const MAC_TERMINALS: readonly MacTerminalCandidate[] = [
  { name: "Ghostty", bundleId: "com.mitchellh.ghostty", appName: "Ghostty.app" },
  { name: "iTerm", bundleId: "com.googlecode.iterm2", appName: "iTerm.app" },
  { name: "Warp", bundleId: "dev.warp.Warp-Stable", appName: "Warp.app" },
  { name: "WezTerm", bundleId: "com.github.wez.wezterm", appName: "WezTerm.app" },
  { name: "kitty", bundleId: "net.kovidgoyal.kitty", appName: "kitty.app" },
  { name: "Alacritty", bundleId: "org.alacritty", appName: "Alacritty.app" },
  {
    name: "Terminal",
    bundleId: "com.apple.Terminal",
    appName: "Terminal.app",
    systemPath: "/System/Applications/Utilities/Terminal.app",
  },
];

/** Extracts bundle identifiers of currently running macOS apps from `lsappinfo list` output. */
export function parseRunningBundleIds(lsappinfoOutput: string): Set<string> {
  const out = new Set<string>();
  for (const m of lsappinfoOutput.matchAll(/bundleID="([^"]+)"/g)) {
    if (m[1]) out.add(m[1]);
  }
  return out;
}

function installedTerminalPath(
  c: MacTerminalCandidate,
  homeDir: string,
  exists: (p: string) => boolean,
): string | undefined {
  const locations = c.systemPath
    ? [c.systemPath]
    : [path.posix.join("/Applications", c.appName), path.posix.join(homeDir, "Applications", c.appName)];
  return locations.find((p) => exists(p));
}

/**
 * Selects the macOS terminal app path to open a folder in:
 * 1. Prefer a terminal app that is currently running (in priority order).
 * 2. Otherwise pick the first installed terminal app (falling back to Terminal.app).
 */
export function chooseMacTerminal(
  homeDir: string,
  runningBundleIds: ReadonlySet<string>,
  exists: (p: string) => boolean,
): string {
  for (const c of MAC_TERMINALS) {
    if (!runningBundleIds.has(c.bundleId)) continue;
    const found = installedTerminalPath(c, homeDir, exists);
    if (found) return found;
  }
  for (const c of MAC_TERMINALS) {
    const found = installedTerminalPath(c, homeDir, exists);
    if (found) return found;
  }
  return "/System/Applications/Utilities/Terminal.app";
}
