import { useEffect, useRef } from "react";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { displayTitle, isActive, isArchivable, isInterruptible, needsAttention, pendingQuestion } from "@hermesh/core";
import type { Command, MenuState } from "../../shared/bridge";
import { i18n } from "../../shared/i18n";
import { local } from "./api";
import { copySessionId, focusAnswer, interrupt } from "./session/actions";
import { openPairing } from "./sheets/Pairing";
import { archive, grantLabel, listSections } from "./Sidebar";
import { useStore, type Store } from "./store";

// The app menu's and the menu bar's commands (main/menu.ts, main/tray.ts,
// notifications), and what the menus need to know about the window.
// searchGrant: and searchMachine: are MainWindow's.
export function useCommands() {
  const st = useStore();
  const latest = useRef(st);
  latest.current = st;
  useEffect(() => window.hermesh?.onCommand((c) => run(latest.current, c)), []);

  const s = selected(st);
  const state: MenuState = {
    main: true,
    session: s
      ? {
          interruptible: isInterruptible(s),
          answerable: pendingQuestion(s) !== undefined,
          archivable: isArchivable(s),
          archived: s.status === SessionStatus.ARCHIVED,
          inspector: st.inspector,
          grant: s.createdBy,
          grantLabel: grantLabel(st, s),
          project: s.projectId,
        }
      : null,
    sidebar: st.sidebar,
    undo: st.toast?.undo !== undefined,
  };
  const key = JSON.stringify(state);
  useEffect(() => window.hermesh?.setMenuState(JSON.parse(key)), [key]);
  useEffect(() => () => window.hermesh?.setMenuState({ main: false, session: null, sidebar: true, undo: false }), []);
}

function selected(st: Store): Session | undefined {
  const sel = st.selection;
  return sel.kind === "session" ? st.sessions.find((s) => s.sessionId === sel.id) : undefined;
}

function run(st: Store, c: Command) {
  const s = selected(st);
  switch (c) {
    case "overview":
      return st.set({ selection: { kind: "overview" } });
    case "search":
    case "find":
      return st.set({ searching: true });
    case "nextAttention":
      return next(st, needsAttention);
    case "nextActive":
      return next(st, isActive);
    case "toggleInspector":
      return s && st.set({ inspector: !st.inspector });
    case "toggleSidebar":
      return st.set({ sidebar: !st.sidebar });
    case "newGrant":
    case "addMachine":
    case "addProject":
    case "enterCode":
      return st.set({ sheet: { kind: c } });
    case "answer":
      if (!s || !pendingQuestion(s)) return;
      return setTimeout(focusAnswer);
    case "interrupt":
      return s && isInterruptible(s) && interrupt(st, s);
    case "archive":
      return s && isArchivable(s) && archive(st, s);
    case "unarchive":
      return s?.status === SessionStatus.ARCHIVED && st.run(() => local.unarchiveSession({ sessionId: s.sessionId }), i18n.t("common:sidebar.unarchived", { title: displayTitle(s) }));
    case "copySessionId":
      return s && copySessionId(st, s);
    case "undo":
      return undo(st);
  }
  const [verb, arg] = [c.slice(0, c.indexOf(":")), c.slice(c.indexOf(":") + 1)];
  switch (verb) {
    case "open":
      return open(st, arg);
    case "pairing":
      return openPairing(st, arg);
  }
}

/** The session after the selected one (from the top) that matches, in the sidebar's order. */
function next(st: Store, matches: (s: Session) => boolean) {
  const list = listSections(st, new Date())
    .flatMap((x) => x.sessions)
    .filter(matches);
  if (!list.length) return;
  const i = list.findIndex((s) => s.sessionId === selected(st)?.sessionId);
  st.select(list[(i + 1) % list.length].sessionId);
}

/** A session from a notification or the menu bar. One newer than the list is fetched first (the window would go back to the overview otherwise). */
async function open(st: Store, id: string) {
  const s = st.sessions.find((x) => x.sessionId === id);
  if (!s) await st.refresh();
  else if (s.status === SessionStatus.ARCHIVED && !st.archived) st.set({ archived: true });
  st.select(id);
}

/** ⌘Z: the text field's own undo while typing, otherwise what the toast offers to undo. */
function undo(st: Store) {
  const el = document.activeElement;
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || (el instanceof HTMLElement && el.isContentEditable)) {
    document.execCommand("undo");
    return;
  }
  const t = st.toast;
  if (!t?.undo) return;
  st.set({ toast: null });
  t.undo();
}
