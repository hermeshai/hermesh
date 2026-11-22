import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Workspace } from "@hermesh/proto/identity";
import type { Machine, Project } from "@hermesh/proto/local";
import { projectIdFromFolder } from "@hermesh/core";
import { local } from "../api";
import { Icon } from "../icons";
import { useStore } from "../store";

// An environment's projects (docs/settings-environments.md §5.6): the folders agents work in,
// for every agent provider there. A table, as Login Items: a row is selected, − removes it,
// + adds one; each row's way of working is a pop-up.

/** Adds the project again, as it is, working another way (adding again changes it). */
const rework = (p: Project, workspace: Workspace) =>
  local.addProject({ machineId: p.machineId, projectId: p.projectId, path: p.path, workspace, defaultBaseRef: workspace === Workspace.IN_PLACE ? "" : p.defaultBaseRef, description: p.description });

export function Projects({ m }: { m: Machine }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const projects = st.projects.filter((p) => p.machineId === m.machineId).sort((a, b) => a.projectId.localeCompare(b.projectId));
  const [selected, setSelected] = useState("");
  const live = m.online || m.local;
  const sel = projects.find((p) => p.projectId === selected);

  // This Mac's folder is picked; another machine's is typed in (or browsed) on a sheet.
  const add = async () => {
    if (!m.local) return st.set({ sheet: { kind: "addProject", machineId: m.machineId } });
    const path = await window.hermesh?.pickDirectory();
    if (!path) return;
    if (projects.some((p) => p.path === path)) return;
    const projectId = projectIdFromFolder(path.split(/[\\/]/).filter(Boolean).pop() ?? "", new Set(projects.map((x) => x.projectId)));
    await st.run(() => local.addProject({ machineId: m.machineId, projectId, path }));
    setSelected(projectId);
  };
  const remove = (p: Project) =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("projects.removeTitle", { name: p.projectId }),
        message: t("projects.removeMessage"),
        button: t("projects.removeButton"),
        run: async () => {
          await local.removeProject({ machineId: p.machineId, projectId: p.projectId });
          setSelected("");
        },
        done: t("projects.removed", { name: p.projectId }),
      },
    });

  return (
    <div className="form">
      <div>
        <div className="fgroup set-table" role="listbox" aria-label={t("env.projects")}>
          {projects.length === 0 && <div className="frow muted">{t("projects.none")}</div>}
          {projects.map((p) => (
            <div
              key={p.projectId}
              className={`frow ${p.projectId === selected ? "sel" : ""}`}
              role="option"
              aria-selected={p.projectId === selected}
              aria-label={p.projectId}
              tabIndex={0}
              onClick={() => setSelected(p.projectId === selected ? "" : p.projectId)}
              onKeyDown={(e) => (e.key === "Backspace" || e.key === "Delete") && live && remove(p)}
            >
              <span className="set-tile sage"><Icon name="folder" /></span>
              <div className="ft">
                <div className="fn">{p.projectId}</div>
                <div className="fdesc mono" title={p.path}>{p.path}</div>
              </div>
              <select
                className="psel"
                aria-label={t("projects.how", { name: p.projectId })}
                value={p.workspace === Workspace.IN_PLACE ? "inPlace" : "worktree"}
                disabled={!live}
                onClick={(e) => e.stopPropagation()}
                onChange={(e) => void st.run(() => rework(p, e.target.value === "inPlace" ? Workspace.IN_PLACE : Workspace.WORKTREE))}
              >
                <option value="worktree">{t("projects.worktree")}</option>
                <option value="inPlace">{t("projects.inPlace")}</option>
              </select>
            </div>
          ))}
          <div className="set-pm">
            <button aria-label={t("projects.add")} title={t("projects.add")} disabled={!live} onClick={() => void add()}>
              <Icon name="plus" />
            </button>
            <button aria-label={t("projects.remove")} title={t("projects.remove")} disabled={!sel || !live} onClick={() => sel && remove(sel)}>
              <Icon name="minus" />
            </button>
          </div>
        </div>
        <p className="set-foot">
          {t("projects.note")}
          <br />
          {t("projects.noteAccess")}
        </p>
      </div>
    </div>
  );
}
