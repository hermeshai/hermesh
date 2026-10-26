import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { SessionStatus } from "@hermesh/proto/session";
import { agoText, dateOf, isActive, platformTitle } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { AgentList, AgentPage, sortAgents } from "../AgentList";
import { useCheckAgents } from "../agents";
import { local } from "../api";
import { Icon } from "../icons";
import { useNow, useStore, type Store } from "../store";
import { GoRow, SRow, Switch, type Ask } from "./common";

function machineIcon(m: Machine): string {
  if (m.local) return "laptop";
  return m.status?.platform.startsWith("darwin") ? "mini" : "server";
}

function running(st: Store, machineId: string): number {
  return st.sessions.filter((x) => x.machineId === machineId && (isActive(x) || x.status === SessionStatus.WAITING_INPUT)).length;
}

/** Machines: the machines, each opening its coding agents with their projects, and sessions; renaming, pausing, removing. */
export function Machines({ open, select, ask }: { open: string; select: (id: string) => void; ask: (a: Ask) => void }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(30_000);
  // "<machine>" opens a machine; "<machine>/<agent>", one of its agents.
  const [id, agent] = open.split("/");
  const m = st.machines.find((x) => x.machineId === id);
  if (m && agent)
    return (
      <div className="form">
        <AgentPage key={open} m={m} agent={agent} />
      </div>
    );
  if (m) return <MachineDetail key={m.machineId} m={m} ask={ask} open={(a) => select(`${m.machineId}/${a}`)} />;
  // A machine only has this machine alone.
  if (st.status?.machineOnly) {
    const self = st.machines.find((x) => x.local);
    return self ? <MachineDetail m={self} ask={ask} open={(a) => select(`${self.machineId}/${a}`)} /> : null;
  }
  return (
    <div className="form">
      {!st.machines.some((x) => x.local) && (
        <div className="fgroup">
          <ShareRow />
        </div>
      )}
      <div className="fgroup" role="list" aria-label={t("pages.machines")}>
        {st.machines.map((x) => {
          const seen = dateOf(x.lastSeen);
          const n = running(st, x.machineId);
          const sub = x.online
            ? [x.local ? t("thisMac") : "", platformTitle(x.status?.platform ?? "")].filter(Boolean).join(" · ")
            : t("machines.lastOnline", { ago: seen ? agoText(seen, now) : t("machines.longAgo") });
          return (
            <GoRow key={x.machineId} icon={<span className="mtile"><Icon name={machineIcon(x)} /></span>} title={x.machineName} sub={sub} onClick={() => select(x.machineId)}>
              <span className="fv-side">
                <i className={`dotc ${x.online ? "on" : ""}`} />
                {!x.online ? t("offline") : x.status?.paused ? t("machines.paused") : n ? t("machines.runningCount", { count: n }) : t("online")}
              </span>
            </GoRow>
          );
        })}
        {st.machines.length === 0 && <div className="frow muted">{t("machines.none")}</div>}
      </div>
      <div className="set-acts">
        <button className="btn" onClick={() => st.set({ sheet: { kind: "addMachine" } })}>{t("machines.add")}</button>
      </div>
    </div>
  );
}

/** Sharing this Mac's coding agents: Hermesh runs them as the owner would, never reading their keys. */
function ShareRow() {
  const { t } = useTranslation("settings");
  const st = useStore();
  // The switch's new state while hermeshd (and, to turn it on, the signer) works on it.
  const [pending, setPending] = useState<boolean | null>(null);
  const on = pending ?? st.status?.localMachineEnabled ?? false;
  const set = async (v: boolean) => {
    setPending(v);
    await st.run(() => local.setLocalMachine({ enabled: v, machineName: st.status?.localMachineName ?? "" }));
    setPending(null);
  };
  return (
    <SRow title={t("machines.share")} desc={t("machines.shareDesc")}>
      <Switch on={on} label={t("machines.share")} set={set} disabled={pending !== null || !st.status} />
    </SRow>
  );
}

function MachineDetail({ m, ask, open }: { m: Machine; ask: (a: Ask) => void; open: (agent: string) => void }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(30_000);
  const seen = dateOf(m.lastSeen);
  const paused = m.status?.paused ?? false;
  const version = m.status?.version || m.version;
  const agents = sortAgents(m.status?.agents);
  useCheckAgents(m.local);

  const rename = () =>
    ask({
      title: t("machines.renameTitle"),
      message: t("machines.renameMessage"),
      field: { label: t("machines.machineName"), value: m.machineName, button: t("machines.renameButton"), run: (name) => st.run(() => local.renameMachine({ machineId: m.machineId, machineName: name })) },
    });
  const remove = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("removeTitle", { name: m.machineName }),
        message: t("machines.removeMessage"),
        button: t("removeButton"),
        run: () => local.removeMachine({ machineId: m.machineId }),
        done: t("removed", { name: m.machineName }),
      },
    });
  const setPaused = (on: boolean) =>
    st.run(() => local.setMachinePaused({ machineId: m.machineId, paused: on }), t(on ? "machines.pausedToast" : "machines.resumedToast", { name: m.machineName }));

  return (
    <div className="form">
      <div className="dhero">
        <span className="mtile xl">
          <Icon name={machineIcon(m)} />
        </span>
        <div>
          <b>{m.machineName}</b>
          <span>
            {[
              m.local ? t("thisMac") : "",
              m.online ? t("online") : t("machines.offlineSince", { ago: seen ? agoText(seen, now) : "—" }),
              platformTitle(m.status?.platform ?? ""),
              version ? `Hermesh ${version}` : "",
            ]
              .filter(Boolean)
              .join(" · ")}
          </span>
        </div>
      </div>
      {/* A machine only is there to share them. */}
      {m.local && !st.status?.machineOnly && (
        <div className="fgroup">
          <ShareRow />
        </div>
      )}
      <div>
        <div className="fhead">{t("machines.codingAgents")}</div>
        <AgentList
          m={m}
          agents={agents}
          onOpen={open}
          unknown={
            <div className="frow muted">
              {(m.online || m.local) && <Mark size={14} variant="seq" />}
              {t(m.online || m.local ? "machines.checkingAgents" : "machines.offlineAgents")}
            </div>
          }
        />
      </div>
      <div>
        <div className="fhead">{t("machines.sessions")}</div>
        <div className="fgroup">
          <SRow title={t("machines.pause")} desc={t("machines.pauseDesc")}>
            <Switch on={paused} label={t("machines.pause")} disabled={!m.online && !m.local} set={setPaused} />
          </SRow>
          <div className="frow">
            <span style={{ flex: 1 }}>{t("machines.running", { count: running(st, m.machineId) })}</span>
            <button className="btn sm" onClick={() => (window.hermesh?.showInMain({ machine: m.machineId }), window.close())}>
              {t("showInMain")}
            </button>
          </div>
        </div>
      </div>
      <div className="set-acts">
        <button className="btn" onClick={rename}>{t("machines.rename")}</button>
        {!m.local && (
          <button className="btn danger push" onClick={remove}>{t("remove")}</button>
        )}
      </div>
    </div>
  );
}
