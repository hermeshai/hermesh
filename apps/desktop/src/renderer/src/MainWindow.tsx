import { lazy, Suspense, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { isHerConversation, parseAppLink } from "@hermesh/core";
import { agentName } from "@hermesh/ui";
import { local } from "./api";
import { Overview } from "./Overview";
import { RequestCards } from "./RequestCards";
import { SearchPalette } from "./Search";
import { SessionPage } from "./SessionPage";
import { ImageLightboxHost } from "./session/files/ImageLightbox";
import { SheetHost } from "./Sheets";
import { Sidebar } from "./Sidebar";
import { useCommands } from "./commands";
import { useLeaving } from "./components";
import { Icon } from "./icons";
import { layoutOf, setSidebar, usePeekEnd } from "./layout";
import { keys, ownKeys, platform } from "./platform";
import { errorText, useStore } from "./store";

// Her's page and its client load when it is first shown.
const HerPage = lazy(() => import("./her/HerPage"));
const HerConversationPage = lazy(() => import("./her/HerPage").then((m) => ({ default: m.HerConversationPage })));

export function MainWindow() {
  const st = useStore();
  useCommands();
  useEffect(() => {
    if (!window.hermesh?.material) return;
    document.documentElement.classList.add("vibrant");
    return () => document.documentElement.classList.remove("vibrant");
  }, []);
  const { set } = st;
  useEffect(
    () =>
      window.hermesh?.onCommand((c) => {
        if (c.startsWith("searchGrant:")) set({ searching: true, searchFor: { grant: c.slice("searchGrant:".length) } });
        if (c.startsWith("searchMachine:")) set({ searching: true, searchFor: { machine: c.slice("searchMachine:".length) } });
      }),
    [set],
  );
  const clients = useRef(st.clients);
  clients.current = st.clients;
  const { show } = st;
  useEffect(
    () =>
      window.hermesh?.onLink((url) => {
        const l = parseAppLink(url);
        if (l?.kind !== "wake") return;
        local.openWakeLink({ code: l.code }).then((o) => {
          const c = clients.current.find((x) => x.clientId === o.clientId);
          set({ sheet: { kind: "wake", clientId: o.clientId, agent: c ? agentName(c.label) : o.clientId, code: l.code, name: o.name } });
        }, (e) => show(errorText(e)));
      }),
    [set, show],
  );
  usePeekEnd(st);
  useEffect(() => {
    set({ peek: null, bgTask: null });
  }, [st.selection, set]);
  const cardFits = layoutOf({ ...st, inspector: true, peek: null }).card === "docked";
  const peeking = st.peek !== null || (st.background && !cardFits);
  useEffect(() => {
    if (!peeking) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && !e.defaultPrevented && set({ peek: null, ...(cardFits ? {} : { background: false }) });
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  }, [peeking, cardFits, set]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "n") {
        e.preventDefault();
        set({ selection: { kind: "newSession" } });
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [set]);
  const { sidebar } = layoutOf(st);
  // A conversation with Her opens her chat (docs/her-conversations.md §3.2).
  const herTask = st.selection.kind === "session" ? st.sessions.find((x) => st.selection.kind === "session" && x.sessionId === st.selection.id && isHerConversation(x)) : undefined;
  const search = useLeaving(st.searching ? JSON.stringify(st.searchFor) : null);
  return (
    <div className={`mainwrap ${sidebar === "docked" ? "" : "noside"} ${sidebar === "over" ? "peek" : ""}`}>
      {sidebar === "docked" && <Sidebar />}
      <section className="content">
        {herTask && !st.status?.machineOnly ? (
          <Suspense fallback={<div className="ctool drag" />}>
            <HerConversationPage session={herTask} />
          </Suspense>
        ) : st.selection.kind === "newSession" || st.selection.kind === "session" ? (
          <SessionPage />
        ) : (st.selection.kind === "her" || st.selection.kind === "progress") && !st.status?.machineOnly ? (
          <Suspense fallback={<div className="ctool drag" />}>
            <HerPage />
          </Suspense>
        ) : (
          <Overview />
        )}
        <RequestCards />
      </section>
      {/* Overlays come after .content in the DOM: Chromium evaluates -webkit-app-region in DOM order, not z-index, so their no-drag controls must follow .ctool.drag. */}
      {sidebar === "hidden" && <SidebarToggle />}
      {sidebar === "over" && (
        <>
          <div className="peek-scrim" onClick={() => setSidebar(st, false)} />
          <Sidebar />
        </>
      )}
      {search.shown !== null && <SearchPalette key={search.shown} leaving={search.leaving} />}
      <SheetHost />
      {/* Every picture of the app, a session's or Her's, opens here. */}
      <ImageLightboxHost />
      <ToastView />
    </div>
  );
}

/** The sidebar hidden: its toggle stays where the sidebar had it, after the traffic lights (or the app menu). */
function SidebarToggle() {
  const st = useStore();
  const { t } = useTranslation("common");
  return (
    <div className="side-float drag">
      <AppMenuButton />
      <button className="tb" onClick={() => setSidebar(st, true)} title={`${t("window.showSidebar")} ${keys(ownKeys.toggleSidebar)}`} aria-label={t("window.showSidebar")}>
        <Icon name="sidebar" />
      </button>
    </div>
  );
}

export function AppMenuButton() {
  const { t } = useTranslation("common");
  if (platform === "mac") return null;
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
  const { shown: toast, leaving } = useLeaving(st.toast);
  if (!toast) return null;
  return (
    <div className={`toast ${leaving ? "leaving" : ""}`} inert={leaving} role="status">
      <Icon name={toast.failed ? "alert" : "check"} />
      <span>{toast.text}</span>
      {toast.undo && (
        <button className="btn sm" onClick={() => (st.set({ toast: null }), toast.undo?.())}>{t("window.undo")}</button>
      )}
    </div>
  );
}
