import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Workspace } from "@hermesh/proto/identity";
import type { Project } from "@hermesh/proto/local";
import { local } from "./api";
import { Icon } from "./icons";
import { SidebarToggle } from "./MainWindow";
import { dayText, Usage, useUsage } from "./overview/Usage";
import { Working } from "./overview/Working";
import { useNow, useStore } from "./store";
import "./machineonly.css";
import "./overview/overview.css";

/** A project of this Mac, with a ⋯ menu to change its way of working or remove it. */
function ProjectRow({ p }: { p: Project }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const worktree = p.workspace !== Workspace.IN_PLACE;
  const more = async () => {
    const id = await window.hermesh?.popupMenu([
      { id: "worktree", label: t("prepare.worktreeItem"), checked: worktree },
      { id: "inPlace", label: t("prepare.inPlaceItem"), checked: !worktree },
      { separator: true },
      { id: "remove", label: t("machineOnly.removeProject") },
    ]);
    const workspace = id === "worktree" ? Workspace.WORKTREE : id === "inPlace" ? Workspace.IN_PLACE : undefined;
    if (workspace !== undefined && workspace !== (worktree ? Workspace.WORKTREE : Workspace.IN_PLACE)) {
      // Adding it again changes it.
      await st.run(() =>
        local.addProject({ machineId: p.machineId, projectId: p.projectId, path: p.path, workspace, defaultBaseRef: workspace === Workspace.IN_PLACE ? "" : p.defaultBaseRef, description: p.description, agents: p.agents }),
      );
    }
    if (id === "remove") await st.run(() => local.removeProject({ machineId: p.machineId, projectId: p.projectId }));
  };
  return (
    <div className="mo-row" role="group" aria-label={p.projectId}>
      <Icon name="folder" />
      <div className="mo-t">
        <b>{p.projectId}</b>
        <span className="mono" title={p.path}>
          {p.path} · {t(worktree ? "prepare.worktree" : "prepare.inPlace")}
        </span>
      </div>
      <button className="tb" aria-label={t("prepare.more")} onClick={more}>
        <Icon name="more" />
      </button>
    </div>
  );
}

/**
 * The overview of a Mac that joined its owner as a machine only (a work
 * Mac): this machine, its projects, what runs on it and its usage. Its
 * sessions are in the sidebar, as on a trusted device. From here it can
 * become a trusted device too, or leave.
 */
export function ThisMac() {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const now = useNow(30_000);
  const usage = useUsage(now);
  const [pick, setPick] = useState("");
  const status = st.status!;
  const m = st.machines.find((x) => x.local);
  const paused = m?.status?.paused ?? false;
  const id = m?.machineId ?? status.localMachineId;
  const state = !status.relayConnected ? t("machineOnly.offline") : paused ? t("machineOnly.paused") : t("machineOnly.online");
  const leave = () =>
    st.set({
      sheet: { kind: "confirm", title: t("machineOnly.leaveTitle"), message: t("machineOnly.leaveMessage"), button: t("machineOnly.leaveButton"), run: () => local.leave({}) },
    });

  return (
    <>
      <div className="ctool drag">
        <SidebarToggle />
        <div className="ctitle" />
      </div>
      <div className="cscroll">
        <div className="ov">
          <div className="ov-h">
            <div className="ov-t">
              <h1>{t("machineOnly.title")}</h1>
              <span>{dayText(now)}</span>
            </div>
          </div>
          <section className="mo-head" aria-label={t("machineOnly.title")}>
            <span className="mo-ic">
              <Icon name="laptop" />
            </span>
            <div className="mo-who">
              <b>{m?.machineName || status.localMachineName}</b>
              <span>
                <i className={`dotc ${status.relayConnected && !paused ? "on" : paused ? "warn" : ""}`} />
                {state} · {t("machineOnly.role")}
              </span>
            </div>
            <button
              className={`sw ${paused ? "" : "on"}`}
              role="switch"
              aria-checked={!paused}
              aria-label={t("machineOnly.accepting")}
              title={t("machineOnly.accepting")}
              disabled={!m}
              onClick={() => st.run(() => local.setMachinePaused({ machineId: id, paused: !paused }))}
            />
          </section>

          <section aria-label={t("machineOnly.projects")}>
            <div className="mo-sec">{t("machineOnly.projects")}</div>
            {st.projects.length ? (
              <div className="mo-list">
                {st.projects.map((p) => (
                  <ProjectRow key={p.projectId} p={p} />
                ))}
              </div>
            ) : (
              <p className="mo-empty">{t("machineOnly.noProjects")}</p>
            )}
            <button className="ag-add mo-add" disabled={!m} onClick={() => st.set({ sheet: { kind: "addProject", machineId: id } })}>
              <Icon name="plus" />
              {t("machineOnly.addProject")}
            </button>
          </section>

          <Working pick={pick} onClear={() => setPick("")} />
          <Usage now={now} res={usage} />

          <div className="mo-actions">
            <button className="link" onClick={() => st.set({ upgrading: true })}>{t("machineOnly.upgrade")}</button>
            <button className="link danger" onClick={leave}>{t("machineOnly.leave")}</button>
          </div>
        </div>
      </div>
    </>
  );
}
