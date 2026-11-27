import type { Appearance, DarkPalette, LightPalette } from "./appearance";
import type { DaemonInfo } from "./daemon";
import type { ChatGptSignIn, ConnectorSignIn } from "./herSignin";
import type { Language, LanguageChoice } from "./i18n";
import type { UpdateInfo } from "./update";

export type { Appearance, DarkPalette, LightPalette };

// What the preload script gives the page (window.hermesh). Everything else
// the page needs comes from hermeshd through app://hermesh/api.

/** detail: a second line under the label (macOS; elsewhere it follows the label). submenu: the item opens it and is not itself a choice. */
export type MenuItem = { id: string; label: string; enabled?: boolean; checked?: boolean; detail?: string; submenu?: MenuItem[] } | { separator: true };

/** Commands from the app menu and the menu bar, handled by the page. */
export type Command =
  | "overview" | "search" | "find" | "nextAttention" | "nextActive"
  | "toggleInspector" | "toggleSidebar"
  | "newGrant" | "addMachine" | "addProject" | "enterCode"
  | "answer" | "interrupt" | "archive" | "unarchive" | "copySessionId"
  | `open:${string}` | `pairing:${string}`
  | `searchGrant:${string}` | `searchMachine:${string}` | "undo" | "upgrade"
  /** The Her entry at an environment: `her:<machine id>` (a notification's). */
  | `her:${string}`;

/**
 * What the menu bar panel shows (main/tray.ts): whether this Mac reaches the
 * Hermesh service (ok), is still being set up, or can't reach its own
 * background service; how many machines are online, how many sessions need
 * the owner (pairing requests included) and run; a downloaded update.
 */
export type TrayView = {
  state: "ok" | "down" | "setup" | "daemon" | "stopped" | "starting" | "stopping";
  online: number;
  attention: number;
  running: number;
  update: string;
  /** The device that sees and operates this Mac's screen now (Computer Control), "" when none does. */
  control: string;
  /** True when the app can start and stop the background service itself. */
  manageable: boolean;
};

/** The panel's buttons: the first session that needs the owner, the first that runs, the main window, settings, quit, restart to update, ending Computer Control, and starting/stopping the background service. */
export type TrayAction = "attention" | "running" | "open" | "settings" | "quit" | "install" | "stopControl" | "startDaemon" | "stopDaemon";

/** Fixed commands the page may ask to run in Terminal (by key, never by text). */
export type TerminalCommand = "codexLogin" | "claudeLogin" | "antigravityLogin";

/** A macOS permission Her's computer use needs on this Mac (docs/her-server.md §4.4). */
export type ComputerPermission = "accessibility" | "screen";

/** A file of a coding session at a turn ("" for as it is now), within the session's folder (docs/session-files.md). */
export type SessionFileRef = {
  session: string;
  run: string;
  path: string;
  /** A file the owner sent with a prompt, read from its blob on the machine; `path` is its name. */
  blob?: { machine: string; sha: string };
};
/** Where a page's view goes over the window, in the page's coordinates. */
export type PageRect = { x: number; y: number; width: number; height: number };
/** What a page shown tells or asks of the app: a web address to open, or the page's document title. */
export type SessionPageEvent = { kind: "link"; url: string } | { kind: "title"; title: string };

export type Bridge = {
  platform: string;
  /** The window's material shows through the sidebar (macOS's vibrancy, Windows 11's Mica). */
  material: boolean;
  userName: string;
  version: string;
  /** The app's language, chosen by the main process for this run (main/language.ts). */
  language: Language;
  /** The app's appearance at the time this window was created, so its first frame matches without waiting for IPC. */
  appearance?: Appearance;
  preferredLight?: LightPalette;
  preferredDark?: DarkPalette;
  /** macOS's major version, 0 elsewhere: System Settings names its lists by it. */
  macos: number;
  /** Opens the pane of System Settings where the owner allows a computer use permission (main/computer.ts). */
  openComputerPermission(kind: ComputerPermission): void;
  /** The settings window, on a tab ("grants"), optionally with an item in it selected ("env/<machine id>", "grants/<grant id>"; settingsRoute.ts). */
  openSettings(tab?: string): void;
  /** In the settings window: another tab was asked for while it is open ("grants", "env/<id>"). */
  onSettingsTab(cb: (tab: string) => void): () => void;
  /** Whether this window has the focus: now, and whenever it changes. */
  onWindowFocus(cb: (focused: boolean) => void): () => void;
  /** Whether this window is visible on screen (not hidden or minimized): now, and whenever it changes. */
  onWindowVisible?(cb: (visible: boolean) => void): () => void;
  /** Shows the main window with its session list narrowed to a grant (its client id) or a machine. */
  showInMain(filter: { grant: string } | { machine: string }): void;
  /** Shows the main window joining the owner as a trusted device (a machine only becoming one). */
  showUpgrade(): void;
  /** Windows: shows the app menu at x, y (the window's menu bar stays hidden). */
  showAppMenu(x: number, y: number): void;
  /** Shows a native menu at the pointer, or at `at` in the window (under a pop-up button); resolves to the chosen item's id. */
  popupMenu(items: MenuItem[], at?: { x: number; y: number }): Promise<string | null>;
  /** Tells the renderer memory is short (webFrame.clearCache): it drops caches and gives back what its allocators keep (renderer/src/memory.ts). */
  releaseMemory(): void;
  onCommand(cb: (c: Command) => void): () => void;
  /** Tells the app menu what the page shows, so its items enable and check right. */
  setMenuState(state: MenuState): void;
  /** Reports the Her conversation visible in the main window for notification presentation. */
  setVisibleHer(machineId: string, botId: string): void;
  /** The page waits for pairing requests (Connect Your Agent, Add Machine, Add Trusted Device): their notifications come to it. */
  waitForPairings(on: boolean): void;
  openExternal(url: string): void;
  pickDirectory(): Promise<string | null>;
  /** Where a file given to the page (picked, dropped, pasted) is on this computer; empty for one that is not a file there. */
  pathForFile(file: File): string;
  /** A machine's Her console, in a window of its own (docs/her-server.md §5.3); at `page`, its address's hash ("browser/connect"). */
  openHerConsole(machineId: string, page?: string): Promise<void>;
  /** Shows a session's page (an HTML file) in a view of its own over `rect`, with no network, reading its folder from the session's machine. */
  openSessionPage(file: SessionFileRef, rect: PageRect): Promise<boolean>;
  /** Moves the page's view to `rect`; null hides it (something of the app's is over it). */
  placeSessionPage(rect: PageRect | null): void;
  closeSessionPage(): void;
  onSessionPage(cb: (e: SessionPageEvent) => void): () => void;
  /** The page with the web files of its folder, in the default browser (the owner's click: it has the network there). */
  openSessionPageInBrowser(file: SessionFileRef): Promise<void>;
  /** A session's file in Quick Look, read from its machine. */
  quickLookSessionFile(file: SessionFileRef, name: string): Promise<void>;
  /** A session's file saved to Downloads (a number added when its name is taken), and shown there. */
  saveSessionFile(file: SessionFileRef, name: string): Promise<void>;
  /** A session's file in Finder, when the session works on this Mac and the file is there; false otherwise. */
  revealSessionFile(file: SessionFileRef): Promise<boolean>;
  /** Saves a file from an assistant's folder on its machine to Downloads, and shows it there; `sha`: a file Her sent, from this computer's copy when it has one. */
  saveHerFile(machineId: string, botId: string, path: string, sha?: string): Promise<void>;
  /** A file from an assistant's folder on its machine, read whole to show in the window (a PDF); with `sha`, a file Her sent, kept on this computer by its hash once read (docs/her-deliverables.md §4.5). */
  readHerFile(machineId: string, botId: string, path: string, sha?: string): Promise<Uint8Array>;
  /** Which of the files Her sent this computer keeps, by their hash: [hash, name] each. */
  herFilesKept(machineId: string, files: [string, string][]): Promise<string[]>;
  /** Signs in to ChatGPT for a machine's Her in the browser (shared/herSignin.ts); resolves once it ended. */
  signInHerChatGpt(machineId: string): Promise<ChatGptSignIn>;
  /** Opens the browser again at the sign-in under way for a machine. */
  reopenHerChatGpt(machineId: string): void;
  /** Stops the sign-in under way for a machine: it ends cancelled. */
  cancelHerChatGpt(machineId: string): void;
  /** Signs in to a connector of a machine's Her in the browser, for a secure card (shared/herSignin.ts); resolves once it ended. */
  signInHerConnector(machineId: string, name: string): Promise<ConnectorSignIn>;
  /** Opens the browser again at a connector's sign-in under way. */
  reopenHerConnector(machineId: string, name: string): void;
  /** Stops a connector's sign-in under way: it ends cancelled, on her's side too. */
  cancelHerConnector(machineId: string, name: string): void;
  runInTerminal(c: TerminalCommand): void;
  /** The compact first-run window, or the main window. */
  setWindowMode(mode: "onboarding" | "main"): void;
  /** Starts the browser sign-in (or opens the browser again for the one under way); the link comes back through onLink. */
  signIn(center: string, owner?: string): Promise<void>;
  /** Signs this device in again to the owner it signed out of, with the link code the browser sent back (no Touch ID). */
  completeResume(): Promise<void>;
  /** A browser sign-in is under way (also one started before the app was restarted). */
  signInPending(): Promise<boolean>;
  cancelSignIn(): Promise<void>;
  /** Makes this computer the owner's first trusted device with the link code the browser sent back (Setup, one Touch ID). */
  completeSignIn(o: { localMachine: boolean }): Promise<void>;
  /** Where a center's devices connect (its .well-known/hermesh.json). */
  connectURL(center: string): Promise<string>;
  /** This computer's name as the person sees it. */
  computerName(): Promise<string>;
  /** Before hermeshd asks for a new root key: the signer is there (Windows sets it up, with one UAC prompt). */
  readySigner(): Promise<void>;
  /** The installed signer's version, when the app carries a newer one. */
  signerUpdate(): Promise<string | undefined>;
  /** Puts the app's signer in place of the installed one (Windows: one UAC prompt). */
  updateSigner(): Promise<void>;
  /** With the signer set up, programs become administrator here without asking (Windows: UAC off or Never notify). */
  elevatesSilently(): Promise<boolean>;
  /** Opens the system's User Account Control settings. */
  openElevationSettings(): void;
  /** hermesh:// links the app received (sign-in, join and wake), also those that came before the page subscribed. */
  onLink(cb: (url: string) => void): () => void;
  /** What the main process does on its own: notifications and the menu bar (stored in the app's profile). */
  getPreferences(): Promise<Preferences>;
  setPreferences(p: Partial<Preferences>): Promise<Preferences>;
  onPreferences(cb: (p: Preferences) => void): () => void;
  /** Transient live preview while hovering an appearance card in Settings (null restores the saved preference). */
  previewAppearance?(a: Appearance | null): void;
  onAppearancePreview?(cb: (a: Appearance | null) => void): () => void;
  /** Quits and opens the app again (a new language takes effect). */
  relaunch(): void;
  /** Whether the app opens at login (Open Hermesh at login). */
  loginItem(): Promise<LoginItem>;
  setLoginItem(on: boolean): Promise<LoginItem>;
  /** hermeshd's background service waits for the person to allow it in System Settings › Login Items. */
  daemonNeedsApproval(): Promise<boolean>;
  /** Current state of the background service and whether the app manages it. */
  daemonState(): Promise<DaemonInfo>;
  onDaemonState(cb: (d: DaemonInfo) => void): () => void;
  /** Starts the background service (when stopped). */
  startDaemon(): Promise<void>;
  /** Stops the background service after confirming its impact with the user; true when stopped. */
  stopDaemon(): Promise<boolean>;
  /** Opens System Settings › Login Items. */
  openLoginItems(): void;
  /** Opens Activity Monitor (this Mac out of processes: the person finds what holds them). */
  openActivityMonitor(): void;
  /** Opens a project's folder on this computer in its file browser or a terminal. */
  openFolder(path: string, to: "files" | "terminal"): void;
  /** Opens a local session's working folder (its git worktree when present, else its project folder) in Finder/Explorer or terminal. */
  openSessionFolder(sessionId: string, to: "files" | "terminal"): Promise<boolean>;
  /** Shows in Finder a worktree erasing everything kept (it had uncommitted changes). */
  revealKeptWorktree(path: string): void;
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
  appearance: Appearance;
  /** Which light and dark palettes "system" switches between (updated when the person picks one directly). */
  preferredLight?: LightPalette;
  preferredDark?: DarkPalette;
  notifyStarted: boolean;
  notifyQuestion: boolean;
  notifyFailed: boolean;
  notifyTurnDone: boolean;
  /** A dot on the menu bar icon when something needs the owner. */
  menuBadge: boolean;
  /** The app's language; a change takes effect when the app next starts. */
  language: LanguageChoice;
  /**
   * Stay online (docs/settings-environments.md §5.4): Hermesh opens in the menu bar at login and
   * quitting it leaves the background service running, so the owner's other devices and agents
   * reach this computer at any time. Off, the service runs while the app does.
   */
  stayOnline: boolean;
};

/** needsApproval: macOS waits for the person to allow it in System Settings › Login Items. */
export type LoginItem = { on: boolean; needsApproval: boolean };

export type MenuState = {
  main: boolean;
  /** The Her entry shows. */
  her: boolean;
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
