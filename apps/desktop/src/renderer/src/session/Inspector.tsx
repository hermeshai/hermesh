import { useEffect, useState, type MouseEvent } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { AgentModel } from "@hermesh/proto/machine";
import type { Session } from "@hermesh/proto/session";
import {
  BackgroundTaskKind,
  BackgroundTaskStatus,
  BackgroundTaskStopScope,
  type BackgroundTask,
} from "@hermesh/proto/thread";
import { clockText, codingAgentTitle, dateOf, elapsedSeconds, modelTitle } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { local } from "../api";
import { GrantAvatar, PopMenu, type MenuEntry } from "../components";
import { Icon, type IconName } from "../icons";
import { copy } from "../settings/common";
import { grantLabel, machineName } from "../Sidebar";
import { useNow, useStore } from "../store";

type Menu = { of: "project" | "branch"; anchor: HTMLElement; keyboard: boolean };

/**
 * Cards floating over the conversation's top right, shown or hidden with
 * the toolbar's button (⌥⌘0). The session's environment: who started it,
 * which coding agent runs it, on which machine (its settings a click away),
 * in which project, on which branch; the project and the branch open a menu
 * to copy them, and on this Mac the project's to open its folder. Under it,
 * its context: the model and reasoning effort it runs with, once known.
 */
export function Inspector({
  s,
  backgroundTasks,
  selectedTaskId,
  onSelectTask,
  onStopBackgroundTask,
}: {
  s: Session;
  backgroundTasks?: readonly BackgroundTask[];
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  onStopBackgroundTask?: (taskId?: string) => void;
}) {
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
              <button className="tb insp-gear" aria-label={t("inspector.machineSettings")} title={t("inspector.machineSettings")} onClick={() => window.hermesh?.openSettings(`env/${s.machineId}`)}>
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
      {backgroundTasks && backgroundTasks.length > 0 && (
        <BackgroundTasksCard
          tasks={backgroundTasks}
          selectedTaskId={selectedTaskId}
          onSelectTask={onSelectTask}
          onStop={onStopBackgroundTask}
        />
      )}
    </div>
  );
}

function bgTaskIcon(kind: BackgroundTaskKind): IconName {
  switch (kind) {
    case BackgroundTaskKind.SUBAGENT:
      return "persons";
    case BackgroundTaskKind.SCHEDULE:
      return "clock";
    case BackgroundTaskKind.DAEMON:
      return "bolt";
    default:
      return "terminal";
  }
}

function BackgroundTasksCard({
  tasks,
  selectedTaskId,
  onSelectTask,
  onStop,
}: {
  tasks: readonly BackgroundTask[];
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  onStop?: (taskId?: string) => void;
}) {
  const { t } = useTranslation("thread");
  const hasRunning = tasks.some((x) => x.status === BackgroundTaskStatus.RUNNING);
  const runningCount = tasks.filter((x) => x.status === BackgroundTaskStatus.RUNNING).length;
  const now = useNow(hasRunning ? 1000 : 30_000);
  const [openIds, setOpenIds] = useState<Record<string, boolean>>({});

  useEffect(() => {
    if (selectedTaskId) {
      setOpenIds((prev) => ({ ...prev, [selectedTaskId]: true }));
    }
  }, [selectedTaskId]);

  const kindLabel = (k: BackgroundTaskKind) => {
    switch (k) {
      case BackgroundTaskKind.SUBAGENT:
        return t("background.kindSubagent");
      case BackgroundTaskKind.SCHEDULE:
        return t("background.kindSchedule");
      case BackgroundTaskKind.DAEMON:
        return t("background.kindDaemon");
      default:
        return t("background.kindCommand");
    }
  };

  const statusLabel = (st: BackgroundTaskStatus) => {
    switch (st) {
      case BackgroundTaskStatus.RUNNING:
        return t("background.statusRunning");
      case BackgroundTaskStatus.COMPLETED:
        return t("background.statusCompleted");
      case BackgroundTaskStatus.FAILED:
        return t("background.statusFailed");
      default:
        return t("background.statusStopped");
    }
  };

  return (
    <aside className="insp" aria-label={t("background.title")} data-testid="inspector-background-tasks">
      <div className="th-bgh">
        <h3 className="insp-h">{t("background.title")}</h3>
        {runningCount > 1 && onStop && (
          <button
            type="button"
            className="btn th-qbtn"
            data-testid="inspector-stop-all-bg"
            onClick={() => onStop("")}
          >
            {t("background.stopAll")}
          </button>
        )}
      </div>
      <div className="th-bglist">
        {tasks.map((bt) => {
          const isRunning = bt.status === BackgroundTaskStatus.RUNNING;
          const label = bt.title || bt.command || bt.taskId;
          const started = dateOf(bt.startedAt);
          const ended = dateOf(bt.completedAt);
          const dur = started ? clockText(elapsedSeconds(started, isRunning ? now : (ended ?? now))) : "";
          const expanded = openIds[bt.taskId] ?? (bt.taskId === selectedTaskId || tasks.length === 1);
          return (
            <div key={bt.taskId} className={`th-bgitem ${isRunning ? "live" : ""}`} data-testid={`bg-task-${bt.taskId}`}>
              <div className="th-bgrow">
                <button
                  type="button"
                  className="th-bgtoggle"
                  aria-expanded={expanded}
                  onClick={() => {
                    const next = !expanded;
                    setOpenIds((prev) => ({ ...prev, [bt.taskId]: next }));
                    if (next) onSelectTask?.(bt.taskId);
                  }}
                >
                  {isRunning ? <span className="mini-spin" /> : <Icon name={bgTaskIcon(bt.kind)} />}
                  <span className="th-bgtitle" title={label}>{label}</span>
                  {dur && <span className="th-bgdur">{dur}</span>}
                </button>
                {isRunning && onStop && (
                  <button
                    type="button"
                    className="btn th-qbtn"
                    title={bt.stopScope === BackgroundTaskStopScope.SESSION ? t("background.sessionStopHint") : t("background.stop")}
                    data-testid={`bg-task-stop-${bt.taskId}`}
                    onClick={() => onStop(bt.taskId)}
                  >
                    {t("background.stop")}
                  </button>
                )}
              </div>
              <div className="th-bgmeta">
                <span>{kindLabel(bt.kind)}</span>
                <span>·</span>
                <span className={bt.status === BackgroundTaskStatus.FAILED ? "th-bgerr" : ""}>
                  {statusLabel(bt.status)}
                  {bt.exitCode !== undefined && bt.status !== BackgroundTaskStatus.RUNNING ? ` (${bt.exitCode})` : ""}
                </span>
              </div>
              {expanded && (
                <div className="th-bgdetail">
                  {bt.command && bt.command !== bt.title && <code className="th-bgcmd sel-text">{bt.command}</code>}
                  {bt.outputTail ? (
                    <pre className="th-bgout sel-text" data-testid={`bg-task-output-${bt.taskId}`}>{bt.outputTail}</pre>
                  ) : (
                    <div className="th-bgempty">{bt.summary || t("background.noOutput")}</div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </aside>
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
