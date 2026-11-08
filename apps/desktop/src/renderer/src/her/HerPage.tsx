import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { Methods } from "@hermesh/her-protocol";
import type { Machine } from "@hermesh/proto/local";
import { agoText } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { errorText, useNow, useStore } from "../store";
import { Icon } from "../icons";
import { SidebarToggle } from "../MainWindow";
import { AssistantList } from "./AssistantList";
import { useEnvironment, useWaiting } from "./connection";
import { HerContext, useHer, type Her, type HerSheet, type Panel } from "./context";
import { Conversation } from "./Conversation";
import { environments, envState, pickEnvironment, reachable } from "./environments";
import { menuKeys } from "./menus";
import { FirstRun, Setup } from "./Setup";
import { AccessSheet, HerDialog, NewAssistantSheet } from "./sheets";
import "../session/markdown/markdown.css";
import "../session/session.css";
import "./her.css";

// The Her entry (docs/her-server.md §9): Her's environments, one at a time,
// with a switcher over the assistant list when there is more than one; the
// assistant's conversation beside the list. The first run while Her runs
// nowhere. Connections to her are open only while this page shows.

/** The environment shown last. */
function rememberedEnv(): string | undefined {
  try {
    return localStorage.getItem("her-env") ?? undefined;
  } catch {
    return undefined;
  }
}

function rememberEnv(machine: string) {
  try {
    localStorage.setItem("her-env", machine);
  } catch {
    // A convenience: without storage the first environment shows.
  }
}

/** The assistant last shown in each environment. */
function rememberedBots(): Record<string, string> {
  try {
    const v = JSON.parse(localStorage.getItem("her-bots") ?? "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}

function rememberBot(machine: string, bot: string) {
  try {
    localStorage.setItem("her-bots", JSON.stringify({ ...rememberedBots(), [machine]: bot }));
  } catch {
    // A convenience: without storage the environment opens at Her.
  }
}

export default function HerPage() {
  const st = useStore();
  const envs = useMemo(() => environments(st.machines), [st.machines]);
  const sel = st.selection.kind === "her" ? st.selection : undefined;
  const machine = pickEnvironment(envs, sel?.machine ?? rememberedEnv());
  useEffect(() => {
    if (machine) rememberEnv(machine.machineId);
  }, [machine?.machineId]);
  const { set } = st;
  const show = useCallback((machineId: string) => set({ selection: { kind: "her", machine: machineId } }), [set]);
  if (!machine) return <FirstRun onDone={show} />;
  return <Environment key={machine.machineId} machine={machine} envs={envs} wantedBot={sel?.machine === machine.machineId ? sel?.bot : undefined} />;
}

function Environment({ machine, envs, wantedBot }: { machine: Machine; envs: Machine[]; wantedBot: string | undefined }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const env = useEnvironment(machine);
  const waiting = useWaiting(machine);
  const [panel, setPanel] = useState<Panel>(null);
  const [sheet, setSheet] = useState<HerSheet>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const id = machine.machineId;
  const bots = env.bots;
  const bot = bots.find((b) => b.id === wantedBot) ?? bots.find((b) => b.id === rememberedBots()[id]) ?? bots.find((b) => b.default) ?? bots[0];
  const { set, show } = st;
  const selectBot = useCallback(
    (botId: string) => {
      rememberBot(id, botId);
      set({ selection: { kind: "her", machine: id, bot: botId } });
    },
    [id, set],
  );
  // Delivered meanwhile: what waits is asked for again as the assistants change.
  const refreshWaiting = waiting.refresh;
  const anyWaiting = waiting.list.length > 0;
  useEffect(() => {
    if (anyWaiting) refreshWaiting();
  }, [bots, anyWaiting, refreshWaiting]);
  const rpc = env.rpc;
  const call = useCallback(
    <M extends keyof Methods & string>(method: M, params: Methods[M][0]): Promise<Methods[M][1]> =>
      rpc ? rpc.request(method, params) : Promise.reject(new Error(t("env.notConnected", { name: machine.machineName }))),
    [rpc, machine.machineName],
  );
  const run = useCallback(
    async (action: () => Promise<unknown>, done?: string) => {
      try {
        await action();
        if (done) show(done);
        return true;
      } catch (e) {
        show(errorText(e));
        return false;
      }
    },
    [show],
  );
  const openConsole = useCallback(() => {
    window.hermesh?.openHerConsole(id).catch((e: unknown) => show(errorText(e)));
  }, [id, show]);
  const her = useMemo<Her>(
    () => ({ machine, envs, env, bot, selectBot, panel, setPanel, sheet, setSheet, renaming, setRenaming, call, run, openConsole }),
    [machine, envs, env, bot, selectBot, panel, sheet, renaming, call, run, openConsole],
  );
  return (
    <HerContext.Provider value={her}>
      <div className="her">
        <aside className="her-side" aria-label={t("page.label")}>
          <div className="her-top drag">
            <SidebarToggle />
            {envs.length > 1 ? <Switcher /> : <span className="her-title">{t("page.title")}</span>}
            <span className="her-gap" />
            <MoreMenu single={envs.length === 1} />
            <button className="tb" title={t("page.newAssistant")} aria-label={t("page.newAssistant")} disabled={!env.rpc} onClick={() => setSheet({ kind: "new" })}>
              <Icon name="edit" />
            </button>
          </div>
          <AssistantList />
        </aside>
        {bot ? (
          <Conversation key={bot.id} bot={bot} waiting={waiting.list} onWaiting={refreshWaiting} />
        ) : (
          <div className="her-col">
            <div className="ctool drag" />
            <div className="dwrap">
              <EnvStatus machine={machine} />
            </div>
          </div>
        )}
        {sheet?.kind === "new" && <NewAssistantSheet />}
        {sheet?.kind === "access" && <AccessSheet bot={sheet.bot} />}
        {sheet?.kind === "add" && <AddEnvironmentSheet />}
      </div>
    </HerContext.Provider>
  );
}

/** In place of a conversation while the environment has none to show: Her starting there, failing to, or the machine away. */
function EnvStatus({ machine }: { machine: Machine }) {
  const { t } = useTranslation("her");
  const now = useNow(30_000);
  const state = envState(machine);
  const name = machine.machineName;
  switch (state.kind) {
    case "offline":
      return (
        <div className="empty" role="status">
          <Mark size={40} variant="wait" />
          <b>{t("env.offline", { name })}</b>
          <span>{state.seen ? t("env.offlineSeen", { name, ago: agoText(state.seen, now) }) : t("chat.unread", { name })}</span>
        </div>
      );
    case "failed":
      return (
        <div className="empty" role="alert">
          <Mark size={40} variant="fail" />
          <b>{t("env.failed", { name })}</b>
          <span className="sel-text">{state.error}</span>
        </div>
      );
    case "starting":
      return (
        <div className="empty" role="status">
          <Mark size={40} variant="pulse" />
          <b>{t("env.starting", { name })}</b>
        </div>
      );
    default:
      return (
        <div className="empty">
          <span className="mini-spin" />
        </div>
      );
  }
}

/** Another environment: the first run's choices, in a sheet. */
function AddEnvironmentSheet() {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const close = () => her.setSheet(null);
  return (
    <HerDialog label={t("setup.addTitle")} width={520} onClose={close}>
      <h3 id="sheet-title">{t("setup.addTitle")}</h3>
      <p className="sub">{t("setup.sub")}</p>
      <Setup
        onDone={(id) => {
          close();
          st.set({ selection: { kind: "her", machine: id } });
        }}
        onNewServer={() => {
          close();
          st.set({ sheet: { kind: "addMachine" } });
        }}
      />
      <div className="sheet-foot">
        <button className="btn" onClick={close}>
          {t("sheet.cancel")}
        </button>
      </div>
    </HerDialog>
  );
}

/**
 * The environments, one at a time (docs/her-server.md §9.1): the one shown, with whether it is online;
 * its menu lists each with its unread count and whether it is online, then Add Environment. Opaque,
 * under its button, from its left edge.
 */
function Switcher() {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const [open, setOpen] = useState<false | "mouse" | "key">(false);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState({ left: 0, top: 0 });
  useEffect(() => {
    if (!open) return;
    (open === "key" ? menu.current?.querySelector<HTMLElement>("[role^=menuitem]") : menu.current)?.focus();
    const outside = (e: MouseEvent) => ![btn, menu].some((r) => r.current?.contains(e.target as Node)) && setOpen(false);
    const blur = () => setOpen(false);
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", blur);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", blur);
    };
  }, [open]);
  const close = () => (setOpen(false), btn.current?.focus());
  const choose = (machineId: string) => {
    setOpen(false);
    st.set({ selection: { kind: "her", machine: machineId } });
  };
  const m = her.machine;
  return (
    <>
      <button
        ref={btn}
        className={`her-switch ${open ? "on" : ""}`}
        aria-haspopup="menu"
        aria-expanded={!!open}
        title={t("page.environments")}
        onClick={(e) => {
          if (open) return setOpen(false);
          const r = e.currentTarget.getBoundingClientRect();
          setAt({ left: r.left, top: r.bottom + 4 });
          setOpen(e.detail ? "mouse" : "key");
        }}
      >
        <i className={`dotc ${reachable(m) ? "on" : ""}`} aria-hidden="true" />
        <span className="her-switch-n">{m.machineName}</span>
        <Icon name="chevD" className="sm" />
      </button>
      {open && (
        <div className="vmenu her-menu down" role="menu" aria-label={t("page.environments")} tabIndex={-1} ref={menu} style={at} onKeyDown={(e: ReactKeyboardEvent) => menuKeys(e, menu.current, close)}>
          {her.envs.map((e) => (
            <button key={e.machineId} className="mi" role="menuitemradio" aria-checked={e.machineId === m.machineId} tabIndex={-1} onMouseMove={(ev) => ev.currentTarget.focus()} onClick={() => choose(e.machineId)}>
              <span className="ck">{e.machineId === m.machineId && <Icon name="check" className="sm" />}</span>
              <span className="two">
                {e.machineName}
                <span>{reachable(e) ? t("env.online") : t("env.offlineShort")}</span>
              </span>
              {(e.her?.unread ?? 0) > 0 && <span className="bbadge">{e.her!.unread}</span>}
            </button>
          ))}
          <div className="msep" role="separator" />
          <button className="mi" role="menuitem" tabIndex={-1} onMouseMove={(ev) => ev.currentTarget.focus()} onClick={() => (setOpen(false), her.setSheet({ kind: "add" }))}>
            <Icon name="plus" />
            <span>{t("page.addEnvironment")}</span>
          </button>
        </div>
      )}
    </>
  );
}

/** The environment's console ("Manage this environment"), and Add Environment while the switcher is hidden. */
function MoreMenu({ single }: { single: boolean }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const items = [{ id: "console", label: t("page.manage", { name: her.machine.machineName }), icon: "gear" }, ...(single ? [{ id: "add", label: t("page.addEnvironment"), icon: "plus" }] : [])];
  const [open, setOpen] = useState<false | "mouse" | "key">(false);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState({ right: 0, top: 0 });
  useEffect(() => {
    if (!open) return;
    (open === "key" ? menu.current?.querySelector<HTMLElement>("[role^=menuitem]") : menu.current)?.focus();
    const outside = (e: MouseEvent) => ![btn, menu].some((r) => r.current?.contains(e.target as Node)) && setOpen(false);
    const blur = () => setOpen(false);
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", blur);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", blur);
    };
  }, [open]);
  const close = () => (setOpen(false), btn.current?.focus());
  const choose = (id: string) => {
    setOpen(false);
    if (id === "console") her.openConsole();
    else her.setSheet({ kind: "add" });
  };
  return (
    <>
      <button
        ref={btn}
        className={`tb ${open ? "on" : ""}`}
        title={t("page.more")}
        aria-label={t("page.more")}
        aria-haspopup="menu"
        aria-expanded={!!open}
        onClick={(e) => {
          if (open) return setOpen(false);
          const r = e.currentTarget.getBoundingClientRect();
          setAt({ right: innerWidth - r.right, top: r.bottom + 4 });
          setOpen(e.detail ? "mouse" : "key");
        }}
      >
        <Icon name="more" />
      </button>
      {open && (
        <div className="vmenu her-menu down" role="menu" aria-label={t("page.more")} tabIndex={-1} ref={menu} style={at} onKeyDown={(e: ReactKeyboardEvent) => menuKeys(e, menu.current, close)}>
          {items.map((it) => (
            <button key={it.id} className="mi" role="menuitem" tabIndex={-1} onMouseMove={(e) => e.currentTarget.focus()} onClick={() => choose(it.id)}>
              <Icon name={it.icon} />
              <span>{it.label}</span>
            </button>
          ))}
        </div>
      )}
    </>
  );
}
