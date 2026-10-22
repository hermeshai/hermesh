import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Workspace } from "@hermesh/proto/identity";
import type { AgentStatus } from "@hermesh/proto/machine";
import type { Machine, Project } from "@hermesh/proto/local";
import { codingAgentIds, codingAgentTitle, projectIdFromFolder } from "@hermesh/core";
import { AgentLogo, AgentTile, Mark } from "@hermesh/ui";
import type { TerminalCommand } from "../../shared/bridge";
import { useRecheckAgents } from "./agents";
import { local } from "./api";
import { Icon } from "./icons";
import { useStore } from "./store";
import "./agentlist.css";

// A machine's coding agents, as the first run's "Check and Manage Available
// Agents" shows them and settings' machine page too: every coding agent
// Hermesh supports, what the machine has of each (version, logged in), and
// for each installed one the projects it may work in. A project is one
// folder; it lists the agents it is for (none: all of them).

/** The projects agent may work in. */
export const projectsFor = (projects: Project[], agent: string) => projects.filter((p) => p.agents.length === 0 || p.agents.includes(agent));

export const ready = (a: AgentStatus) => a.available && a.loggedIn;

/** Claude Code and Codex first, then the order Hermesh lists them in. */
const order = (name: string) => codingAgentIds.indexOf(name);

/**
 * A machine's agents sorted out: installed (ready first), still being checked
 * (not known yet), not installed; searching while none is known yet, and
 * known once the machine has said which it has.
 */
export function sortAgents(agents: AgentStatus[] | undefined) {
  const installed = (agents ?? []).filter((a) => a.available).sort((a, b) => Number(ready(b)) - Number(ready(a)) || order(a.name) - order(b.name));
  const checking = (agents ?? []).filter((a) => a.checking && !a.available).sort((a, b) => order(a.name) - order(b.name));
  const missing = codingAgentIds.filter((id) => !installed.some((a) => a.name === id) && !checking.some((a) => a.name === id));
  return { installed, checking, missing, searching: !agents || (installed.length === 0 && checking.length > 0), known: !!agents?.length };
}

/**
 * The list: one row per installed agent, opening to its projects, the ones
 * being checked, then the ones not installed; until the machine says which
 * it has, unknown instead. Projects none of the installed agents may use
 * (theirs is gone, or not known yet) follow, so they can still be removed.
 */
export function AgentList({ m, agents, unknown }: { m: Machine; agents: ReturnType<typeof sortAgents>; unknown?: ReactNode }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const projects = st.projects.filter((p) => p.machineId === m.machineId);
  const { installed, checking, missing, known } = agents;
  const others = projects.filter((p) => !installed.some((a) => projectsFor([p], a.name).length > 0));
  const othersTitle = known ? t("prepare.otherProjects") : t("prepare.allProjects");
  // The first ready agent starts open; the person opens and closes the rest.
  const [opened, setOpened] = useState<Set<string> | null>(null);
  const open = opened ?? new Set(installed.filter(ready).slice(0, 1).map((a) => a.name));
  const toggle = (name: string) => {
    const next = new Set(open);
    if (!next.delete(name)) next.add(name);
    setOpened(next);
  };
  return (
    <>
      <div className="ag-list" role="list" aria-label={t("prepare.agents")}>
        {!known && unknown}
        {installed.map((a) => (
          <AgentRow key={a.name} a={a} m={m} projects={projects} open={open.has(a.name)} toggle={() => toggle(a.name)} />
        ))}
        {checking.map((a) => (
          <div key={a.name} className="ag" role="listitem" aria-label={codingAgentTitle(a.name)}>
            <div className="ag-head">
              <AgentTile agent={a.name} size={32} />
              <span className="ag-text">
                <b>{codingAgentTitle(a.name)}</b>
                <span className="ag-sub">
                  <span className="ag-checking">
                    <Mark size={11} variant="seq" />
                    {t("prepare.checking")}
                  </span>
                </span>
              </span>
            </div>
          </div>
        ))}
      </div>
      {others.length > 0 && (
        <>
          <div className="ag-group">{othersTitle}</div>
          <div className="ag-prjs" role="list" aria-label={othersTitle}>
            {others.map((x) => (
              <ProjectRow key={x.projectId} p={x} />
            ))}
          </div>
        </>
      )}
      {known && missing.length > 0 && (
        <>
          <div className="ag-group">{t("prepare.notInstalled")}</div>
          <div className="ag-missing" role="list" aria-label={t("prepare.missing")}>
            {missing.map((name) => (
              <div key={name} role="listitem" aria-label={codingAgentTitle(name)}>
                <AgentLogo agent={name} size={16} />
                {codingAgentTitle(name)}
              </div>
            ))}
          </div>
        </>
      )}
    </>
  );
}

const logins: Record<string, { key: TerminalCommand; text: string }> = {
  codex: { key: "codexLogin", text: "codex login" },
  claude: { key: "claudeLogin", text: "claude" },
};

function AgentRow(p: { a: AgentStatus; m: Machine; projects: Project[]; open: boolean; toggle: () => void }) {
  const { a, m } = p;
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const [busy, setBusy] = useState(false);
  const [ran, setRan] = useState(false);
  useRecheckAgents(ran && !a.loggedIn);
  const title = codingAgentTitle(a.name);
  const mine = projectsFor(p.projects, a.name);
  // Logging in runs in this Mac's Terminal: another machine's agent is logged in there.
  const login = m.local ? logins[a.name] : undefined;

  // On this Mac the folder is picked: one already a project gets this agent
  // too; a new one is for this agent only. Another machine's is typed in.
  const add = async () => {
    if (!m.local) return st.set({ sheet: { kind: "addProject", machineId: m.machineId, agent: a.name } });
    const path = await window.hermesh?.pickDirectory();
    if (!path) return;
    const same = p.projects.find((x) => x.path === path);
    setBusy(true);
    if (same) {
      if (same.agents.length > 0 && !same.agents.includes(a.name)) await st.run(() => save(same, [...same.agents, a.name]), t("prepare.alsoFor", { project: same.projectId, agent: title }));
    } else {
      const projectId = projectIdFromFolder(path.split(/[\\/]/).filter(Boolean).pop() ?? "", new Set(p.projects.map((x) => x.projectId)));
      await st.run(() => local.addProject({ machineId: m.machineId, projectId, path, agents: [a.name] }));
    }
    setBusy(false);
  };

  return (
    <div className={`ag ${p.open ? "open" : ""}`} role="listitem" aria-label={title} title={a.detail || undefined}>
      <button className="ag-head" aria-expanded={p.open} onClick={p.toggle}>
        <AgentTile agent={a.name} size={32} />
        <span className="ag-text">
          <b>{title}</b>
          <span className="ag-sub">
            {a.version && <span className="mono">{a.version}</span>}
            <span className={a.loggedIn ? "ok" : "warn"}>
              <i />
              {a.loggedIn ? t("prepare.signedIn") : t("prepare.notSignedIn")}
            </span>
            <span>{mine.length > 0 ? t("prepare.projects", { count: mine.length }) : t("prepare.noProjects")}</span>
          </span>
        </span>
        <Icon name="chevR" className="ag-chev" />
      </button>
      {p.open && (
        <div className="ag-body">
          {!a.loggedIn && (
            <div className="ag-login">
              {ran ? (
                <>
                  <Mark size={12} variant="seq" />
                  {t("prepare.waitingLogin")}
                </>
              ) : (
                <>
                  <span>{t("prepare.loginFirst", { agent: title })}</span>
                  {login && (
                    <>
                      <span className="chip mono">{login.text}</span>
                      <button className="btn sm" onClick={() => (window.hermesh?.runInTerminal(login.key), setRan(true))}>
                        <Icon name="terminal" />
                        {t("prepare.runInTerminal")}
                      </button>
                    </>
                  )}
                </>
              )}
            </div>
          )}
          {mine.length > 0 && (
            <div className="ag-prjs">
              {mine.map((x) => (
                <ProjectRow key={x.projectId} p={x} agent={a.name} />
              ))}
            </div>
          )}
          <button className="ag-add" onClick={add} disabled={busy || (!m.online && !m.local)}>
            <Icon name="plus" />
            {busy ? t("prepare.adding") : t("prepare.addProject")}
          </button>
        </div>
      )}
    </div>
  );
}

/** Adds the project again, as it is, for agents (adding again changes it). */
const save = (p: Project, agents: string[], workspace = p.workspace) =>
  local.addProject({ machineId: p.machineId, projectId: p.projectId, path: p.path, workspace, defaultBaseRef: workspace === Workspace.IN_PLACE ? "" : p.defaultBaseRef, description: p.description, agents });

/**
 * A project of one agent: its name and way of working, its folder, and a
 * ⋯ menu to change the way of working or take it from this agent. Of no
 * installed agent (agent unset): the agents it is for, and removing it.
 */
function ProjectRow({ p, agent }: { p: Project; agent?: string }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const worktree = p.workspace !== Workspace.IN_PLACE;
  const how = worktree ? t("prepare.worktree") : t("prepare.inPlace");
  const more = async () => {
    const id = await window.hermesh?.popupMenu([
      { id: "worktree", label: t("prepare.worktreeItem"), checked: worktree },
      { id: "inPlace", label: t("prepare.inPlaceItem"), checked: !worktree },
      { separator: true },
      { id: "remove", label: agent ? t("prepare.removeItem", { agent: codingAgentTitle(agent) }) : t("machineOnly.removeProject") },
    ]);
    if (id === "worktree" && !worktree) await st.run(() => save(p, p.agents, Workspace.WORKTREE));
    if (id === "inPlace" && worktree) await st.run(() => save(p, p.agents, Workspace.IN_PLACE));
    if (id === "remove") {
      // Taken from the last agent it is for, it goes; one for all stays for the others.
      const rest = agent ? (p.agents.length > 0 ? p.agents : codingAgentIds).filter((x) => x !== agent) : [];
      await st.run(() => (rest.length > 0 ? save(p, rest) : local.removeProject({ machineId: p.machineId, projectId: p.projectId })));
    }
  };
  return (
    <div className="ag-prj" role="group" aria-label={p.projectId}>
      <Icon name="folder" />
      <div className="ag-prj-t">
        <span>
          <b>{p.projectId}</b>
          <em>{agent ? how : [how, ...p.agents.map(codingAgentTitle)].join(" · ")}</em>
        </span>
        <span className="mono" title={p.path}>
          {p.path}
        </span>
      </div>
      <button className="tb" aria-label={t("prepare.more")} onClick={more}>
        <Icon name="more" />
      </button>
    </div>
  );
}
