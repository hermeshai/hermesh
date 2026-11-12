import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { SessionStatus } from "@hermesh/proto/session";
import { agoText, dateOf, isActive, platformTitle } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { AgentList, AgentPage, sortAgents } from "../AgentList";
import { useCheckAgents } from "../agents";
import { local } from "../api";
import { ComputerUse } from "../her-computer/ComputerUse";
import { Problems } from "../health";
import { herDataKept } from "../her/environments";
import { GrantLine } from "../her/GrantLine";
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
          const trusted = !x.local && st.devices.some((d) => d.machineId === x.machineId && !d.thisDevice);
          const sub = (
            x.online
              ? [x.local ? t("thisMac") : trusted ? t("machines.trustedDevice") : "", platformTitle(x.status?.platform ?? "")]
              : [trusted ? t("machines.trustedDevice") : "", t("machines.lastOnline", { ago: seen ? agoText(seen, now) : t("machines.longAgo") })]
          )
            .filter(Boolean)
            .join(" · ");
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

/**
 * Her on a machine (docs/her-server.md §11, phase 1): turned on with Touch ID (SetHer); turned off on a
 * sheet that asks whether her data goes too (§5.6). Windows can't run her yet. Turning her on also lets
 * her use the owner's computers (docs/her-mesh.md §3.3), and says so first.
 */
function HerRow({ m }: { m: Machine }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  // The switch's new state while hermeshd and the signer work on it.
  const [pending, setPending] = useState<boolean | null>(null);
  const on = pending ?? m.her?.enabled ?? false;
  const unsupported = m.her?.unsupported ?? "";
  const windows = unsupported === "windows" || (m.status?.platform ?? "").split("/")[0] === "windows";
  const set = async (v: boolean) => {
    if (!v) return st.set({ sheet: { kind: "herOff", machineId: m.machineId, machineName: m.machineName } });
    setPending(v);
    await st.run(() => local.setHer({ machineId: m.machineId, enabled: v }));
    setPending(null);
  };
  const eraseData = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("machines.herEraseTitle", { name: m.machineName }),
        message: t("machines.herEraseMessage", { name: m.machineName }),
        button: t("machines.herEraseButton"),
        run: () => local.setHer({ machineId: m.machineId, enabled: false, erase: true }),
        done: t("machines.herErased", { name: m.machineName }),
      },
    });
  const desc = windows ? t("machines.herWindows") : unsupported ? t("machines.herUnsupported", { reason: unsupported }) : m.her?.enabled ? t("machines.herDesc") : <>{i18n.t("her:grant.runs")}<br />{i18n.t("her:grant.desc")}</>;
  return (
    <>
      <SRow title={t("machines.herRun")} desc={desc}>
        <Switch on={on} label={t("machines.herRun")} set={set} disabled={pending !== null || (!on && (windows || unsupported !== "")) || (!m.online && !m.local)} />
      </SRow>
      {/* Off, her data kept (docs/her-server.md §5.6): erased while the machine answers. */}
      {!on && herDataKept(m) && (
        <div className="frow">
          <span style={{ flex: 1 }}>{t("machines.herKept")}</span>
          {(m.online || m.local) && <button className="btn sm" onClick={eraseData}>{t("machines.herErase")}</button>}
        </div>
      )}
    </>
  );
}

/** A machine name as hermeshd's machine.CleanName makes it: lowercase letters, digits and -, at most 63 bytes; "" when nothing is left. */
export function cleanName(s: string): string {
  let n = s.trim().toLowerCase();
  if (n.endsWith(".local")) n = n.slice(0, -".local".length);
  n = n.replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
  return n.length > 63 ? n.slice(0, 63).replace(/^-+|-+$/g, "") : n;
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
      field: {
        label: t("machines.machineName"),
        value: m.machineName,
        button: t("machines.renameButton"),
        run: (name) => st.run(() => local.renameMachine({ machineId: m.machineId, machineName: name })),
        // What hermeshd keeps of the name (machine.CleanName): said before saving, not found out after.
        hint: (typed) => {
          const name = cleanName(typed);
          if (!typed.trim() || name === typed.trim()) return undefined;
          return name ? { text: t("machines.nameSavedAs", { name }) } : { text: t("machines.nameUnusable"), error: true };
        },
      },
    });
  // Another trusted device's own machine is removed with the device, in
  // 账号与设备 only: removing just the machine would leave the device trusted.
  const device = m.local ? undefined : st.devices.find((d) => d.machineId === m.machineId && !d.thisDevice);
  const remove = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("removeMachineTitle", { name: m.machineName }),
        message: t("machines.removeMessage"),
        button: t("removeButton"),
        run: (eraseHer) => local.removeMachine({ machineId: m.machineId, eraseHer }),
        done: t("removed", { name: m.machineName }),
        // Her's data there can go too, while the machine answers (docs/her-server.md §5.6).
        herData: m.her?.enabled || herDataKept(m) ? { name: m.machineName, offline: !m.online } : undefined,
      },
    });
  const copyDiagnostics = () =>
    st.run(async () => {
      const { text } = await local.getDiagnostics({});
      await navigator.clipboard.writeText(text);
    }, t("machines.diagnosticsCopied"));
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
              m.local ? t("thisMac") : device ? t("machines.trustedDevice") : "",
              m.online ? t("online") : t("machines.offlineSince", { ago: seen ? agoText(seen, now) : "—" }),
              platformTitle(m.status?.platform ?? ""),
              version ? `Hermesh ${version}` : "",
            ]
              .filter(Boolean)
              .join(" · ")}
          </span>
        </div>
      </div>
      <Problems m={m} />
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
        {m.local && (
          <p className="set-note">
            <button className="link" onClick={() => void local.checkAgents({}).catch(() => {})}>{t("machines.recheck")}</button>
          </p>
        )}
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
      {/* Turning Her on is signed with the root key, which a machine only does not hold. */}
      {!st.status?.machineOnly && (
        <div>
          <div className="fhead">{t("machines.her")}</div>
          <div className="fgroup">
            <HerRow m={m} />
          </div>
          <GrantLine m={m} />
        </div>
      )}
      {/* What macOS lets Her's desktop tools do here: the Mac's permissions (macOS checks hermeshd), not an assistant's. */}
      {m.local && m.her?.enabled && <ComputerUse machineId={m.machineId} />}
      <div className="set-acts">
        <button className="btn" onClick={rename}>{t("machines.rename")}</button>
        {/* What to send when something doesn't work; only this Mac's is at hand. */}
        {m.local && <button className="btn" onClick={copyDiagnostics}>{t("machines.copyDiagnostics")}</button>}
        {!m.local && !device && (
          <button className="btn danger push" onClick={remove}>{t("remove")}</button>
        )}
      </div>
      {device && (
        <p className="set-note">
          <Trans t={t} i18nKey="machines.deviceNote" values={{ name: device.label }} components={{ go: <button className="link" onClick={() => window.hermesh?.openSettings("account")} /> }} />
        </p>
      )}
    </div>
  );
}
