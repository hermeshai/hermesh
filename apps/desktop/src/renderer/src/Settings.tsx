import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { dateOf } from "@hermesh/core";
import { Icon } from "./icons";
import { ToastView } from "./MainWindow";
import { openPairing } from "./sheets/Pairing";
import { SheetHost } from "./Sheets";
import { settingsRoute, settingsUp, type SettingsPage } from "./settingsRoute";
import { useNow, useStore, type Store } from "./store";
import { Account } from "./settings/Account";
import { AskSheet, type Ask } from "./settings/common";
import { Environment, addEnvironmentMenu, envItemTitle, envTitle, machineIcon, useDaemon } from "./settings/Environment";
import { General } from "./settings/General";
import { Grants } from "./settings/Grants";
import { Notifications } from "./settings/Notifications";
import "./settings/settings.css";

/** The environments in the sidebar: this Mac first (whether or not it is one), then by name. */
export function sidebarEnvs(st: Store): { id: string; m?: Machine }[] {
  const local = st.machines.find((m) => m.local);
  const others = st.machines.filter((m) => !m.local && !st.status?.machineOnly).sort((a, b) => a.machineName.localeCompare(b.machineName));
  return [{ id: local?.machineId ?? "this", m: local }, ...others.map((m) => ({ id: m.machineId, m }))];
}

/**
 * The settings window (#settings or #settings/<page>[/<item>], settingsRoute.ts), laid out as
 * System Settings (docs/settings-environments.md §4.1): the account at the top of the sidebar,
 * the app's own pages, then every environment, this Mac first; the page on the right with back
 * and forward in its title bar.
 */
export function SettingsWindow() {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(60_000);
  const [start] = useState(() => settingsRoute(location.hash.replace(/^#settings\/?/, "")));
  const [page, setPage] = useState<SettingsPage>(start.page);
  const [item, setItem] = useState(start.item);
  const [forward, setForward] = useState<string[]>([]);
  const [ask, setAsk] = useState<Ask | null>(null);
  // A machine only sets what is this Mac's: no grants, and no other machine.
  const only = st.status?.machineOnly === true;
  const shown: SettingsPage = only && page === "grants" ? "general" : page;
  const local = st.machines.find((m) => m.local);
  // "this" is this Mac's machine while it is one.
  const resolved = shown === "env" ? item.replace(/^this(?=\/|$)/, local?.machineId ?? "this") : item;
  const envId = shown === "env" ? resolved.split("/")[0] : "";
  const open = (p: SettingsPage, it = "") => (setPage(p), setItem(it), setForward([]));
  const go = (it: string) => (setItem(it), setForward([]));
  const up = settingsUp(shown, resolved);
  const back = () => (setForward([resolved, ...forward]), setItem(up));
  const fwd = () => (setItem(forward[0]), setForward(forward.slice(1)));
  // Grants running out within a week, said once beside Access.
  const soon = st.clients.filter((c) => {
    const d = dateOf(c.expiresAt);
    return !c.revoked && d && d.getTime() - now.getTime() <= 7 * 86_400_000;
  }).length;
  const envs = sidebarEnvs(st);
  const env = envs.find((e) => e.id === envId);
  const title = (it: string) => (shown === "env" ? envItemTitle(t, it, envs.find((e) => e.id === it.split("/")[0])?.m) : shown === "account" ? t(only ? "pages.accountOnly" : "pages.account") : t(`pages.${shown}`));
  const label = title(resolved);

  // A request's notification, while a sheet here waits for requests (main/ipc.ts).
  const latest = useRef(st);
  latest.current = st;
  useEffect(() => window.hermesh?.onCommand((c) => c.startsWith("pairing:") && openPairing(latest.current, c.slice("pairing:".length))), []);
  useEffect(() => window.hermesh?.onSettingsTab((s) => {
    const r = settingsRoute(s);
    open(r.page, r.item);
  }), []);
  useEffect(() => {
    document.title = label;
  }, [label]);
  // The window's material shows through the sidebar, as in the main window.
  useEffect(() => {
    if (!window.hermesh?.material) return;
    document.documentElement.classList.add("vibrant");
    return () => document.documentElement.classList.remove("vibrant");
  }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (ask) setAsk(null);
      else if (!st.sheet) window.close();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [ask, st.sheet]);

  const nav = (p: "general" | "notifications" | "grants", icon: string, extra?: React.ReactNode) => (
    <button className={`nav-i ${p === shown ? "sel" : ""}`} aria-current={p === shown ? "page" : undefined} onClick={() => open(p)}>
      <Icon name={icon} />
      <span className="nav-n">{t(`pages.${p}`)}</span>
      {extra}
    </button>
  );
  const user = window.hermesh?.userName || "—";
  return (
    <div className="settings">
      <aside className="side set-side">
        <div className="set-side-top drag" />
        <nav className="side-nav" aria-label={t("title")}>
          <button className={`set-me ${shown === "account" ? "sel" : ""}`} aria-label={t(only ? "pages.accountOnly" : "pages.account")} aria-current={shown === "account" ? "page" : undefined} onClick={() => open("account")}>
            <span className="av me" aria-hidden="true">{user.slice(0, 1).toUpperCase()}</span>
            <span>
              <b>{user}</b>
              <small>{t(only ? "pages.accountOnly" : "pages.account")}</small>
            </span>
          </button>
          <div className="set-gap" />
          {nav("general", "gear")}
          {nav("notifications", "bell")}
          {!only && nav("grants", "key", soon > 0 && <span className="set-soon">{t("expiringSoon", { count: soon })}</span>)}
          <div className="set-head">
            {t("pages.envs")}
            {!only && (
              <button className="set-add" aria-label={t("env.add")} title={t("env.add")} onClick={() => void addEnvironmentMenu(st, t)}>
                <Icon name="plus" />
              </button>
            )}
          </div>
          {envs.map((e) => (
            <button key={e.id} className={`nav-i ${shown === "env" && envId === e.id ? "sel" : ""}`} aria-current={shown === "env" && envId === e.id ? "page" : undefined} onClick={() => open("env", e.id)}>
              <Icon name={machineIcon(e.m)} />
              <span className="nav-n">{envTitle(t, e.m)}</span>
              <EnvState m={e.m} />
            </button>
          ))}
        </nav>
      </aside>
      <main className="set-main" aria-label={label}>
        <div className="set-top drag">
          <button className="set-nb" aria-label={t("back", { page: title(up) })} disabled={!item || up === resolved} onClick={back}>
            <Icon name="chevL" />
          </button>
          <button className="set-nb" aria-label={t("forward")} disabled={!forward.length} onClick={fwd}>
            <Icon name="chevR" />
          </button>
          <h1>{label}</h1>
        </div>
        <div className="set-body">
          {shown === "general" && <General />}
          {shown === "notifications" && <Notifications />}
          {shown === "grants" && <Grants open={item} select={go} />}
          {shown === "account" && <Account ask={setAsk} />}
          {shown === "env" && <Environment key={envId} item={resolved} go={go} />}
        </div>
      </main>
      {ask && <AskSheet ask={ask} close={() => setAsk(null)} />}
      <SheetHost />
      <ToastView />
    </div>
  );
}

/** An environment's state in the sidebar, when it is not simply online. */
function EnvState({ m }: { m?: Machine }) {
  const { t } = useTranslation("settings");
  const daemon = useDaemon();
  if (!m) return null;
  if (m.local && daemon?.manageable && daemon.state === "stopped") return <span className="set-st">{t("env.stoppedShort")}</span>;
  if (m.status?.paused && (m.online || m.local)) return <span className="set-st warn">{t("env.paused")}</span>;
  if (!m.online && !m.local) return <span className="set-st">{t("env.offline")}</span>;
  return null;
}
