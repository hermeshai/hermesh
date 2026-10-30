import { keys as format, platformKeys, type KeyPlatform } from "../../shared/keys";

// The system the app runs on, as the page's tokens name it (html[data-platform]).
export const platform: KeyPlatform = window.hermesh?.platform === "win32" ? "win" : "mac";

/** An accelerator as this system shows it: "CmdOrCtrl+K" reads ⌘K, or Ctrl+K. */
export const keys = (accelerator: string) => format(accelerator, platform);

/** The shortcuts whose keys differ between the systems (shared/keys.ts). */
export const ownKeys = platformKeys[platform];
