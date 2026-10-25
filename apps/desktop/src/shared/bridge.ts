import type { Language, LanguageChoice } from "./i18n";
import type { UpdateInfo } from "./update";

// What the preload script gives the page (window.hermesh). Everything else
// the page needs comes from hermeshd through app://hermesh/api.

export type MenuItem = { id: string; label: string; enabled?: boolean; checked?: boolean } | { separator: true };

/** Commands from the app menu and the menu bar, handled by the page. */
export type Command =
  | "overview" | "search" | "find" | "nextAttention" | "nextActive"
  | "toggleInspector" | "toggleSidebar"
  | "newGrant" | "addMachine" | "addProject" | "enterCode"
  | "answer" | "interrupt" | "archive" | "unarchive" | "copySessionId"
  | `open:${string}` | `pairing:${string}`
  | `filterGrant:${string}` | `filterMachine:${string}`
  | `filterProject:${string}` | "undo";

/**
 * What the menu bar panel shows (main/tray.ts): whether this Mac reaches the
 * Hermesh service (ok), is still being set up, or can't reach its own
 * background service; how many machines are online, how many sessions need
 * the owner (pairing requests included) and run; a downloaded update.
 */
export type TrayView = { state: "ok" | "down" | "setup" | "daemon"; online: number; attention: number; running: number; update: string };

/** The panel's buttons: the first session that needs the owner, the first that runs, the main window, settings, quit, check for updates and restart to update. */
export type TrayAction = "attention" | "running" | "open" | "settings" | "quit" | "install";

/** Fixed commands the page may ask to run in Terminal (by key, never by text). */
export type TerminalCommand = "codexLogin" | "claudeLogin";

export type Bridge = {
  platform: string;
  userName: string;
  version: string;
  /** The app's language, chosen by the main process for this run (main/language.ts). */
  language: Language;
  /** The settings window, on a tab ("grants"), optionally with an item in it selected ("machines/<machine id>", "grants/<grant id>"). */
  openSettings(tab?: string): void;
  /** In the settings window: another tab was asked for while it is open ("grants", "machines/<id>"). */
  onSettingsTab(cb: (tab: string) => void): () => void;
  /** Shows the main window with its session list narrowed to a grant (its client id) or a machine. */
  showInMain(filter: { grant: string } | { machine: string }): void;
  /** Shows a native menu at the pointer; resolves to the chosen item's id. */
  popupMenu(items: MenuItem[]): Promise<string | null>;
  onCommand(cb: (c: Command) => void): () => void;
  /** Tells the app menu what the page shows, so its items enable and check right. */
  setMenuState(state: MenuState): void;
  /** The page waits for pairing requests (Connect Your Agent, Add Machine, Add Trusted Device): their notifications come to it. */
  waitForPairings(on: boolean): void;
  openExternal(url: string): void;
  pickDirectory(): Promise<string | null>;
  runInTerminal(c: TerminalCommand): void;
  /** The compact first-run window, or the main window. */
  setWindowMode(mode: "onboarding" | "main"): void;
  /** Starts the browser sign-in (or opens the browser again for the one under way); the link comes back through onLink. */
  signIn(center: string): Promise<void>;
  /** A browser sign-in is under way (also one started before the app was restarted). */
  signInPending(): Promise<boolean>;
  cancelSignIn(): Promise<void>;
  /** Makes this computer the owner's first trusted device with the link code the browser sent back (Setup, one Touch ID). */
  completeSignIn(o: { localMachine: boolean }): Promise<void>;
  /** Where a center's devices connect (its .well-known/hermesh.json). */
  connectURL(center: string): Promise<string>;
  /** This computer's name as the person sees it. */
  computerName(): Promise<string>;
  /** hermesh:// links the app received (sign-in and join), also those that came before the page subscribed. */
  onLink(cb: (url: string) => void): () => void;
  /** What the main process does on its own: notifications and the menu bar (stored in the app's profile). */
  getPreferences(): Promise<Preferences>;
  setPreferences(p: Partial<Preferences>): Promise<Preferences>;
  /** Quits and opens the app again (a new language takes effect). */
  relaunch(): void;
  /** Whether the app opens at login (Open Hermesh at login). */
  loginItem(): Promise<LoginItem>;
  setLoginItem(on: boolean): Promise<LoginItem>;
  /** hermeshd's background service waits for the person to allow it in System Settings › Login Items. */
  daemonNeedsApproval(): Promise<boolean>;
  /** Opens System Settings › Login Items. */
  openLoginItems(): void;
  /** The app's updates (main/updater.ts). */
  openUpdates(manual?: boolean): void;
  /** Fit the update window to its content, within the display work area. */
  updateWindowHeight(height: number): void;
  openWhatsNew(): void;
  snoozeUpdate(): Promise<void>;
  returnToApproval(): void;
  getUpdate(): Promise<UpdateInfo>;
  onUpdate(cb: (u: UpdateInfo) => void): () => void;
  checkForUpdates(): void;
  /** Downloads the available update (when automatic download is off). */
  downloadUpdate(): void;
  /** Restart to Update: quits, installs the downloaded update and opens the new version. */
  installUpdate(): void;
  /** Download updates automatically. */
  setAutoDownload(on: boolean): Promise<UpdateInfo>;
  /** The menu bar panel: what it shows, and its changes. */
  trayView(): Promise<TrayView | null>;
  onTrayView(cb: (v: TrayView) => void): () => void;
  trayAction(a: TrayAction): void;
  /** The panel's content height, which the window takes. */
  traySize(height: number): void;
};

export type Preferences = {
  notifyQuestion: boolean;
  notifyFailed: boolean;
  notifyTurnDone: boolean;
  /** A dot on the menu bar icon when something needs the owner. */
  menuBadge: boolean;
  /** The app's language; a change takes effect when the app next starts. */
  language: LanguageChoice;
};

/** needsApproval: macOS waits for the person to allow it in System Settings › Login Items. */
export type LoginItem = { on: boolean; needsApproval: boolean };

export type MenuState = {
  main: boolean;
  session: null | {
    interruptible: boolean;
    answerable: boolean;
    archivable: boolean;
    archived: boolean;
    inspector: boolean;
    /** Its grant (client id and label) and project, for Show Only Sessions by/in “…”. */
    grant: string;
    grantLabel: string;
    project: string;
  };
  sidebar: boolean;
  /** The toast offers Undo (⌘Z undoes it rather than typing). */
  undo: boolean;
};

declare global {
  interface Window {
    hermesh?: Bridge;
  }
}
