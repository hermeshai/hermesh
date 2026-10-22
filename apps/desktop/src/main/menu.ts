import { app, BrowserWindow, dialog, Menu, shell, type MenuItemConstructorOptions } from "electron";
import type { Command } from "../shared/bridge";
import { i18n } from "../shared/i18n";
import { command, currentMenuState, menuStateListeners } from "./ipc";
import { world, worldListeners } from "./notify";
import { install, openUpdates, updateInfo, updateListeners } from "./updater";
import { mainWindow, settingsVisible, showMain, showSettings, showAbout, showWhatsNew } from "./windows";

// The app menu. Its items are the page's commands (handled by
// renderer/src/commands.ts); what they enable and check follows what the
// page reports (MenuState). Every item that tests click has an id.
//
// Windows and Linux: the same menu, for its shortcuts, but the windows'
// menu bar stays hidden (Alt shows it), since the page has every action.

const mac = process.platform === "darwin";

export function setAppMenu() {
  build();
  menuStateListeners.add(build);
  updateListeners.add(build);
  worldListeners.add(() => ready() !== builtReady && build());
  app.on("browser-window-created", (_e, w) => {
    if (!mac) w.setAutoHideMenuBar(true);
    // The window menu checks the open windows.
    const rebuild = () => setImmediate(build);
    w.on("show", rebuild);
    w.on("hide", rebuild);
    w.on("closed", rebuild);
    rebuild();
  });
}

let builtReady = false;

function build() {
  builtReady = ready();
  Menu.setApplicationMenu(Menu.buildFromTemplate(template()));
}

/** Past the first run: the main window's commands work (opening it when it is closed). */
function ready(): boolean {
  return currentMenuState().main || world.status?.initialized === true || world.status?.machineOnly === true;
}

function template(): MenuItemConstructorOptions[] {
  const t = i18n.getFixedT(null, "main", "menu");
  const st = currentMenuState();
  const s = st.session;
  const main = ready();
  // What only a trusted device does (a machine only approves nothing).
  const owner = main && world.status?.machineOnly !== true;
  const sep = { type: "separator" } as const;
  // Checkbox items flip themselves when clicked: rebuilt, they show the page's state again.
  const item = (id: string, label: string, c: Command, enabled: boolean, more: MenuItemConstructorOptions = {}): MenuItemConstructorOptions => ({
    id,
    label,
    enabled,
    click: () => {
      command(c);
      setImmediate(build);
    },
    ...more,
  });
  const settings: MenuItemConstructorOptions = { id: "settings", label: t("settings"), accelerator: "CmdOrCtrl+,", enabled: main, click: () => showSettings() };
  const settingsOpen = settingsVisible();
  const u = updateInfo().update;
  const updates: MenuItemConstructorOptions[] = [
    { id: "checkForUpdates", label: t("update.check"), click: () => openUpdates(true) },
    ...(u.state === "ready" ? [
      { id: "showUpdateDetails", label: t("update.details"), click: () => openUpdates() },
      { id: "installUpdate", label: t("update.install"), click: install },
    ] : []),
  ];

  return [
    ...(mac
      ? [
          {
            label: "Hermesh",
            submenu: [
              { id: "about", label: t("app.about"), click: showAbout },
              ...updates,
              sep,
              settings,
              sep,
              { role: "services", label: t("app.services") },
              sep,
              { role: "hide", label: t("app.hide") },
              { role: "hideOthers", label: t("app.hideOthers") },
              { role: "unhide", label: t("app.unhide") },
              sep,
              { role: "quit", label: t("app.quit") },
            ],
          } satisfies MenuItemConstructorOptions,
        ]
      : []),
    {
      label: t("file.title"),
      submenu: [
        item("newGrant", t("file.newGrant"), "newGrant", owner, { accelerator: "Shift+CmdOrCtrl+N" }),
        item("addMachine", t("file.addMachine"), "addMachine", owner),
        item("addProject", t("file.addProject"), "addProject", main),
        item("enterCode", t("file.enterCode"), "enterCode", owner, { accelerator: "Shift+CmdOrCtrl+P" }),
        sep,
        ...(mac ? [] : [settings, sep]),
        { role: "close", label: t("file.close") },
      ],
    },
    {
      label: t("edit.title"),
      submenu: [
        {
          id: "undo",
          label: t("edit.undo"),
          accelerator: "CmdOrCtrl+Z",
          // What the main window's toast offers to undo (an archive), unless typing there; otherwise the text field's own undo.
          click: () => {
            const w = BrowserWindow.getFocusedWindow();
            if (currentMenuState().undo && (!w || w === mainWindow())) command("undo");
            else w?.webContents.undo();
          },
        },
        { role: "redo", label: t("edit.redo") },
        sep,
        { role: "cut", label: t("edit.cut") },
        { role: "copy", label: t("edit.copy") },
        { role: "paste", label: t("edit.paste") },
        { role: "selectAll", label: t("edit.selectAll") },
        sep,
        item("find", t("edit.find"), "find", main, { accelerator: "CmdOrCtrl+F" }),
        // ⌘K, as the sidebar's Search shows; the menu lists ⌘F.
        item("search", t("edit.search"), "search", main, { accelerator: "CmdOrCtrl+K", visible: false }),
      ],
    },
    {
      label: t("view.title"),
      submenu: [
        item("overview", t("view.overview"), "overview", main, { accelerator: "CmdOrCtrl+1", type: "checkbox", checked: st.main && !s }),
        item("nextAttention", t("view.nextAttention"), "nextAttention", main, { accelerator: "CmdOrCtrl+2" }),
        item("nextActive", t("view.nextActive"), "nextActive", main, { accelerator: "CmdOrCtrl+3" }),
        sep,
        item("toggleInspector", s?.inspector === false ? t("view.showInspector") : t("view.hideInspector"), "toggleInspector", !!s, { accelerator: "Alt+CmdOrCtrl+0" }),
        sep,
        item("toggleSidebar", st.sidebar ? t("view.hideSidebar") : t("view.showSidebar"), "toggleSidebar", main, { accelerator: mac ? "Ctrl+Cmd+S" : undefined }),
        { role: "togglefullscreen", label: t("view.fullScreen") },
      ],
    },
    {
      label: t("session.title"),
      submenu: [
        item("answer", t("session.answer"), "answer", !!s?.answerable, { accelerator: "CmdOrCtrl+R" }),
        item("interrupt", t("session.interrupt"), "interrupt", !!s?.interruptible, { accelerator: "CmdOrCtrl+." }),
        sep,
        item("copySessionId", t("session.copyId"), "copySessionId", !!s),
        item("filterGrant", s ? t("session.onlyGrant", { name: s.grantLabel }) : t("session.onlyThisGrant"), `filterGrant:${s?.grant ?? ""}`, !!s),
        item("filterProject", s ? t("session.onlyProject", { name: s.project }) : t("session.onlyThisProject"), `filterProject:${s?.project ?? ""}`, !!s),
        sep,
        item("archive", t("session.archive"), "archive", !!s?.archivable, { accelerator: "CmdOrCtrl+E" }),
        item("unarchive", t("session.unarchive"), "unarchive", !!s?.archived),
      ],
    },
    {
      label: t("window.title"),
      submenu: [
        { role: "minimize", label: t("window.minimize") },
        { role: "zoom", label: t("window.zoom") },
        sep,
        { id: "mainWindow", label: "Hermesh", type: "checkbox", checked: !!mainWindow()?.isVisible(), click: () => (showMain(), setImmediate(build)) },
        { id: "settingsWindow", label: t("window.settings"), type: "checkbox", checked: settingsOpen, enabled: main || settingsOpen, click: () => (showSettings(), setImmediate(build)) },
      ],
    },
    {
      role: "help",
      label: t("help.title"),
      submenu: [
        { label: t("help.help"), click: () => shell.openExternal("https://hermesh.io") },
        { id: "whatsNew", label: t("help.whatsNew"), click: showWhatsNew },
        { id: "shortcuts", label: t("help.shortcuts"), click: showShortcuts },
        sep,
        { label: t("help.report"), click: () => shell.openExternal("https://github.com/hermeshai/hermesh/issues") },
      ],
    },
  ];
}

/** The shortcuts, as the menus list them. */
function showShortcuts() {
  const lines: string[] = [];
  for (const m of template()) {
    if (!Array.isArray(m.submenu)) continue;
    for (const i of m.submenu) if (i.accelerator && i.label && i.registerAccelerator !== false) lines.push(`${keys(i.accelerator)}\t${i.label}`);
  }
  dialog.showMessageBox({ message: i18n.t("main:menu.help.shortcuts"), detail: lines.join("\n") });
}

/** "Shift+CmdOrCtrl+N" → "⇧⌘N", in the order macOS shows modifiers. */
function keys(accelerator: string): string {
  const parts = accelerator.split("+");
  const key = parts.pop() ?? "";
  const symbols: [string, string][] = [["Ctrl", "⌃"], ["Alt", "⌥"], ["Shift", "⇧"], ["Cmd", "⌘"], ["CmdOrCtrl", "⌘"]];
  return symbols.filter(([m]) => parts.includes(m)).map(([, sym]) => sym).join("") + key;
}
