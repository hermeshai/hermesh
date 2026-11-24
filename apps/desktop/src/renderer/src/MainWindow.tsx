import { lazy, Suspense, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { parseAppLink } from "@hermesh/core";
import { agentName } from "@hermesh/ui";
import { local } from "./api";
import { Overview } from "./Overview";
import { RequestCards } from "./RequestCards";
import { SearchPalette } from "./Search";
import { SessionPage } from "./SessionPage";
import { SheetHost } from "./Sheets";
import { Rail } from "./Rail";
import { Sidebar } from "./Sidebar";
import { useCommands } from "./commands";
import { useLeaving } from "./components";
import { Icon } from "./icons";
import { layoutOf, setSidebar, usePeekEnd } from "./layout";
import { platform } from "./platform";
import { errorText, useStore } from "./store";

// Her's page and its client load when it is first shown.
const HerPage = lazy(() => import("./her/HerPage"));

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
    set({ peek: null });
  }, [st.selection, set]);
  const peeking = st.peek !== null;
  useEffect(() => {
    if (!peeking) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && !e.defaultPrevented && set({ peek: null });
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  }, [peeking, set]);
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
  const search = useLeaving(st.searching ? JSON.stringify(st.searchFor) : null);
  return (
    <div className={`mainwrap ${sidebar === "docked" ? "" : "railed"} ${sidebar === "over" ? "peek" : ""}`}>
      {sidebar === "docked" ? <Sidebar /> : <Rail />}
      {sidebar === "over" && <Sidebar />}
      {sidebar === "over" && <div className="peek-scrim" onClick={() => setSidebar(st, false)} />}
      <section className="content">
        {st.selection.kind === "newSession" || st.selection.kind === "session" ? (
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
      {search.shown !== null && <SearchPalette key={search.shown} leaving={search.leaving} />}
      <SheetHost />
      <ToastView />
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
