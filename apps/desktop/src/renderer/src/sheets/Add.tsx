import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { PairingKind, Workspace } from "@hermesh/proto/identity";
import { folderName, grantsReaching, isAbsolutePath, projectIdFromFolder } from "@hermesh/core";
import type { ListFoldersResponse, Repo } from "@hermesh/proto/machine";
import { Mark } from "@hermesh/ui";
import { local } from "../api";
import { Icon } from "../icons";
import { i18n } from "../../../shared/i18n";
import { errorText, useStore } from "../store";
import { ErrorLine, useAction, useWaitForPairings } from "./common";

const releases = "https://github.com/hermeshai/hermesh/releases/latest/download";

/** The command that makes a computer a machine of this account: install.sh's, or install.ps1's in PowerShell. */
function addMachineCommand(windows: boolean, address: string) {
  return windows
    ? `& ([scriptblock]::Create((irm ${releases}/install.ps1))) ${address}`
    : `curl -fsSL ${releases}/install.sh | sh -s -- ${address}`;
}

/** Where that Mac or PC gets the app: the site picks the build for it. */
const downloadPage = "hermesh.io/download";

type MachineOs = "mac" | "windows" | "linux";

/**
 * Add Machine: pick that machine's system, then how: the app (a Mac or a
 * Windows PC, which then signs in to this account or joins this server) or
 * one command in its terminal (Linux has only this). Its request then opens
 * the approval here, with the code it shows compared in the signer's window.
 */
export function AddMachineSheet() {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [os, setOs] = useState<MachineOs>("mac");
  const [app, setApp] = useState(true);
  const address = st.status?.connectAddress ?? "";
  const windows = os === "windows";
  const viaApp = app && os !== "linux";
  const command = addMachineCommand(windows, address);
  const [before] = useState(() => new Set(st.pairings.map((p) => p.code)));
  // The app joins as a machine only, or as a trusted device (which is a machine too).
  const arrived = st.pairings.find((p) => (p.kind === PairingKind.MACHINE || p.kind === PairingKind.DEVICE) && !before.has(p.code))?.code;
  useWaitForPairings();
  const { set } = st;
  useEffect(() => {
    if (arrived) set({ sheet: { kind: "pairing", code: arrived } });
  }, [arrived, set]);
  const copy = (text: string) => {
    navigator.clipboard.writeText(text).catch(() => {});
    st.show(t("copied"));
  };
  const copyButton = (text: string) => (
    <button className="btn sm" onClick={() => copy(text)}>
      <Icon name="copy" />
      {t("copy")}
    </button>
  );
  return (
    <>
      <h3 id="sheet-title">{t("addMachine.title")}</h3>
      <p className="sub">{t("addMachine.sub")}</p>
      <div className="os-pick" role="group" aria-label={t("addMachine.system")}>
        {(["mac", "windows", "linux"] as const).map((o) => (
          <button key={o} type="button" className={o === os ? "os on" : "os"} aria-pressed={o === os} onClick={() => setOs(o)}>
            <OsIcon os={o} />
            {o === "mac" ? "Mac" : o === "windows" ? "Windows" : "Linux"}
          </button>
        ))}
      </div>
      <div className="seg" role="group" aria-label={t("addMachine.how")}>
        <button type="button" className={viaApp ? "on" : ""} aria-pressed={viaApp} disabled={os === "linux"} onClick={() => setApp(true)}>
          {t("addMachine.app")}
          {os === "linux" && <span className="tag">{t("addMachine.noApp")}</span>}
        </button>
        <button type="button" className={viaApp ? "" : "on"} aria-pressed={!viaApp} onClick={() => setApp(false)}>{t("addMachine.command")}</button>
      </div>
      <div className="am-panel">
        {viaApp ? (
          <>
            <div className="am-step">
              <span className="am-n">1</span>
              {t(windows ? "addMachine.openOnPc" : "addMachine.openOnMac")}
              <span className="am-chip sel-text">{downloadPage}</span>
              {copyButton(`https://${downloadPage}`)}
            </div>
            <div className="am-step">
              <span className="am-n">2</span>
              {st.status?.hosted ? (
                t("addMachine.signIn")
              ) : (
                <>
                  {t("addMachine.join")}
                  <span className="am-chip sel-text">{address}</span>
                  {copyButton(address)}
                </>
              )}
            </div>
          </>
        ) : (
          <>
            <div className="am-cmdh">
              {t(windows ? "addMachine.powershell" : "addMachine.terminal")}
              {copyButton(command)}
            </div>
            <div className="am-code sel-text">{command}</div>
          </>
        )}
      </div>
      <div className="sheet-foot">
        <span className="am-wait left">
          <Mark size={20} variant="wait" />
          {t("addMachine.waiting")}
        </span>
        <button className="link" onClick={() => st.set({ sheet: { kind: "enterCode" } })}>{t("addMachine.enterCode")}</button>
        <button className="btn" onClick={() => st.set({ sheet: null })}>{t("done")}</button>
      </div>
    </>
  );
}

function OsIcon({ os }: { os: MachineOs }) {
  if (os === "windows")
    return (
      <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
        <rect x="3" y="3" width="8.4" height="8.4" rx="1" />
        <rect x="12.6" y="3" width="8.4" height="8.4" rx="1" />
        <rect x="3" y="12.6" width="8.4" height="8.4" rx="1" />
        <rect x="12.6" y="12.6" width="8.4" height="8.4" rx="1" />
      </svg>
    );
  if (os === "linux")
    return (
      <svg viewBox="0 0 24 24" fill="currentColor" fillRule="evenodd" aria-hidden="true">
        <path d="M12 2.5c-2.6 0-4 2-4 4.8 0 1.4-.6 2.6-1.6 4C5 13.1 4 15 4 17c0 1.2.5 2 1.4 2.4L7 20.5c.6.4 1.6 1 2.7 1h4.6c1.1 0 2.1-.6 2.7-1l1.6-1.1c.9-.4 1.4-1.2 1.4-2.4 0-2-1-3.9-2.4-5.7-1-1.4-1.6-2.6-1.6-4 0-2.8-1.4-4.8-4-4.8zM8.6 15.6a3.4 4 0 1 0 6.8 0a3.4 4 0 1 0-6.8 0zM9.9 6.7a.9 1.1 0 1 0 1.8 0a.9 1.1 0 1 0-1.8 0zM12.3 6.7a.9 1.1 0 1 0 1.8 0a.9 1.1 0 1 0-1.8 0z" />
      </svg>
    );
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <path d="M16.4 12.6c0-2.2 1.8-3.3 1.9-3.3-1-1.5-2.6-1.7-3.2-1.7-1.4-.1-2.6.8-3.3.8-.7 0-1.7-.8-2.9-.8-1.5 0-2.8.9-3.6 2.2-1.5 2.7-.4 6.6 1.1 8.8.7 1.1 1.6 2.2 2.7 2.2 1.1 0 1.5-.7 2.8-.7 1.3 0 1.7.7 2.9.7 1.2 0 1.9-1.1 2.6-2.1.8-1.2 1.2-2.4 1.2-2.5 0 0-2.3-.9-2.2-3.6zM14.2 6.1c.6-.7 1-1.8.9-2.8-.9 0-1.9.6-2.5 1.3-.6.6-1 1.7-.9 2.7.9.1 1.9-.5 2.5-1.2z" />
    </svg>
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
 * agent only, when added under it in settings). On this Mac it can be picked;
 * on any machine its recent repositories are listed and its folders browsed
 * (ListRepos, ListFolders: read when asked, never kept). Before adding, it
 * says which agents' access would reach the new project at once
 * (docs/thread-composer.md §3.2). then: told the project once added.
 */
export function AddProjectSheet({ machineId, agent, then }: { machineId?: string; agent?: string; then?: (projectId: string, worktree: boolean) => void }) {
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
  const [browsing, setBrowsing] = useState<string | null>(null);
  const act = useAction();
  const machine = online.find((m) => m.machineId === mid);
  const here = machine?.local ?? false;
  // The machine's recent repositories, read when the sheet opens on it (not those already projects).
  const [repos, setRepos] = useState<Repo[] | null>(null);
  useEffect(() => {
    if (!mid) return;
    let gone = false;
    setRepos(null);
    local.listMachineRepos({ machineId: mid }).then((r) => !gone && setRepos(r.repos), () => !gone && setRepos([]));
    return () => {
      gone = true;
    };
  }, [mid]);
  const added = new Set(st.projects.filter((p) => p.machineId === mid).map((p) => p.path));
  // The machine refuses a folder inside a repository, naming the repository's top (ValidateRepo).
  const insideRepo = /is inside repository (.+); register the repository root/.exec(act.error)?.[1] ?? "";
  const reaching = grantsReaching(st.clients, mid, id.trim());
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
  // A repository chosen from the list or the folder browser: worktrees when it is one.
  const chooseFolder = (p: string, repo: boolean) => {
    choosePath(p);
    setWorktree(repo);
    setBrowsing(null);
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
      then?.(projectId, worktree);
    }
  };

  if (browsing !== null && machine)
    return <FolderBrowser machine={machine.machineId} machineName={machine.machineName} start={browsing} onBack={() => setBrowsing(null)} onChoose={chooseFolder} />;

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
          {here ? (
            <button type="button" className="btn sm" onClick={pick}>{t("addProject.choose")}</button>
          ) : (
            <button type="button" className="btn sm" onClick={() => setBrowsing("~")}>{t("addProject.browse")}</button>
          )}
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
      {!path.trim() && mid && (
        <>
          <div className="pj-sec">{t("addProject.recent")}</div>
          <div className="pj-box" data-testid="add-project-recent">
            {repos === null ? (
              <div className="pj-empty">{t("addProject.finding")}</div>
            ) : (
              repos
                .filter((r) => !added.has(r.path))
                .slice(0, 5)
                .map((r) => (
                  <button key={r.path} type="button" className="pj-row" onClick={() => chooseFolder(r.path, true)}>
                    <Icon name="folder" />
                    <span className="pj-path">{r.path}</span>
                    {r.branch && (
                      <span className="pj-val">
                        <Icon name="branch" className="sm" />
                        {r.branch}
                      </span>
                    )}
                  </button>
                ))
            )}
            <button type="button" className="pj-row" onClick={() => setBrowsing("~")}>
              <Icon name="search" />
              <span>{t("addProject.browseOther")}</span>
              <span className="pj-val"><Icon name="chevR" className="sm" /></span>
            </button>
          </div>
        </>
      )}
      {id.trim() !== "" && reaching.length > 0 && (
        <p className="sheet-note" data-testid="add-project-reach">
          {[
            reaching.some((g) => g.all) && t("addProject.reachAll", { names: names(reaching.filter((g) => g.all).map((g) => g.client)) }),
            reaching.some((g) => !g.all) && t("addProject.reachNamed", { names: names(reaching.filter((g) => !g.all).map((g) => g.client)), project: id.trim() }),
          ]
            .filter(Boolean)
            .join(" ")}
        </p>
      )}
      {insideRepo && path.trim() !== "" ? (
        <p className="sheet-note" data-testid="add-project-inside-repo">
          {t("addProject.insideRepo", { root: insideRepo })}{" "}
          <button type="button" className="link" onClick={() => (choosePath(insideRepo), setWorktree(true))}>{t("addProject.useRoot")}</button>
        </p>
      ) : (
        <ErrorLine text={act.error} />
      )}
      <div className="sheet-foot">
        <button type="button" className="btn" onClick={() => st.set({ sheet: null })}>{t("cancel")}</button>
        <button type="submit" className="btn primary" disabled={act.busy || !mid || !id.trim() || !isAbsolutePath(path.trim(), platform)}>
          {act.busy ? t("addProject.adding") : t("addProject.add")}
        </button>
      </div>
    </form>
  );
}

/** Agents by name, as the lists name them (Her by hers). */
function names(clients: { label: string; herMachineId: string }[]): string {
  const sep = i18n.language.startsWith("zh") ? "、" : ", ";
  return [...new Set(clients.map((c) => (c.herMachineId ? "Her" : c.label)))].join(sep);
}

/**
 * A machine's folders, one level at a time (ListFolders, as the iPhone's
 * Add a Project browses): the way back up, its folders (a repository's
 * branch beside it), hidden ones on request, a path typed in. A folder
 * macOS protects asks before it is opened; the home folder can't be chosen.
 */
function FolderBrowser({ machine, machineName, start, onBack, onChoose }: {
  machine: string;
  machineName: string;
  start: string;
  onBack: () => void;
  onChoose: (path: string, repo: boolean) => void;
}) {
  const { t } = useTranslation("sheets");
  const [path, setPath] = useState(start);
  const [hidden, setHidden] = useState(false);
  const [shown, setShown] = useState<ListFoldersResponse | null>(null);
  const [error, setError] = useState("");
  const [typing, setTyping] = useState(false);
  const [asking, setAsking] = useState<string | null>(null);
  const [reading, setReading] = useState(false);
  // A new folder here: its name, and whether it starts as a git repository (CreateFolder).
  const [making, setMaking] = useState<{ name: string; git: boolean } | null>(null);
  const make = useAction();
  useEffect(() => {
    let gone = false;
    setError("");
    // Shown only when a read takes a moment, so a quick one doesn't flash.
    const slow = setTimeout(() => !gone && setReading(true), 300);
    local.listMachineFolders({ machineId: machine, path, hidden }).then(
      (r) => !gone && (setShown(r), setTyping(false)),
      (e) => !gone && setError(errorText(e)),
    ).finally(() => (clearTimeout(slow), !gone && setReading(false)));
    return () => {
      gone = true;
      clearTimeout(slow);
    };
  }, [machine, path, hidden]);
  const at = shown?.path ?? path;
  const home = at === "~";
  const parts = at === "~" ? ["~"] : at.startsWith("~/") ? ["~", ...at.slice(2).split("/")] : at.split(/(?<=.)[\\/]/);
  const leaf = folderName(at) || at;
  return (
    <div className="pj-browse" data-testid="folder-browser">
      <h3 id="sheet-title">{t("addProject.title")}</h3>
      <div className="pj-crumbs">
        {typing ? (
          <input
            className="field mono"
            autoFocus
            defaultValue={at === "~" ? "~/" : `${at}/`}
            aria-label={t("addProject.typePath")}
            onKeyDown={(e) => {
              if (e.key === "Enter") setPath(e.currentTarget.value.trim().replace(/[\\/]+$/, "") || "~");
              else if (e.key === "Escape") (e.stopPropagation(), setTyping(false));
            }}
          />
        ) : (
          <>
            {parts.map((p, i) => (
              <span key={i} className="pj-crumb">
                {i > 0 && <Icon name="chevR" className="sm" />}
                <button
                  type="button"
                  className={i === parts.length - 1 ? "cur" : ""}
                  onClick={() => setPath(parts[0] === "~" ? (i === 0 ? "~" : `~/${parts.slice(1, i + 1).join("/")}`) : parts.slice(0, i + 1).join("/"))}
                >
                  {p === "~" ? t("addProject.home") : p}
                </button>
              </span>
            ))}
            <button type="button" className="link pj-type" onClick={() => setTyping(true)}>{t("addProject.typePath")}</button>
          </>
        )}
      </div>
      {making && (
        <form
          className="pj-make"
          data-testid="new-folder"
          onSubmit={async (e) => {
            e.preventDefault();
            const name = making.name.trim();
            const ok = await make.go(() => local.createMachineFolder({ machineId: machine, parent: at, name, gitInit: making.git }));
            if (ok) (setMaking(null), setPath(at === "~" ? `~/${name}` : `${at}/${name}`));
          }}
        >
          <Icon name="folder" />
          <input className="field" autoFocus aria-label={t("addProject.folderName")} placeholder={t("addProject.folderName")} value={making.name} onChange={(e) => setMaking({ ...making, name: e.target.value })} />
          <label className="pj-git">
            <input type="checkbox" checked={making.git} onChange={(e) => setMaking({ ...making, git: e.target.checked })} />
            {t("addProject.gitInit")}
          </label>
          <button type="submit" className="btn sm primary" disabled={make.busy || !making.name.trim()}>{t("addProject.create")}</button>
        </form>
      )}
      {making && <ErrorLine text={make.error} />}
      <div className="pj-box pj-list">
        {error ? (
          <div className="pj-empty">{error}</div>
        ) : reading && !shown ? (
          <div className="pj-empty">{t("addProject.reading", { machine: machineName })}</div>
        ) : (
          <>
            {shown?.folders.map((f) =>
              asking === f.path ? (
                <div key={f.path} className="pj-row ask">
                  <Icon name="folder" />
                  <span>{t("addProject.protected")}</span>
                  <span className="pj-val">
                    <button type="button" className="btn sm" onClick={() => (setAsking(null), setPath(f.path))}>{t("addProject.open")}</button>
                  </span>
                </div>
              ) : (
                <button key={f.path} type="button" className="pj-row" onClick={() => (f.protected ? setAsking(f.path) : setPath(f.path))}>
                  <Icon name="folder" />
                  <span>{f.name}</span>
                  <span className="pj-val">
                    {f.repo && (
                      <>
                        <Icon name="branch" className="sm" />
                        {f.branch}
                      </>
                    )}
                    <Icon name="chevR" className="sm" />
                  </span>
                </button>
              ),
            )}
            {shown && shown.folders.length === 0 && <div className="pj-empty">{t("addProject.noFolders")}</div>}
          </>
        )}
      </div>
      <p className="pj-about">{!home && shown && (shown.repo ? t("addProject.isRepo", { branch: shown.branch }) : t("addProject.isFolder", { machine: machineName }))}</p>
      <div className="sheet-foot">
        <label className="pj-hidden">
          <input type="checkbox" checked={hidden} onChange={(e) => setHidden(e.target.checked)} />
          {t("addProject.hidden")}
        </label>
        <button type="button" className="btn" disabled={!shown || !!error || !!making} onClick={() => setMaking({ name: "", git: true })}>{t("addProject.newFolder")}</button>
        <button type="button" className="btn" onClick={onBack}>{t("addProject.back")}</button>
        <button type="button" className="btn primary" disabled={home || !shown || !!error} onClick={() => shown && onChoose(shown.path, shown.repo)}>
          {home ? t("addProject.openOne") : t("addProject.chooseThis", { name: leaf })}
        </button>
      </div>
    </div>
  );
}
