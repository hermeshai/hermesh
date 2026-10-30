// Shortcuts are Electron accelerators everywhere (main/menu.ts registers
// them); what the menus and the page show is made from those, in each
// system's own way (docs/windows.md §5.2).

export type KeyPlatform = "mac" | "win";

/**
 * The shortcuts whose keys differ: Windows has no ⌃⌘, and its Ctrl+Alt is
 * AltGr on many keyboards (AltGr+0 types } or @), so they get their own.
 */
export const platformKeys: Record<KeyPlatform, { toggleSidebar: string; toggleInspector: string }> = {
  mac: { toggleSidebar: "Ctrl+Cmd+S", toggleInspector: "Alt+Cmd+0" },
  win: { toggleSidebar: "Ctrl+B", toggleInspector: "Ctrl+Shift+I" },
};

/** "Shift+CmdOrCtrl+N" → "⇧⌘N", in the order macOS shows modifiers, or "Ctrl+Shift+N" on Windows. */
export function keys(accelerator: string, platform: KeyPlatform): string {
  const parts = accelerator.split("+");
  const key = parts.pop() ?? "";
  if (platform === "win") {
    const names: [string[], string][] = [[["Ctrl", "CmdOrCtrl"], "Ctrl"], [["Alt"], "Alt"], [["Shift"], "Shift"]];
    return [...names.filter(([ms]) => ms.some((m) => parts.includes(m))).map(([, name]) => name), key].join("+");
  }
  const symbols: [string, string][] = [["Ctrl", "⌃"], ["Alt", "⌥"], ["Shift", "⇧"], ["Cmd", "⌘"], ["CmdOrCtrl", "⌘"]];
  return symbols.filter(([m]) => parts.includes(m)).map(([, sym]) => sym).join("") + key;
}
