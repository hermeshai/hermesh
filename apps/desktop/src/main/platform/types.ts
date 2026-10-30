import type { BrowserWindow, BrowserWindowConstructorOptions, Rectangle } from "electron";
import type { LoginItem, TerminalCommand } from "../../shared/bridge";

/** The kinds of window, for their frames. */
export type WindowKind = "main" | "onboarding" | "settings" | "auxiliary" | "panel";

/**
 * What differs between the systems the app runs on (docs/windows.md §5.1):
 * one implementation each, chosen once at start (platform/index.ts).
 */
export interface Platform {
  /** The pages' data-platform token. */
  token: "mac" | "win";
  /** The installed app runs hermeshd itself; development and test runs use a daemon of their own. */
  installed: boolean;
  /** True when the app may start here; otherwise it has moved, or is quitting. */
  settle(): Promise<boolean>;
  /** The installer started this run to set the app up or remove it (Windows): it does that and quits. */
  installerEvent(): boolean;
  /** Takes hermesh:// links (sign-in and join). */
  registerLinks(): void;
  /** Makes hermeshd start by itself, and start now if it does not run. */
  registerDaemon(): void;
  /** Restarts hermeshd (after an update brought another version). */
  restartDaemon(why: string): void;
  /** The owner turned hermeshd's start off in the system's settings. */
  daemonNeedsApproval(): boolean;
  /** Opening the app at login. */
  loginItem(): LoginItem | Promise<LoginItem>;
  setLoginItem(on: boolean): LoginItem | Promise<LoginItem>;
  /** This run was started at login: the app lives in the menu bar or notification area only. */
  openedAtLogin(): boolean;
  /** The system's settings for what starts at login. */
  openLoginItems(): void;
  /** The signer's program and the arguments for the app's language, where it is the app's child (macOS). */
  signer(language: string): { bin: string; args: string[] } | undefined;
  /** Before hermeshd asks for a new root key: the signer is there (Windows sets it up, with one UAC prompt). */
  readySigner(): Promise<void>;
  /** The installed signer's version, when the app carries a newer one (Windows: the signer is a copy in Program Files). */
  signerUpdate(): Promise<string | undefined>;
  /** Sets the app's signer up over the installed one (Windows: one UAC prompt). */
  updateSigner(): Promise<void>;
  /** Runs one of a fixed set of commands in a terminal window of the system's. */
  runInTerminal(c: TerminalCommand, command: string): void;
  /** Opens a folder in the system's file browser (Finder, File Explorer) or a terminal there; anything but a folder, not at all. */
  openFolder(dir: string, to: "files" | "terminal"): void;
  /** Opens the system's list of processes (Activity Monitor, Task Manager). */
  openProcessMonitor(): void;
  /** This computer's name, as the person sees it. */
  computerName(): Promise<string>;
  /** A window's frame: its title bar and material. */
  frame(kind: WindowKind): BrowserWindowConstructorOptions;
  /** Places the window's own buttons for its mode (macOS's traffic lights). */
  placeButtons(win: BrowserWindow, kind: WindowKind): void;
  /** Moves the window to bounds, animated. */
  grow(win: BrowserWindow, bounds: Rectangle): void;
}
