import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Workspace } from "@hermesh/proto/identity";
import type { Project } from "@hermesh/proto/local";
import { codingAgentIds, codingAgentTitle, defaultScope, scopeComplete, scopeOfClient, scopeSpec, ttlChoices, type ScopeChoice } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { Icon } from "../icons";
import { machineName } from "../Sidebar";
import { useStore, type Store } from "../store";
import { ErrorLine, SignerWaiting, useAction } from "./common";

const key = (p: Project) => `${p.machineId}/${p.projectId}`;

/** The coding agents ready on some machine. */
function readyAgents(st: Store): string[] {
  const ready = new Set(st.machines.flatMap((m) => (m.status?.agents ?? []).filter((a) => a.available).map((a) => a.name)));
  return codingAgentIds.filter((a) => ready.has(a));
}

/**
 * A new agent's scope: every project on this Mac, and Claude Code and Codex
 * where they are ready (else every ready agent); with none ready, every
 * coding agent, including ones installed later.
 */
export function newScope(st: Store): ScopeChoice {
  const here = st.projects.filter((p) => p.machineId === st.status?.localMachineId).map(key);
  const ready = readyAgents(st);
  const usual = ready.filter((a) => defaultScope().agents.has(a));
  return { ...defaultScope(), allProjects: st.projects.length === 0, projects: new Set(here), allAgents: ready.length === 0, agents: new Set(usual.length ? usual : ready) };
}

function projectsText(st: Store, s: ScopeChoice): string {
  if (s.allProjects) return i18n.t("sheets:scope.allProjects");
  const picked = st.projects.filter((p) => s.projects.has(key(p)));
  if (!picked.length) return i18n.t("sheets:scope.none");
  const names = picked.length <= 3 ? picked.map((p) => p.projectId).join(i18n.t("sheets:scope.separator")) : i18n.t("sheets:scope.projectCount", { count: picked.length });
  const machines = new Set(picked.map((p) => p.machineId));
  return machines.size === 1 ? i18n.t("sheets:scope.onMachine", { machine: machineName(st, picked[0].machineId), names }) : names;
}

function Check({ on, disabled, onClick, children }: { on: boolean; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button className={`cb ${on ? "on" : ""} ${disabled ? "dis" : ""}`} role="checkbox" aria-checked={on} aria-disabled={disabled} onClick={() => !disabled && onClick()}>
      <span className="box"><Icon name="check" /></span>
      {children}
    </button>
  );
}

/** A grant's scope as three lines (projects, coding agents, lifetime), each opened to change it. */
export function ScopeRows({ scope, onChange, open: initialOpen = "" }: { scope: ScopeChoice; onChange: (s: ScopeChoice) => void; open?: "" | "p" | "a" }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [open, setOpen] = useState(initialOpen);
  const toggle = (set: ReadonlySet<string>, k: string) => {
    const n = new Set(set);
    if (!n.delete(k)) n.add(k);
    return n;
  };
  const agents = codingAgentIds.filter((a) => readyAgents(st).includes(a) || scope.agents.has(a));
  const picked = codingAgentIds.filter((a) => scope.agents.has(a));
  const row = (id: "p" | "a", k: string, v: string, opts: ReactNode) => (
    <div className="srow2" role="group" aria-label={k}>
      <div className="top">
        <span className="k">{k}</span>
        <span className="v">{v}</span>
        <button className="link" onClick={() => setOpen(open === id ? "" : id)}>{open === id ? t("scope.ok") : t("scope.change")}</button>
      </div>
      {open === id && <div className="opts">{opts}</div>}
    </div>
  );
  return (
    <>
      <div className="scope">
        {row(
          "p",
          t("scope.projects"),
          projectsText(st, scope),
          <>
            <Check on={scope.allProjects} onClick={() => onChange({ ...scope, allProjects: !scope.allProjects })}>{t("scope.allProjects")}</Check>
            {st.projects.map((p) => (
              <Check key={key(p)} on={scope.allProjects || scope.projects.has(key(p))} disabled={scope.allProjects} onClick={() => onChange({ ...scope, projects: toggle(scope.projects, key(p)) })}>
                {machineName(st, p.machineId)} / {p.projectId} <span className="faint">{p.workspace === Workspace.IN_PLACE ? t("scope.inPlace") : t("scope.worktree")}</span>
              </Check>
            ))}
          </>,
        )}
        {row(
          "a",
          t("scope.codingAgents"),
          scope.allAgents ? t("scope.allAgents") : picked.map(codingAgentTitle).join(t("scope.separator")) || t("scope.none"),
          <>
            <Check on={scope.allAgents} onClick={() => onChange({ ...scope, allAgents: !scope.allAgents })}>{t("scope.allAgents")}</Check>
            {agents.map((a) => (
              // The last one stays: a grant needs at least one coding agent.
              <Check key={a} on={scope.allAgents || scope.agents.has(a)} disabled={scope.allAgents} onClick={() => (scope.agents.size > 1 || !scope.agents.has(a)) && onChange({ ...scope, agents: toggle(scope.agents, a) })}>
                {codingAgentTitle(a)}
              </Check>
            ))}
          </>,
        )}
        <div className="srow2" role="group" aria-label={t("scope.ttl")}>
          <div className="top">
            <span className="k">{t("scope.ttl")}</span>
            <span className="v">{t("scope.ttlValue", { count: scope.ttlDays })}</span>
            <div className="seg">
              {ttlChoices.map((n) => (
                <button key={n} className={scope.ttlDays === n ? "on" : ""} aria-pressed={scope.ttlDays === n} onClick={() => onChange({ ...scope, ttlDays: n })}>
                  {t("scope.days", { count: n })}
                </button>
              ))}
            </div>
          </div>
        </div>
      </div>
      {!scope.allProjects && !scope.projects.size && <p className="sheet-note warn">{t("scope.needProject")}</p>}
      {!scope.allAgents && !scope.agents.size && <p className="sheet-note warn">{t("scope.needAgent")}</p>}
    </>
  );
}

/** Change / Renew: a new grant, signed in the signer's window, replaces the old one at once. */
export function ScopeSheet({ clientId, renew }: { clientId: string; renew: boolean }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const c = st.clients.find((x) => x.clientId === clientId);
  const [scope, setScope] = useState(() => (c ? scopeOfClient(c, st.projects) : defaultScope()));
  const act = useAction();
  const close = () => st.set({ sheet: null });
  if (!c) {
    return (
      <>
        <h3 id="sheet-title">{t("scopeSheet.goneTitle")}</h3>
        <div className="sheet-foot"><button className="btn" onClick={close}>{t("close")}</button></div>
      </>
    );
  }
  const save = async () => {
    const spec = scopeSpec(scope);
    spec.label = c.label;
    spec.taskVisibility = c.spec?.taskVisibility ?? spec.taskVisibility;
    spec.maxConcurrentSessions = c.spec?.maxConcurrentSessions ?? 0;
    if (await act.go(() => local.updateClient({ grantId: c.grantId, spec }))) {
      close();
      st.show(t("scopeSheet.saved", { name: c.label }));
      st.refresh();
    }
  };
  return (
    <>
      <h3 id="sheet-title">{renew ? t("scopeSheet.titleRenew", { name: c.label }) : t("scopeSheet.titleChange", { name: c.label })}</h3>
      <p className="sub">{t("scopeSheet.sub", { name: c.label })}</p>
      <ScopeRows scope={scope} onChange={setScope} open={renew ? "" : "p"} />
      {act.busy && <SignerWaiting detail={t("scopeSheet.signerDetail")} />}
      <ErrorLine text={act.error} />
      <div className="sheet-foot">
        <button className="btn" onClick={close}>{t("cancel")}</button>
        <button className="btn primary" disabled={act.busy || !scopeComplete(scope)} onClick={save}>
          <Icon name="lock" />
          {t("scopeSheet.save")}
        </button>
      </div>
    </>
  );
}
