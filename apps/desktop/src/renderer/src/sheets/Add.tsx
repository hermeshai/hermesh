import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { PairingKind, Workspace } from "@hermesh/proto/identity";
import { folderName, isAbsolutePath, projectIdFromFolder } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local } from "../api";
import { Icon } from "../icons";
import { useStore } from "../store";
import { ErrorLine, useAction, useWaitForPairings } from "./common";

const releases = "https://github.com/hermeshai/hermesh/releases/latest/download";

/** The command that makes a computer a machine of this account: install.sh's, or install.ps1's in PowerShell. */
function addMachineCommand(windows: boolean, address: string) {
  return windows
    ? `& ([scriptblock]::Create((irm ${releases}/install.ps1))) ${address}`
    : `curl -fsSL ${releases}/install.sh | sh -s -- ${address}`;
}

/**
 * Add Machine: one command on that machine (a Mac or Linux one, or a
 * Windows one's PowerShell). Its request then opens the approval here, with
 * the code its terminal shows compared in the signer's window.
 */
export function AddMachineSheet() {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [windows, setWindows] = useState(false);
  const command = addMachineCommand(windows, st.status?.connectAddress ?? "");
  const [before] = useState(() => new Set(st.pairings.map((p) => p.code)));
  const arrived = st.pairings.find((p) => p.kind === PairingKind.MACHINE && !before.has(p.code))?.code;
  useWaitForPairings();
  const { set } = st;
  useEffect(() => {
    if (arrived) set({ sheet: { kind: "pairing", code: arrived } });
  }, [arrived, set]);
  const copy = () => {
    navigator.clipboard.writeText(command).catch(() => {});
    st.show(t("copied"));
  };
  return (
    <>
      <h3 id="sheet-title">{t("addMachine.title")}</h3>
      <p className="sub">{t("addMachine.sub")}</p>
      <div className="fhead">{t("addMachine.step1")}</div>
      <div className="seg" role="group" aria-label={t("addMachine.system")} style={{ marginBottom: 8 }}>
        <button type="button" className={windows ? "" : "on"} aria-pressed={!windows} onClick={() => setWindows(false)}>{t("addMachine.unix")}</button>
        <button type="button" className={windows ? "on" : ""} aria-pressed={windows} onClick={() => setWindows(true)}>Windows</button>
      </div>
      <div className="msgbox sel-text" style={{ paddingRight: 80 }}>
        {command}
        <button className="btn sm" onClick={copy}>
          <Icon name="copy" />
          {t("copy")}
        </button>
      </div>
      <p className="sub" style={{ marginTop: 8 }}>{t(windows ? "addMachine.hintWindows" : "addMachine.hint")}</p>
      <div className="fhead" style={{ marginTop: 6 }}>{t("addMachine.step2")}</div>
      <div className="waitline">
        <Mark size={40} variant="wait" />
        <div>
          <b>{t("addMachine.waiting")}</b>
          <span>{t("addMachine.waitingDetail")}</span>
        </div>
      </div>
      <div className="sheet-foot">
        <button className="link left" onClick={() => st.set({ sheet: { kind: "enterCode" } })}>{t("addMachine.enterCode")}</button>
        <button className="btn" onClick={() => st.set({ sheet: null })}>{t("done")}</button>
      </div>
    </>
  );
}

/**
 * Add Trusted Device: another Mac joins the account and can approve too. Its join
 * request pops up here; the owner compares the code, approves, and types
 * nothing: the new Mac takes the two digits shown here.
 */
export function AddDeviceSheet() {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const addr = st.status?.connectAddress ?? "";
  const [before] = useState(() => new Set(st.pairings.map((p) => p.code)));
  const arrived = st.pairings.find((p) => p.kind === PairingKind.DEVICE && !before.has(p.code))?.code;
  useWaitForPairings();
  const { set } = st;
  useEffect(() => {
    if (arrived) set({ sheet: { kind: "pairing", code: arrived } });
  }, [arrived, set]);
  const copy = () => {
    navigator.clipboard.writeText(addr).catch(() => {});
    st.show(t("copied"));
  };
  return (
    <>
      <h3 id="sheet-title">{t("addDevice.title")}</h3>
      <p className="sub">{t("addDevice.sub")}</p>
      <div className="fhead">{t("addDevice.step1")}</div>
      {st.status?.hosted ? (
        <p className="sub">{t("addDevice.hosted")}</p>
      ) : (
        <>
          <p className="sub">{t("addDevice.selfHosted")}</p>
          <div className="msgbox sel-text" style={{ paddingRight: 80 }}>
            {addr}
            <button className="btn sm" onClick={copy}>
              <Icon name="copy" />
              {t("copy")}
            </button>
          </div>
        </>
      )}
      <div className="fhead" style={{ marginTop: 14 }}>{t("addDevice.step2")}</div>
      <div className="waitline">
        <Mark size={40} variant="wait" />
        <div>
          <b>{t("addDevice.waiting")}</b>
          <span>{t("addDevice.waitingDetail")}</span>
        </div>
      </div>
      <div className="sheet-foot">
        <button className="btn" onClick={() => st.set({ sheet: null })}>{t("done")}</button>
      </div>
    </>
  );
}

/**
 * A project: a directory on an online machine where agents may work (for
 * agent only, when added under it in settings). On this Mac it can be picked.
 */
export function AddProjectSheet({ machineId, agent }: { machineId?: string; agent?: string }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  // This Mac adds to itself in process, online or not.
  const online = st.machines.filter((m) => m.online || m.local);
  const [chosen, setMid] = useState(machineId);
  // The one asked for or chosen, else this Mac, else any (also once the list arrives).
  const mid = (online.find((m) => m.machineId === chosen) ?? online.find((m) => m.local) ?? online[0])?.machineId ?? "";
  const [path, setPath] = useState("");
  const [id, setId] = useState("");
  const [worktree, setWorktree] = useState(true);
  const [base, setBase] = useState("");
  const act = useAction();
  const machine = online.find((m) => m.machineId === mid);
  const here = machine?.local ?? false;
  const platform = machine?.status?.platform ?? "";
  const taken = new Set(st.projects.filter((p) => p.machineId === mid).map((p) => p.projectId));

  const choosePath = (p: string) => {
    setPath(p);
    const folder = folderName(p);
    setId(folder ? projectIdFromFolder(folder, taken) : "");
  };
  const pick = async () => {
    const p = await window.hermesh?.pickDirectory();
    if (p) choosePath(p);
  };
  const add = async (e: FormEvent) => {
    e.preventDefault();
    const projectId = id.trim();
    const ok = await act.go(() =>
      local.addProject({
        machineId: mid,
        projectId,
        path: path.trim(),
        workspace: worktree ? Workspace.WORKTREE : Workspace.IN_PLACE,
        defaultBaseRef: worktree ? base.trim() : "",
        agents: agent ? [agent] : [],
      }),
    );
    if (ok) {
      st.set({ sheet: null });
      st.show(t("addProject.added", { name: projectId }));
      st.refresh();
    }
  };

  return (
    <form onSubmit={add}>
      <h3 id="sheet-title">{t("addProject.title")}</h3>
      <p className="sub">{t("addProject.sub")}</p>
      <div className="fgroup">
        <div className="frow">
          <span className="fl">{t("addProject.machine")}</span>
          <span className="fv">
            {online.length ? (
              <div className="seg" role="group" aria-label={t("addProject.machine")}>
                {online.map((m) => (
                  <button type="button" key={m.machineId} className={m.machineId === mid ? "on" : ""} aria-pressed={m.machineId === mid} onClick={() => setMid(m.machineId)}>
                    {m.machineName}
                  </button>
                ))}
              </div>
            ) : (
              t("addProject.noMachines")
            )}
          </span>
        </div>
        <div className="frow">
          <label className="fl" htmlFor="pj-path">{t("addProject.folder")}</label>
          <span className="fv">
            <input id="pj-path" className="field mono" style={{ width: 260 }} autoFocus placeholder={here ? t("addProject.folderPlaceholder") : platform.startsWith("windows/") ? "C:\\code\\my-app" : "/home/me/code/my-app"} value={path} onChange={(e) => choosePath(e.target.value)} />
          </span>
          {here && <button type="button" className="btn sm" onClick={pick}>{t("addProject.choose")}</button>}
        </div>
        <div className="frow">
          <label className="fl" htmlFor="pj-id">{t("addProject.name")}</label>
          <span className="fv">
            <input id="pj-id" className="field mono" style={{ width: 260 }} value={id} onChange={(e) => setId(e.target.value)} />
          </span>
        </div>
        <div className="frow">
          <span className="fl">{t("addProject.workspace")}</span>
          <span className="fv">
            <div className="seg" role="group" aria-label={t("addProject.workspace")}>
              <button type="button" className={worktree ? "on" : ""} aria-pressed={worktree} onClick={() => setWorktree(true)}>{t("addProject.worktree")}</button>
              <button type="button" className={worktree ? "" : "on"} aria-pressed={!worktree} onClick={() => setWorktree(false)}>{t("addProject.inPlace")}</button>
            </div>
          </span>
        </div>
        {worktree && (
          <div className="frow">
            <label className="fl" htmlFor="pj-base">{t("addProject.baseRef")}</label>
            <span className="fv">
              <input id="pj-base" className="field mono" style={{ width: 260 }} placeholder={t("addProject.baseRefPlaceholder")} value={base} onChange={(e) => setBase(e.target.value)} />
            </span>
          </div>
        )}
      </div>
      <p className="sub" style={{ margin: "10px 0 0" }}>
        {worktree ? t("addProject.worktreeHint") : t("addProject.inPlaceHint")}
      </p>
      <ErrorLine text={act.error} />
      <div className="sheet-foot">
        <button type="button" className="btn" onClick={() => st.set({ sheet: null })}>{t("cancel")}</button>
        <button type="submit" className="btn primary" disabled={act.busy || !mid || !id.trim() || !isAbsolutePath(path.trim(), platform)}>
          {act.busy ? t("addProject.adding") : t("addProject.add")}
        </button>
      </div>
    </form>
  );
}
