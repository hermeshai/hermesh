import { useState, type ReactNode } from "react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { AgentSpeed, Workspace } from "@hermesh/proto/identity";
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
// each installed one opening its page (AgentPage): the model its sessions run
// with (Claude Code and Codex) and the projects it may work in. A project is
// one folder; it lists the agents it is for (none: all of them).

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
 * The list: one row per installed agent, opening its page (onOpen), the ones
 * being checked, then the ones not installed; until the machine says which
 * it has, unknown instead. Projects none of the installed agents may use
 * (theirs is gone, or not known yet) follow, so they can still be removed.
 */
export function AgentList({ m, agents, unknown, onOpen }: { m: Machine; agents: ReturnType<typeof sortAgents>; unknown?: ReactNode; onOpen: (agent: string) => void }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const projects = st.projects.filter((p) => p.machineId === m.machineId);
  const { installed, checking, missing, known } = agents;
  const others = projects.filter((p) => !installed.some((a) => projectsFor([p], a.name).length > 0));
  const othersTitle = known ? t("prepare.otherProjects") : t("prepare.allProjects");
  return (
    <>
      <div className="ag-list" role="list" aria-label={t("prepare.agents")}>
        {!known && unknown}
        {installed.map((a) => {
          const set = defaultsText(a, t);
          return (
            <div key={a.name} className="ag" role="listitem" aria-label={codingAgentTitle(a.name)} title={a.detail || undefined}>
              <button className="ag-head" onClick={() => onOpen(a.name)}>
                <AgentTile agent={a.name} size={32} />
                <span className="ag-text">
                  <b>{codingAgentTitle(a.name)}</b>
                  <span className="ag-sub">
                    {a.version && <span className="mono">{a.version}</span>}
                    <span className={a.loggedIn ? "ok" : "warn"}>
                      <i />
                      {a.loggedIn ? t("prepare.signedIn") : t("prepare.notSignedIn")}
                    </span>
                    <span>{projectsFor(projects, a.name).length > 0 ? t("prepare.projects", { count: projectsFor(projects, a.name).length }) : t("prepare.noProjects")}</span>
                    {set && <span>{set}</span>}
                  </span>
                </span>
                <Icon name="chevR" className="ag-chev" />
              </button>
            </div>
          );
        })}
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

type T = TFunction<"firstrun">;

const modelName = (a: AgentStatus, id: string) => a.models.find((x) => x.id === id)?.name || id;
const effortName = (t: T, e: string) => t(`prepare.efforts.${e}`, { defaultValue: e });
const speedName = (t: T, v: AgentSpeed) => (v === AgentSpeed.FAST ? t("prepare.fast") : t("prepare.standard"));

/** What is set for the agent's sessions here, for its row: "" while it follows its own configuration. */
function defaultsText(a: AgentStatus, t: T): string {
  const d = a.defaults;
  if (!d) return "";
  return [d.model && modelName(a, d.model), d.effort && effortName(t, d.effort), d.speed ? speedName(t, d.speed) : ""].filter(Boolean).join(" · ");
}

const logins: Record<string, { key: TerminalCommand; text: string }> = {
  codex: { key: "codexLogin", text: "codex login" },
  claude: { key: "claudeLogin", text: "claude" },
};

/**
 * An agent's page on a machine, opened from its row: signing in when it
 * isn't, the model, reasoning effort and (Codex) speed its sessions run with
 * when the agent starting one names no model (Claude Code and Codex), and the
 * projects it may work in.
 */
export function AgentPage({ m, agent }: { m: Machine; agent: string }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const a = m.status?.agents.find((x) => x.name === agent);
  const [busy, setBusy] = useState(false);
  const [ran, setRan] = useState(false);
  useRecheckAgents(ran && !a?.loggedIn);
  const title = codingAgentTitle(agent);
  const projects = st.projects.filter((p) => p.machineId === m.machineId);
  const mine = projectsFor(projects, agent);
  // Logging in runs in this Mac's Terminal: another machine's agent is logged in there.
  const login = m.local ? logins[agent] : undefined;

  // On this Mac the folder is picked: one already a project gets this agent
  // too; a new one is for this agent only. Another machine's is typed in.
  const add = async () => {
    if (!m.local) return st.set({ sheet: { kind: "addProject", machineId: m.machineId, agent } });
    const path = await window.hermesh?.pickDirectory();
    if (!path) return;
    const same = projects.find((x) => x.path === path);
    setBusy(true);
    if (same) {
      if (same.agents.length > 0 && !same.agents.includes(agent)) await st.run(() => save(same, [...same.agents, agent]), t("prepare.alsoFor", { project: same.projectId, agent: title }));
    } else {
      const projectId = projectIdFromFolder(path.split(/[\\/]/).filter(Boolean).pop() ?? "", new Set(projects.map((x) => x.projectId)));
      await st.run(() => local.addProject({ machineId: m.machineId, projectId, path, agents: [agent] }));
    }
    setBusy(false);
  };

  return (
    <div className="ag-page" role="region" aria-label={title}>
      <div className="ag-hero">
        <AgentTile agent={agent} size={44} />
        <span className="ag-text">
          <b>{title}</b>
          <span className="ag-sub">
            <span>{m.machineName}</span>
            {a?.version && <span className="mono">{a.version}</span>}
            {a && (
              <span className={a.loggedIn ? "ok" : "warn"}>
                <i />
                {a.loggedIn ? t("prepare.signedIn") : t("prepare.notSignedIn")}
              </span>
            )}
          </span>
        </span>
      </div>
      {a && !a.loggedIn && (
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
      {a && a.models.length > 0 && <Defaults a={a} m={m} />}
      <section aria-label={t("prepare.projectsHead")}>
        <div className="ag-group">{t("prepare.projectsHead")}</div>
        {mine.length > 0 && (
          <div className="ag-prjs">
            {mine.map((x) => (
              <ProjectRow key={x.projectId} p={x} agent={agent} />
            ))}
          </div>
        )}
        <button className="ag-add" onClick={add} disabled={busy || (!m.online && !m.local)}>
          <Icon name="plus" />
          {busy ? t("prepare.adding") : t("prepare.addProject")}
        </button>
      </section>
    </div>
  );
}

/**
 * The model, reasoning effort and (Codex) speed an agent's sessions run with
 * when the agent starting one names no model, one row each: the agent's own
 * configuration unless set here; set here, only Hermesh's sessions use them.
 * Each row's button shows the value and opens a menu of the choices.
 */
function Defaults({ a, m }: { a: AgentStatus; m: Machine }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const title = codingAgentTitle(a.name);
  const d = { model: a.defaults?.model ?? "", effort: a.defaults?.effort ?? "", speed: a.defaults?.speed ?? AgentSpeed.UNSPECIFIED };
  const own = a.own;
  const same = (v: string) => (v ? t("prepare.sameAsValue", { agent: title, value: v }) : t("prepare.sameAs", { agent: title }));
  const effortsOf = (id: string) => a.models.find((x) => x.id === id)?.efforts;
  const model = d.model || own?.model || "";

  const save = (next: typeof d) => st.run(() => local.setAgentDefaults({ machineId: m.machineId, defaults: { agent: a.name, ...next } }));
  const pick = async (items: { id: string; label: string; checked: boolean }[], set: (v: string) => void) => {
    const id = await window.hermesh?.popupMenu(items);
    if (id == null) return;
    set(id);
  };
  const rows = [
    {
      key: "model",
      label: t("prepare.model"),
      value: d.model ? modelName(a, d.model) : same(own?.model ? modelName(a, own.model) : ""),
      open: () =>
        pick(
          [{ id: "", label: same(own?.model ? modelName(a, own.model) : ""), checked: !d.model }, ...a.models.map((x) => ({ id: x.id, label: x.name || x.id, checked: d.model === x.id }))],
          (v) => {
            // An effort the newly picked model lacks goes back to the agent's own.
            const ok = effortsOf(v || own?.model || "");
            save({ ...d, model: v, effort: d.effort && ok && !ok.includes(d.effort) ? "" : d.effort });
          },
        ),
    },
    {
      key: "effort",
      label: t("prepare.effort"),
      value: d.effort ? effortName(t, d.effort) : same(own?.effort ? effortName(t, own.effort) : ""),
      open: () =>
        pick(
          [
            { id: "", label: same(own?.effort ? effortName(t, own.effort) : ""), checked: !d.effort },
            ...(effortsOf(model) ?? a.models[0].efforts).map((e) => ({ id: e, label: effortName(t, e), checked: d.effort === e })),
          ],
          (v) => save({ ...d, effort: v }),
        ),
    },
    ...(a.name === "codex"
      ? [
          {
            key: "speed",
            label: t("prepare.speed"),
            value: d.speed ? speedName(t, d.speed) : same(own?.speed ? speedName(t, own.speed) : ""),
            open: () =>
              pick(
                [
                  { id: "0", label: same(own?.speed ? speedName(t, own.speed) : ""), checked: !d.speed },
                  { id: "1", label: t("prepare.standard"), checked: d.speed === AgentSpeed.STANDARD },
                  { id: "2", label: t("prepare.fast"), checked: d.speed === AgentSpeed.FAST },
                ],
                (v) => save({ ...d, speed: Number(v) }),
              ),
          },
        ]
      : []),
  ];
  return (
    <section aria-label={t("prepare.defaultsHead")}>
      <div className="ag-group">{t("prepare.defaultsHead")}</div>
      <div className="ag-prjs">
        {rows.map((r) => (
          <div key={r.key} className="ag-set" role="group" aria-label={r.label}>
            <span>{r.label}</span>
            <button className="ag-pop" onClick={r.open} disabled={!m.online && !m.local}>
              {r.value}
              <Icon name="chevD" />
            </button>
          </div>
        ))}
      </div>
      <p className="ag-note">{t("prepare.defaultsNote", { agent: title })}</p>
    </section>
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
