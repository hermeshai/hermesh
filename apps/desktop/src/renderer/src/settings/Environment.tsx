import { useEffect, useState } from "react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { RuntimeMode } from "@hermesh/proto/identity";
import type { Machine } from "@hermesh/proto/local";
import { SessionStatus } from "@hermesh/proto/session";
import { agoText, codingAgentTitle, dateOf, formatBytes, isActive, platformTitle } from "@hermesh/core";
import type { DaemonInfo } from "../../../shared/daemon";
import { i18n } from "../../../shared/i18n";
import { sortAgents } from "../AgentList";
import { useCheckAgents } from "../agents";
import { local } from "../api";
import { Problems } from "../health";
import { herDataKept } from "../her/environments";
import { Icon } from "../icons";
import { machineAnswers } from "../skew";
import { useNow, useStore, type Store } from "../store";
import { EnvHer } from "./EnvHer";
import { Projects } from "./Projects";
import { ProviderHistory } from "./ProviderMaintenance";
import { Provider, Providers } from "./Providers";
import { StayOnline } from "./StayOnline";
import { Storage, useWorkspaces } from "./Storage";
import { SRow } from "./common";

// An environment's page and what opens from it (docs/settings-environments.md §5.3): a machine
// of the owner's, or this Mac, which may not be one.

export function machineIcon(m?: Machine): string {
  if (!m || m.local) return "laptop";
  return m.status?.platform.startsWith("darwin") ? "mini" : "server";
}

/** "这台 Mac" for this Mac (one or not), else the machine's name. */
export function envTitle(t: TFunction<"settings">, m?: Machine): string {
  return !m || m.local ? t("thisMac") : m.machineName;
}

/** What the title bar says for an environment's item ("<id>/providers/codex": Codex). */
export function envItemTitle(t: TFunction<"settings">, item: string, m?: Machine): string {
  const [, sub, x] = item.split("/");
  if (sub === "providers" && x === "history") return t("maintenance.history");
  if (sub === "providers") return x ? codingAgentTitle(x) : t("env.providers");
  if (sub === "projects") return t("env.projects");
  if (sub === "her") return t("env.her");
  if (sub === "storage") return t("env.storage");
  return envTitle(t, m);
}

/** The tasks running or waiting for an answer there. */
export function runningOn(st: Store, machineId: string): number {
  return st.sessions.filter((x) => x.machineId === machineId && (isActive(x) || x.status === SessionStatus.WAITING_INPUT)).length;
}

/** Whether this Mac's background service runs, as the app manages it. */
export function useDaemon(): DaemonInfo | null {
  const [d, setD] = useState<DaemonInfo | null>(null);
  useEffect(() => {
    window.hermesh?.daemonState().then(setD);
    return window.hermesh?.onDaemonState(setD);
  }, []);
  return d;
}

/** + beside Environments: this Mac, while it isn't one, and another computer or server. */
export async function addEnvironmentMenu(st: Store, t: TFunction<"settings">) {
  const thisOne = !st.status?.localMachineEnabled;
  const id = await window.hermesh?.popupMenu([...(thisOne ? [{ id: "this", label: t("env.addThis") }] : []), { id: "other", label: t("env.addOther") }]);
  if (id === "this") await addThisMac(st);
  if (id === "other") st.set({ sheet: { kind: "addMachine" } });
}

/** Makes this Mac an environment (Touch ID: its machine is signed by the root key). */
export const addThisMac = (st: Store) => st.run(() => local.setLocalMachine({ enabled: true, machineName: st.status?.localMachineName ?? "" }));

/** A machine name as hermeshd's machine.CleanName makes it: lowercase letters, digits and -, at most 63 bytes; "" when nothing is left. */
export function cleanName(s: string): string {
  let n = s.trim().toLowerCase();
  if (n.endsWith(".local")) n = n.slice(0, -".local".length);
  n = n.replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
  return n.length > 63 ? n.slice(0, 63).replace(/^-+|-+$/g, "") : n;
}

/**
 * The environment page, or what opens from it: item is "<machine id>[/providers[/<agent>] |
 * /projects | /her | /storage]"; "this" while this Mac is not an environment.
 */
export function Environment({ item, go }: { item: string; go: (item: string) => void }) {
  const st = useStore();
  const [id, sub, x] = item.split("/");
  const m = st.machines.find((y) => y.machineId === id);
  if (!m) return <NotEnvironment />;
  if (sub === "providers" && x === "history") return <ProviderHistory m={m} />;
  if (sub === "providers" && x) return <Provider key={m.machineId + x} m={m} agent={x} />;
  if (sub === "providers") return <Providers m={m} open={(a) => go(`${id}/providers/${a}`)} />;
  if (sub === "projects") return <Projects m={m} />;
  if (sub === "her") return <EnvHer m={m} />;
  if (sub === "storage") return <Storage m={m} />;
  return <EnvironmentPage m={m} go={(s) => go(`${id}/${s}`)} />;
}

/** The pill under the name: online and what runs, offline since when, or this Mac's service stopped. */
function StatePill({ m }: { m: Machine }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(30_000);
  const daemon = useDaemon();
  const n = runningOn(st, m.machineId);
  const seen = dateOf(m.lastSeen);
  if (m.local && daemon?.manageable && daemon.state !== "running") {
    return (
      <span className="set-pill">
        <i />
        {t("env.stopped")} ·{" "}
        <button className="link" disabled={daemon.state !== "stopped" && daemon.state !== "unavailable"} onClick={() => void window.hermesh?.startDaemon()}>{t("env.start")}</button>
      </span>
    );
  }
  if (!m.online && !m.local) {
    return (
      <span className="set-pill">
        <i />
        {t("env.offlineSince", { ago: seen ? agoText(seen, now) : "—" })}
      </span>
    );
  }
  return (
    <span className="set-pill on">
      <i />
      {t("env.online")}
      {n > 0 && (
        <>
          {" · "}
          <button className="link" onClick={() => (window.hermesh?.showInMain({ machine: m.machineId }), window.close())}>{t("env.running", { count: n })}</button>
        </>
      )}
    </span>
  );
}

function EnvironmentPage({ m, go }: { m: Machine; go: (sub: string) => void }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const live = m.online || m.local;
  const paused = m.status?.paused ?? false;
  const version = m.status?.version || m.version;
  const agents = sortAgents(m.status?.agents);
  const ready = agents.installed.filter((a) => a.loggedIn).length;
  const unsigned = agents.installed.length - ready;
  const projects = st.projects.filter((p) => p.machineId === m.machineId).length;
  const device = m.local ? undefined : st.devices.find((d) => d.machineId === m.machineId && !d.thisDevice);
  const storage = machineAnswers(m, "ListWorkspaces");
  const ws = useWorkspaces(m, true, live && storage !== false);
  useCheckAgents(m.local);

  const setPaused = (on: boolean) =>
    st.run(() => local.setMachinePaused({ machineId: m.machineId, paused: on }), t(on ? "machines.pausedToast" : "machines.resumedToast", { name: m.machineName }));
  const copyDiagnostics = () =>
    st.run(async () => {
      const { text } = await local.getDiagnostics({});
      await navigator.clipboard.writeText(text);
    }, t("machines.diagnosticsCopied"));
  const disable = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("env.disableTitle"),
        message: t("env.disableMessage"),
        button: t("env.disableButton"),
        run: () => local.setLocalMachine({ enabled: false, machineName: st.status?.localMachineName ?? "" }),
        done: t("env.disabled"),
      },
    });
  // A trusted device's own machine goes with the device (docs/settings-environments.md, D13).
  const remove = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: device ? t("removeDeviceTitle", { name: device.label }) : t("removeMachineTitle", { name: m.machineName }),
        message: device ? t("account.removeDeviceMessage") : t("env.removeMessage"),
        button: t("removeButton"),
        run: (eraseHer) => (device ? local.removeDevice({ keyId: device.keyId, eraseHer }) : local.removeMachine({ machineId: m.machineId, eraseHer })),
        done: t("removed", { name: device?.label ?? m.machineName }),
        herData: m.her?.enabled || herDataKept(m) ? { name: m.machineName, offline: !m.online } : undefined,
      },
    });
  const storageValue = !live ? "—" : storage === false ? t("env.needsUpdate") : ws.res ? formatSize(ws.res.workspaces.reduce((n, w) => n + Number(w.sizeBytes), 0)) : "";

  return (
    <div className="form set-env">
      <div className="set-hero">
        <span className="set-tile blue xl"><Icon name={machineIcon(m)} /></span>
        <b>{m.machineName}</b>
        <span className="sub">
          {[m.local ? t("thisMac") : device ? t("machines.trustedDevice") : "", platformTitle(m.status?.platform ?? ""), version ? `Hermesh ${version}` : ""].filter(Boolean).join(" · ")}
        </span>
        <StatePill m={m} />
      </div>
      <Problems m={m} />
      {paused && live && (
        <div className="set-banner" role="status">
          <Icon name="pause" />
          <div className="ft">
            <div>{t("env.pausedBanner")}</div>
            <div className="fdesc">{t("env.pausedDesc")}</div>
          </div>
          <button className="btn" onClick={() => setPaused(false)}>{t("env.resume")}</button>
        </div>
      )}
      <div>
        <div className="fgroup">
          <NameRow m={m} />
          <MaxModeRow m={m} />
        </div>
        <p className="set-foot">{t("env.nameNote")}</p>
      </div>
      {m.local && <StayOnline />}
      <div className="fgroup" role="list" aria-label={m.machineName}>
        <EntryRow icon="code" tone="slate" title={t("env.providers")} off={!live} onClick={() => go("providers")}
          value={!live ? "—" : <>{t("env.available", { count: ready })}{unsigned > 0 && <span className="warn"> · {t("env.notSignedIn", { count: unsigned })}</span>}</>} />
        <EntryRow icon="folder" tone="sage" title={t("env.projects")} off={!live} value={live ? String(projects) : "—"} onClick={() => go("projects")} />
        {!st.status?.machineOnly && <EntryRow icon="her" tone="plum" title={t("env.her")} value={m.her?.enabled ? t("env.herOn") : t("env.herOff")} onClick={() => go("her")} />}
        <EntryRow icon="disk" tone="sand" title={t("env.storage")} off={!live} value={storageValue} onClick={() => go("storage")} />
      </div>
      <div className="set-acts set-env-acts">
        {m.local ? (
          !st.status?.machineOnly && <button className="btn" onClick={disable}>{t("env.disable")}</button>
        ) : (
          <button className="btn" onClick={remove}>{t("env.remove")}</button>
        )}
        <span className="push" />
        {m.local && <button className="btn" onClick={copyDiagnostics}>{t("env.copyDiagnostics")}</button>}
        <button className="btn" disabled={!live} onClick={() => setPaused(!paused)}>{t(paused ? "env.resumeTasks" : "env.pause")}</button>
      </div>
    </div>
  );
}

/** A size as this computer's file manager counts it: it is read here. */
export const formatSize = (bytes: number | bigint) => formatBytes(bytes, window.hermesh?.platform ?? "darwin");

/** One way into what an environment has: its tile, name, a value and ›. */
function EntryRow({ icon, tone, title, value, off, onClick }: { icon: string; tone: string; title: string; value: React.ReactNode; off?: boolean; onClick: () => void }) {
  return (
    <button className="frow go" role="listitem" aria-label={title} disabled={off} onClick={onClick}>
      <span className={`set-tile ${tone}`}><Icon name={icon} /></span>
      <div className="ft">
        <div className="fn">{title}</div>
      </div>
      <span className="fv-side">{value}</span>
      <Icon name="chevR" className="sm chev" />
    </button>
  );
}

/** The name, changed in place: saved on Enter or leaving the field, as hermeshd keeps it. */
function NameRow({ m }: { m: Machine }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const [value, setValue] = useState(m.machineName);
  useEffect(() => setValue(m.machineName), [m.machineName]);
  const name = cleanName(value);
  const hint = !value.trim() || name === value.trim() ? "" : name ? t("machines.nameSavedAs", { name }) : t("machines.nameUnusable");
  const save = () => {
    if (!name) return setValue(m.machineName);
    if (name === m.machineName) return setValue(m.machineName);
    void st.run(() => local.renameMachine({ machineId: m.machineId, machineName: name })).then((ok) => ok || setValue(m.machineName));
  };
  return (
    <>
      <div className="frow">
        <label className="ft" htmlFor="env-name">{t("env.name")}</label>
        <input
          id="env-name"
          className="field mono set-name"
          value={value}
          spellCheck={false}
          disabled={!m.online && !m.local}
          onChange={(e) => setValue(e.target.value)}
          onBlur={save}
          onKeyDown={(e) => {
            if (e.key === "Enter") (e.target as HTMLInputElement).blur();
            if (e.key === "Escape") (e.preventDefault(), setValue(m.machineName));
          }}
        />
      </div>
      {hint && <div className={`frow set-hint ${name ? "" : "warn"}`}>{hint}</div>}
    </>
  );
}

/** The machine's own limit on what its sessions may do (SetMaxRuntimeMode, docs/thread-composer.md §5.4). */
const maxModes = [
  [RuntimeMode.UNSPECIFIED, "settings:machines.maxModeNone"],
  [RuntimeMode.SUPERVISED, "thread:mode.supervised"],
  [RuntimeMode.AUTO_EDITS, "thread:mode.autoEdits"],
  [RuntimeMode.AUTO, "thread:mode.auto"],
  [RuntimeMode.FULL_ACCESS, "thread:mode.fullAccess"],
] as const;

function MaxModeRow({ m }: { m: Machine }) {
  const st = useStore();
  const { t } = useTranslation("settings");
  const now = m.status?.maxRuntimeMode ?? RuntimeMode.UNSPECIFIED;
  const title = (mode: RuntimeMode): string => i18n.t(maxModes.find(([x]) => x === mode)?.[1] ?? "settings:machines.maxModeNone");
  const pick = async () => {
    const id = await window.hermesh?.popupMenu(maxModes.map(([mode, key]) => ({ id: String(mode), label: i18n.t(key), checked: mode === now })));
    if (id == null || Number(id) === now) return;
    const mode = Number(id) as RuntimeMode;
    void st.run(() => local.setMachineMaxRuntimeMode({ machineId: m.machineId, mode }), t("machines.maxModeSet", { name: m.machineName, mode: title(mode) }));
  };
  return (
    <SRow title={t("machines.maxMode")} desc={t("machines.maxModeDesc")}>
      <button className="btn sm" data-testid="machine-max-mode" disabled={!m.online && !m.local} onClick={pick}>
        {title(now)}
      </button>
    </SRow>
  );
}

/** This Mac while it is not an environment: becoming one, and staying online all the same. */
function NotEnvironment() {
  const { t } = useTranslation("settings");
  const st = useStore();
  return (
    <div className="form set-env">
      <div className="set-hero">
        <span className="set-tile blue xl"><Icon name="laptop" /></span>
        <b>{t("env.notEnv")}</b>
        <span className="sub set-hero-desc">{t("env.notEnvDesc")}</span>
        <button className="btn primary set-hero-btn" onClick={() => void addThisMac(st)}>{t("env.addThisButton")}</button>
      </div>
      <StayOnline />
    </div>
  );
}
