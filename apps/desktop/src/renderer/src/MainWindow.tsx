import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Overview } from "./Overview";
import { SearchPalette } from "./Search";
import { SessionPage } from "./SessionPage";
import { SheetHost } from "./Sheets";
import { Sidebar } from "./Sidebar";
import { useCommands } from "./commands";
import { Icon } from "./icons";
import { platform } from "./platform";
import { useStore } from "./store";

export function MainWindow() {
  const st = useStore();
  useCommands();
  // On macOS the window's sidebar material shows through the sidebar (the first-run pages stay opaque).
  useEffect(() => {
    if (window.hermesh?.platform !== "darwin") return;
    document.documentElement.classList.add("vibrant");
    return () => document.documentElement.classList.remove("vibrant");
  }, []);
  const session = st.selection.kind === "session" ? st.sessions.find((s) => s.sessionId === (st.selection as { id: string }).id) : undefined;
  // A selected session that is gone (e.g. its machine forgot it): back to the overview.
  useEffect(() => {
    if (st.selection.kind !== "session" || !st.sessionsLoaded || session) return;
    if (st.moreSessions) st.loadOlderSessions();
    else st.set({ selection: { kind: "overview" } });
  }, [st, session]);
  // "Show in Main Window" in the settings window: the search palette, narrowed to a grant or a machine.
  const { set } = st;
  useEffect(
    () =>
      window.hermesh?.onCommand((c) => {
        if (c.startsWith("searchGrant:")) set({ searching: true, searchFor: { grant: c.slice("searchGrant:".length) } });
        if (c.startsWith("searchMachine:")) set({ searching: true, searchFor: { machine: c.slice("searchMachine:".length) } });
      }),
    [set],
  );
  return (
    <div className={`mainwrap ${st.sidebar ? "" : "noside"}`}>
      <Sidebar />
      <section className="content">
        {session ? <SessionPage s={session} /> : <Overview />}
      </section>
      {st.searching && <SearchPalette key={JSON.stringify(st.searchFor)} />}
      <SheetHost />
      <ToastView />
    </div>
  );
}

/** The window's title bar area when the sidebar is hidden: traffic lights' room and the sidebar toggle. */
export function SidebarToggle() {
  const st = useStore();
  const { t } = useTranslation("common");
  if (st.sidebar) return null;
  return (
    <>
      <span className="tl-room" />
      <AppMenuButton />
      <button className="tb" onClick={() => st.set({ sidebar: true })} aria-label={t("window.showSidebar")}>
        <Icon name="sidebar" />
      </button>
    </>
  );
}

/** Windows: the app menu, under its button (there is no menu bar; main/menu.ts). */
export function AppMenuButton() {
  const { t } = useTranslation("common");
  if (platform !== "win") return null;
  return (
    <button
      className="tb"
      aria-label={t("window.menu")}
      title={t("window.menu")}
      onClick={(e) => {
        const r = e.currentTarget.getBoundingClientRect();
        window.hermesh?.showAppMenu(r.left, r.bottom + 4);
      }}
    >
      <Icon name="menu" />
    </button>
  );
}

export function ToastView() {
  const st = useStore();
  const { t } = useTranslation("common");
  if (!st.toast) return null;
  const toast = st.toast;
  return (
    <div className="toast" role="status">
      <Icon name="check" />
      <span>{toast.text}</span>
      {toast.undo && (
        <button className="btn sm" onClick={() => (st.set({ toast: null }), toast.undo?.())}>{t("window.undo")}</button>
      )}
    </div>
  );
}
