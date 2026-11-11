import type { BrowserStatus, ConfigEdit, ExtensionProblem, ExtensionStatus } from "@hermesh/her-protocol";

// The owner's own Chrome in the console's Browser section, without words: where it stands, from browser/status and `[browser]`.
// On the Mac or Linux computer Her runs on, hermeshd registers her as the Hermesh extension's host while Her is on;
// the extension connects once Chrome has it, and Her uses it once `browser.extension` is on.

/** `[browser]` as this section reads it (config/read): whether Her uses the owner's Chrome, null when not set. */
export type YoursConfig = { extension: boolean | null; legacyBackend: boolean };

export function yoursConfig(config: unknown): YoursConfig {
  const browser = ((config as Record<string, unknown> | null)?.browser ?? {}) as Record<string, unknown>;
  // `backend = "extension"`: the old way of choosing it, rewritten on the next write.
  const legacyBackend = browser.backend === "extension";
  return { extension: typeof browser.extension === "boolean" ? browser.extension : legacyBackend ? true : null, legacyBackend };
}

/**
 * off: not connected, not used. ready: connected, not used yet. on: connected and used.
 * lost: used, but not connected now. outdated: connected, but it or her is too old to use.
 */
export type YoursState = "off" | "ready" | "on" | "lost" | "outdated";

export type Yours = {
  state: YoursState;
  /** The extension Her can use, while one is connected. */
  connected: ExtensionStatus | null;
  problem: ExtensionProblem | null;
  /** The extensions to choose from: several browsers or profiles have it, or the one chosen is away. */
  several: ExtensionStatus[];
  /** Why the last host was refused (an update moved her while Chrome kept the old host), until one connects. */
  refused: string | null;
};

export function yours(status: BrowserStatus | null, config: YoursConfig | null): Yours {
  const problem = status?.extension?.problem ?? null;
  const connected = problem ? null : (status?.extension ?? null);
  const used = config?.extension === true;
  const state: YoursState = problem ? "outdated" : connected ? (used ? "on" : "ready") : used ? "lost" : "off";
  return { state, connected, problem, several: status?.extensions ?? [], refused: status?.refused ?? null };
}

/** The systems Her runs on, each with its own way to add the extension; one her doesn't name reads as a Mac. */
export type HerSystem = "macos" | "linux";

export function herSystem(status: BrowserStatus | null): HerSystem {
  return status?.os === "linux" ? "linux" : "macos";
}

/** The folder to load the extension from until it is in the store: hermeshd keeps the extension
 * there (internal/herhost/extension.go, ExtensionFolder). */
export const extensionFolder: Record<HerSystem, string> = {
  macos: "~/Library/Application Support/Hermesh/Chrome Extension",
  linux: "~/.local/share/hermesh/chrome-extension",
};

/** `[browser]` as the built-in browser's row reads it: whether it is turned on in so many words, the
 * program named, and whether it shows a window (null: auto). */
export type BuiltinConfig = { enabled: boolean; executable: string | null; headless: boolean | null };

export function builtinConfig(config: unknown): BuiltinConfig {
  const browser = ((config as Record<string, unknown> | null)?.browser ?? {}) as Record<string, unknown>;
  const executable = typeof browser.executable === "string" && browser.executable ? browser.executable : null;
  const headless = browser.headless === true || browser.headless === "true" ? true : browser.headless === false || browser.headless === "false" ? false : null;
  return { enabled: browser.enabled === true, executable, headless };
}

/** The built-in browser: on or not, the program it starts (null: none found), and whether with a window. */
export type Builtin = { on: boolean; program: string | null; window: boolean };

export function builtin(status: BrowserStatus | null, config: BuiltinConfig): Builtin {
  return { on: config.enabled, program: config.executable ?? status?.program ?? null, window: config.headless === null ? !!status?.window : !config.headless };
}

/** Turns the built-in browser on, or back to unset: `browser.enabled = false` would turn the owner's Chrome off too. */
export function builtinEdits(on: boolean): ConfigEdit[] {
  return [{ key_path: "browser.enabled", value: on ? true : null }];
}

const programs: [RegExp, string][] = [
  [/^google-chrome(-stable|-beta)?$/, "Google Chrome"],
  [/^chromium(-browser)?$/, "Chromium"],
  [/^microsoft-edge(-stable)?$/, "Microsoft Edge"],
  [/^brave-browser$/, "Brave"],
  // Chrome for Testing on Linux, as `her browser install` unpacks it.
  [/^chrome$/, "Chrome for Testing"],
];

/** "/usr/bin/google-chrome" reads as "Google Chrome"; a Mac app's program has its app's name already. */
export function programName(path: string): string {
  const file = path.split(/[/\\]/).pop() ?? path;
  return programs.find(([re]) => re.test(file))?.[1] ?? file;
}

/** "Google Chrome 142.0.7444.60" reads as "Google Chrome 142". */
export function browserName(browser?: string | null): string {
  return browser?.replace(/ (\d+)(\.\d+)+$/, " $1") || "Chrome";
}

/** The edits of her's user file that turn Her's use of the owner's Chrome on or off; the old way of choosing it goes. */
export function extensionEdits(on: boolean, legacyBackend: boolean): ConfigEdit[] {
  return [{ key_path: "browser.extension", value: on }, ...(legacyBackend ? [{ key_path: "browser.backend", value: null }] : [])];
}
