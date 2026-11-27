import { useEffect, useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { codingAgentIds, codingAgentTitle, projectIdFromFolder } from "@hermesh/core";
import { Workspace } from "@hermesh/proto/identity";
import type { Machine } from "@hermesh/proto/local";
import { AgentLogo, Mark } from "@hermesh/ui";
import { local } from "../api";
import { logins, ready, sortAgents, useMissingAgentList } from "../AgentList";
import { useCheckAgents, useRecheckAgents } from "../agents";
import { Icon } from "../icons";
import { useStore } from "../store";
import { rise } from "./Stage";

// 2 Agent providers (docs/cold-start-prd.md §5.4): this Mac's, looked for (at least 2.2s, the
// sweep once and a little), then listed: the installed ones, signed in or not, and the projects.

/** Long enough for the sweep to be seen once, whatever the check takes. */
const SEARCH_MS = 2200;

/** Whether the step (once active) is still looking: the check has not answered, or the sweep has not played. */
export function useSearching(m: Machine | undefined, active: boolean): boolean {
  const searching = sortAgents(m?.status?.agents).searching;
  const [held, setHeld] = useState(true);
  useCheckAgents(active);
  useEffect(() => {
    if (!active) return;
    setHeld(true);
    const timer = setTimeout(() => setHeld(false), SEARCH_MS);
    return () => clearTimeout(timer);
  }, [active]);
  return held || searching || !m;
}

export function Searching() {
  const { t } = useTranslation("firstrun");
  return (
    <div className="fr-main">
      <div className="fr-center">
        <Mark size={52} variant="draw" delay={200} />
        <h2 className="fr-title in" style={{ marginTop: 24, fontSize: 19 }}>
          {t("team.searching")}
        </h2>
        <div className="fr-sweep in" style={rise(1)} aria-hidden="true">
          {codingAgentIds.map((id, i) => (
            <span key={id} style={{ "--i": i } as CSSProperties}>
              <AgentLogo agent={id} size={32} />
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}

export function Team({ m, next }: { m: Machine; next: () => void }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const agents = sortAgents(m.status?.agents);
  const missingList = useMissingAgentList(agents);
  const projects = st.projects.filter((p) => p.machineId === m.machineId).sort((a, b) => a.projectId.localeCompare(b.projectId));
  const readyCount = agents.installed.filter(ready).length;
  const [ran, setRan] = useState(false);
  useRecheckAgents(ran && agents.installed.some((a) => !a.loggedIn));
  const canGo = readyCount > 0 && projects.length > 0;

  const add = async () => {
    const path = await window.hermesh?.pickDirectory();
    if (!path || projects.some((p) => p.path === path)) return;
    const projectId = projectIdFromFolder(path.split(/[\\/]/).filter(Boolean).pop() ?? "", new Set(projects.map((x) => x.projectId)));
    await st.run(() => local.addProject({ machineId: m.machineId, projectId, path }));
  };
  const addButton = (
    <button className="btn" onClick={() => void add()}>
      {t("team.addProject")}
    </button>
  );
  const none = agents.installed.length === 0;

  return (
    <>
      <div className="fr-main" style={{ paddingTop: 26 }}>
        <h2 className="fr-title in" style={rise(0)}>
          {none ? t("team.none") : t("team.found", { count: readyCount })}
        </h2>
        <p className="fr-text in" style={rise(1)}>
          {none ? t("team.noneBody") : t("team.sub")}
        </p>
        <div className="fr-grp fr-team" role="list" aria-label={t("team.agents")}>
          {(none ? missingList.names.map((name) => ({ name, available: false, loggedIn: false, version: "" })) : agents.installed).map((a, i) => (
            <div key={a.name} className="fr-row lead in" role="listitem" style={rise(i + 2)} aria-label={codingAgentTitle(a.name)}>
              <AgentLogo agent={a.name} size={30} />
              <div className="t">
                <div className="n">{codingAgentTitle(a.name)}</div>
                {a.version && <div className="d mono">{a.version}</div>}
              </div>
              {!a.available ? (
                <span className="v">{t("team.notInstalled")}</span>
              ) : a.loggedIn ? (
                <span className="v ok">
                  <span className="fr-pop" style={{ animationDelay: `${(i + 2) * 80 + 500}ms` }}>
                    <Icon name="check" />
                  </span>
                  {t("team.signedIn")}
                </span>
              ) : (
                <>
                  <span className="v warn">{t("team.notSignedIn")}</span>
                  {logins[a.name] && (
                    <button className="btn" onClick={() => (window.hermesh?.runInTerminal(logins[a.name].key), setRan(true))}>
                      {t("team.runInTerminal")}
                    </button>
                  )}
                </>
              )}
            </div>
          ))}
        </div>
        {none && missingList.toggle}
        <div className="fr-group in" style={rise(5)}>
          {t("team.projects")}
        </div>
        <div className="fr-grp in" style={rise(6)} role="list" aria-label={t("team.projects")}>
          {projects.length === 0 && (
            <div className="fr-row lead" role="listitem">
              <span className="fr-tile" style={{ background: "var(--t-sage)" }}>
                <Icon name="folder" />
              </span>
              <div className="t">
                <div className="n">{t("team.noProjects")}</div>
                {/* What 继续 waits for, once an agent provider can work. */}
                {readyCount > 0 && <div className="d">{t("team.needProject")}</div>}
              </div>
              {addButton}
            </div>
          )}
          {projects.map((p, i) => (
            <div key={p.projectId} className="fr-row lead" role="listitem" aria-label={p.projectId}>
              <span className="fr-tile" style={{ background: "var(--t-sage)" }}>
                <Icon name="folder" />
              </span>
              <div className="t">
                <div className="n">{p.projectId}</div>
                <div className="d mono" title={p.path}>{p.path}</div>
              </div>
              <span className="v">{p.workspace === Workspace.IN_PLACE ? t("prepare.inPlace") : t("prepare.worktree")}</span>
              {i === projects.length - 1 && addButton}
            </div>
          ))}
        </div>
      </div>
      <div className="fr-end in" style={rise(7)}>
        <p className="fr-note">
          <Icon name="shield" />
          {t("prepare.promise")}
        </p>
        <button className="link" onClick={next}>
          {t("team.later")}
        </button>
        <button className="btn primary md" disabled={!canGo} onClick={next}>
          {t("team.next")}
        </button>
      </div>
    </>
  );
}
