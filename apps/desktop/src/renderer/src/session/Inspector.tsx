import { useEffect, useState, type MouseEvent } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { AgentModel } from "@hermesh/proto/machine";
import type { Session } from "@hermesh/proto/session";
import { codingAgentTitle, modelTitle } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { local } from "../api";
import { GrantAvatar, PopMenu, type MenuEntry } from "../components";
import { Icon } from "../icons";
import { copy } from "../settings/common";
import { grantLabel, machineName } from "../Sidebar";
import { useStore } from "../store";

type Menu = { of: "project" | "branch"; anchor: HTMLElement; keyboard: boolean };

/**
 * Cards floating over the conversation's top right, shown or hidden with
 * the toolbar's button (⌥⌘0). The session's environment: who started it,
 * which coding agent runs it, on which machine (its settings a click away),
 * in which project, on which branch; the project and the branch open a menu
 * to copy them, and on this Mac the project's to open its folder. Under it,
 * its context: the model and reasoning effort it runs with, once known.
 */
export function Inspector({ s }: { s: Session }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const grant = grantLabel(st, s);
  const m = st.machines.find((x) => x.machineId === s.machineId);
  const here = m?.local === true;
  // A trusted device sets every machine; a machine only, its own.
  const settings = here || (st.status?.initialized === true && st.status.machineOnly !== true);
  const machine = here ? t("thisMac") : machineName(st, s.machineId);
  const path = st.projects.find((p) => p.machineId === s.machineId && p.projectId === s.projectId)?.path ?? "";
  const branch = useBranch(s, path);
  const [menu, setMenu] = useState<Menu>();
  const toggle = (of: Menu["of"]) => (e: MouseEvent<HTMLElement>) =>
    setMenu(menu?.of === of ? undefined : { of, anchor: e.currentTarget, keyboard: e.detail === 0 });
  const open = (to: "files" | "terminal") => window.hermesh?.openFolder(path, to);
  const items: MenuEntry[] =
    menu?.of === "project"
      ? [
          { id: "copy", label: t("inspector.copyPath"), run: () => copy(st, path) },
          ...(here ? [
            { id: "files", label: t(window.hermesh?.platform === "win32" ? "inspector.openInExplorer" : "inspector.openInFinder"), run: () => open("files") },
            { id: "terminal", label: t("inspector.openInTerminal"), run: () => open("terminal") },
          ] : []),
        ]
      : [{ id: "copy", label: t("inspector.copyBranch"), run: () => copy(st, branch ?? "") }];
  return (
    <div className="insp-col">
      <aside className="insp" aria-label={t("inspector.label")}>
        <h3 className="insp-h">{t("inspector.title")}</h3>
        <ul className="insp-facts">
          <li>
            <GrantAvatar c={st.clients.find((c) => c.clientId === s.createdBy) ?? { label: grant }} size="sm" />
            <span className="dim" title={grant}><Trans t={t} i18nKey="inspector.startedBy" values={{ grant }} components={{ b: <b /> }} /></span>
          </li>
          <li>
            <AgentLogo agent={s.agent} size={16} />
            <span className="dim"><Trans t={t} i18nKey="inspector.runBy" values={{ agent: codingAgentTitle(s.agent) }} components={{ b: <b /> }} /></span>
          </li>
          <li>
            <Icon name="laptop" />
            <span title={machine}>{machine}</span>
            {settings && (
              <button className="tb insp-gear" aria-label={t("inspector.machineSettings")} title={t("inspector.machineSettings")} onClick={() => window.hermesh?.openSettings(`machines/${s.machineId}`)}>
                <Icon name="gear" />
              </button>
            )}
          </li>
          <li>
            {path === "" ? (
              <>
                <Icon name="folder" />
                <span>{s.projectId}</span>
              </>
            ) : (
              <button className="insp-pick" title={path} aria-haspopup="menu" aria-expanded={menu?.of === "project"} onClick={toggle("project")}>
                <Icon name="folder" />
                <span>{s.projectId}</span>
              </button>
            )}
          </li>
          {branch !== undefined && (
            <li>
              {branch === "" ? (
                <>
                  <Icon name="branch" />
                  <span className="dim">{t("inspector.notRepo")}</span>
                </>
              ) : (
                <button className="insp-pick" title={branch} aria-haspopup="menu" aria-expanded={menu?.of === "branch"} onClick={toggle("branch")}>
                  <Icon name="branch" />
                  <span>{branch}</span>
                </button>
              )}
            </li>
          )}
        </ul>
        {menu && <PopMenu label={t(`inspector.${menu.of}Menu`)} items={items} anchor={menu.anchor} keyboard={menu.keyboard} onClose={() => setMenu(undefined)} />}
      </aside>
      {(s.model !== "" || s.effort !== "") && <Context s={s} models={m?.status?.agents.find((a) => a.name === s.agent)?.models} />}
    </div>
  );
}

/**
 * The model and reasoning effort the session runs with; what the agent does
 * not tell (Claude Code's effort) follows its own setting.
 */
function Context({ s, models }: { s: Session; models?: AgentModel[] }) {
  const { t } = useTranslation(["session", "firstrun"]);
  const same = t("inspector.sameAs", { agent: codingAgentTitle(s.agent) });
  const effort = s.effort === "" ? same : t(`firstrun:prepare.efforts.${s.effort}`, { defaultValue: s.effort });
  return (
    <aside className="insp" aria-label={t("inspector.context")}>
      <h3 className="insp-h">{t("inspector.context")}</h3>
      <ul className="insp-facts">
        <li>
          <Icon name="chip" />
          {s.model === "" ? (
            <span className="dim"><Trans t={t} i18nKey="inspector.model" values={{ model: same }} components={{ b: <b /> }} /></span>
          ) : (
            <span title={s.model}>{modelTitle(s.model, models)}</span>
          )}
        </li>
        <li>
          <Icon name="gauge" />
          <span className="dim" title={effort}><Trans t={t} i18nKey="inspector.effort" values={{ effort }} components={{ b: <b /> }} /></span>
        </li>
      </ul>
    </aside>
  );
}

/**
 * The branch the session works on: its own in a worktree, else the one
 * checked out in the project's folder, read from the machine when the card
 * opens and as the session's state changes. "": not a git repository;
 * undefined: not known (yet, or the machine can't be read).
 */
function useBranch(s: Session, path: string): string | undefined {
  const [read, setRead] = useState<string>();
  useEffect(() => {
    setRead(undefined);
    if (s.branch !== "" || path === "") return;
    let live = true;
    local.listMachineFolders({ machineId: s.machineId, path }).then(
      (r) => live && setRead(r.repo ? r.branch : ""),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [s.branch, s.machineId, path, s.status]);
  return s.branch !== "" ? s.branch : read;
}
