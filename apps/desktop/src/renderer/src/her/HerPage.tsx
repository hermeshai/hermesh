import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Methods, Op } from "@hermesh/her-protocol";
import type { Machine } from "@hermesh/proto/local";
import { agoText } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { errorText, useNow, useStore } from "../store";
import type { SendOp } from "./Approval";
import { AssistantPanel } from "./AssistantPanel";
import { useConversation, useEnvironment, useKept, useWaiting } from "./connection";
import { HerContext, useHer, type Her, type HerSheet, type Panel } from "./context";
import { Conversation, putInComposer, showRun } from "./Conversation";
import { environments, envState, pickEnvironment } from "./environments";
import { RoutinesPanel } from "./RoutinesPanel";
import { FirstRun, Setup } from "./Setup";
import { AccessSheet, HerDialog } from "./sheets";
import { TopBar } from "./TopBar";
import "../session/markdown/markdown.css";
import "../session/session.css";
import "./her.css";

// The Her entry (docs/her-server.md §9): a conversation with Her, the
// environment's one assistant the apps show for now, full width; its
// environments one at a time, chosen beside its name when there are several.
// The first run while Her runs nowhere. Connections to her are open only
// while this page shows.

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

export default function HerPage() {
  const st = useStore();
  const envs = useMemo(() => environments(st.machines), [st.machines]);
  const wanted = st.selection.kind === "her" ? st.selection.machine : undefined;
  const machine = pickEnvironment(envs, wanted ?? rememberedEnv());
  useEffect(() => {
    if (machine) rememberEnv(machine.machineId);
  }, [machine?.machineId]);
  const { set } = st;
  const choose = useCallback((machineId: string) => set({ selection: { kind: "her", machine: machineId } }), [set]);
  if (!machine) return <FirstRun onDone={choose} />;
  return <Environment key={machine.machineId} machine={machine} envs={envs} choose={choose} />;
}

function Environment({ machine, envs, choose }: { machine: Machine; envs: Machine[]; choose: (machineId: string) => void }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const { show } = st;
  const env = useEnvironment(machine);
  const kept = useKept(machine.machineId);
  const waiting = useWaiting(machine);
  const [panel, setPanel] = useState<Panel>(null);
  const [sheet, setSheet] = useState<HerSheet>(null);
  const id = machine.machineId;
  // Until her answers, and while the machine is offline: Her and the conversation as this Mac keeps them.
  const bot = env.bots.find((b) => b.default) ?? kept?.bot;
  const live = useConversation(machine, bot, show);
  const conv = live.view || !kept?.view ? live : { ...live, view: kept.view, session: kept.session };
  // Delivered meanwhile: what waits is asked for again as Her changes.
  const refreshWaiting = waiting.refresh;
  const anyWaiting = waiting.list.length > 0;
  useEffect(() => {
    if (anyWaiting) refreshWaiting();
  }, [bot, anyWaiting, refreshWaiting]);
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
  // Her's conversation, as the page and the panels beside it act on it.
  const op: SendOp = useCallback(
    async (o: Op) => {
      if (!conv.rpc || !conv.session) {
        show(t("env.notConnected", { name: machine.machineName }));
        return false;
      }
      try {
        await conv.rpc.request("session/op", { session_id: conv.session, op: o });
        return true;
      } catch (e) {
        show(errorText(e));
        return false;
      }
    },
    [conv.rpc, conv.session, machine.machineName, show],
  );
  const her = useMemo<Her>(
    () => ({ machine, envs, choose, env, bot, panel, setPanel, sheet, setSheet, call, run, openConsole }),
    [machine, envs, choose, env, bot, panel, sheet, call, run, openConsole],
  );
  const shown = bot ? panel : null;
  return (
    <HerContext.Provider value={her}>
      <div className={`her ${shown ? "bgopen" : ""}`}>
        <div className="scol">
          <TopBar />
          {bot ? <Conversation bot={bot} conv={conv} op={op} waiting={waiting.list} onWaiting={refreshWaiting} /> : <EnvStatus machine={machine} />}
        </div>
        {bot && shown?.kind === "settings" && <AssistantPanel bot={bot} view={conv.view} op={op} onClose={() => setPanel(null)} />}
        {bot && shown?.kind === "routines" && (
          <RoutinesPanel
            bot={bot}
            view={conv.view}
            route={shown.route}
            onDraft={(text) => {
              setPanel(null);
              putInComposer(id, text);
            }}
            onShowBubble={(runId) => showRun(id, runId)}
            onClose={() => setPanel(null)}
          />
        )}
        {sheet?.kind === "access" && <AccessSheet bot={sheet.bot} />}
        {sheet?.kind === "add" && <AddEnvironmentSheet />}
      </div>
    </HerContext.Provider>
  );
}

/** In place of the conversation while the environment has none to show: Her starting there, failing to, or the machine away. */
function EnvStatus({ machine }: { machine: Machine }) {
  const { t } = useTranslation("her");
  const now = useNow(30_000);
  const state = envState(machine);
  const name = machine.machineName;
  const body = (() => {
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
  })();
  return <div className="dwrap">{body}</div>;
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
          her.choose(id);
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
