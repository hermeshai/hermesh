import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  BackgroundTaskKind,
  BackgroundTaskStatus,
  BackgroundTaskStopScope,
  type BackgroundTask,
} from "@hermesh/proto/session";
import { dateOf } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import { useNow, useStore } from "../store";
import { formatTaskTiming, latestOutputLine, stripAnsi, taskLabel, type TimeTranslate } from "./backgroundModel";
import { Markdown } from "./Markdown";
import { TerminalOutput } from "./TerminalOutput";

const tTime = i18n.getFixedT(null, "her") as unknown as TimeTranslate;

function taskTime(bt: BackgroundTask, now: Date): string {
  return formatTaskTiming(
    bt.status === BackgroundTaskStatus.RUNNING,
    dateOf(bt.startedAt),
    dateOf(bt.completedAt) ?? dateOf(bt.updatedAt),
    now,
    tTime,
  );
}

function kindIconName(kind: BackgroundTaskKind): string {
  switch (kind) {
    case BackgroundTaskKind.SUBAGENT:
      return "sparkle";
    case BackgroundTaskKind.SCHEDULE:
      return "clock";
    case BackgroundTaskKind.DAEMON:
      return "pulse";
    default:
      return "terminal";
  }
}

/**
 * Right-side single-column background tasks panel on Mac: collapsible Running / Completed sections
 * and individual rounded cards that expand vertically in place to inspect commands, live output, and summaries.
 */
export function Background({
  tasks,
  onStop,
}: {
  tasks: readonly BackgroundTask[];
  onStop: (taskId?: string) => void;
}) {
  const { t } = useTranslation("thread");
  const st = useStore();
  const runningTasks = tasks.filter((x) => x.status === BackgroundTaskStatus.RUNNING);
  const finishedTasks = tasks.filter((x) => x.status !== BackgroundTaskStatus.RUNNING);
  const running = runningTasks.length;
  const now = useNow(running > 0 ? 1000 : 30_000);

  const [foldRunning, setFoldRunning] = useState(false);
  const [foldDone, setFoldDone] = useState(false);
  const [expandedIds, setExpandedIds] = useState<Set<string>>(() =>
    st.bgTask ? new Set([st.bgTask]) : new Set(),
  );
  const [confirmStopAll, setConfirmStopAll] = useState(false);

  useEffect(() => {
    if (running <= 1 && confirmStopAll) setConfirmStopAll(false);
  }, [running, confirmStopAll]);

  useEffect(() => {
    if (!st.bgTask) return;
    const target = tasks.find((x) => x.taskId === st.bgTask);
    if (!target) return;
    if (target.status === BackgroundTaskStatus.RUNNING) setFoldRunning(false);
    else setFoldDone(false);
    setExpandedIds((prev) => {
      if (prev.has(target.taskId)) return prev;
      const next = new Set(prev);
      next.add(target.taskId);
      return next;
    });
    requestAnimationFrame(() => {
      document.querySelector(`[data-testid="bg-task-${CSS.escape(target.taskId)}"]`)?.scrollIntoView({ block: "nearest" });
    });
  }, [st.bgTask, tasks]);

  const done = tasks.filter((x) => x.status === BackgroundTaskStatus.COMPLETED).length;
  const stopped = tasks.filter((x) => x.status === BackgroundTaskStatus.STOPPED).length;
  const failed = tasks.filter((x) => x.status === BackgroundTaskStatus.FAILED).length;
  const parts = [
    running > 0 && t("background.running", { count: running }),
    done > 0 && t("background.completed", { count: done }),
    stopped > 0 && t("background.stopped", { count: stopped }),
    failed > 0 && t("background.failed", { count: failed }),
  ].filter(Boolean);
  const sub =
    tasks.length === 0
      ? t("background.none")
      : done === tasks.length
        ? t("background.allDone")
        : parts.join(" · ");

  const toggleCard = (taskId: string) => {
    setConfirmStopAll(false);
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(taskId)) next.delete(taskId);
      else next.add(taskId);
      return next;
    });
    if (st.bgTask === taskId) st.set({ bgTask: null });
  };

  const handleStopAll = () => {
    if (running <= 1 || confirmStopAll) {
      setConfirmStopAll(false);
      onStop("");
    } else {
      setConfirmStopAll(true);
    }
  };

  return (
    <div className="insp-col bgc-stack" role="complementary" aria-label={t("background.title")} data-testid="background-panel">
      <section className="insp bgc">
        <div className="bgc-h">
          <h3>{t("background.title")}</h3>
          {running > 0 && (
            <button
              type="button"
              className={`bgc-stopall ${confirmStopAll ? "confirm" : ""}`}
              data-testid="background-stop-all"
              onClick={handleStopAll}
            >
              {confirmStopAll ? t("background.confirmStopAll", { count: running }) : t("background.stopAll")}
            </button>
          )}
          <button
            type="button"
            className="bgc-close"
            aria-label={t("background.close")}
            title={t("background.close")}
            onClick={() => st.set({ background: false, bgTask: null })}
          >
            <Icon name="x" />
          </button>
        </div>

        <div className="bgc-sub">
          <span>{sub}</span>
        </div>

        <div className="bgc-list">
          {runningTasks.length > 0 && (
            <div className="bgc-sec">
              <button
                type="button"
                className={`bgc-sec-h ${foldRunning ? "folded" : ""}`}
                aria-expanded={!foldRunning}
                data-testid="background-sec-running"
                onClick={() => setFoldRunning((v) => !v)}
              >
                <span>{t("background.sectionRunning")}</span>
                <span className="bgc-sec-cnt">{runningTasks.length}</span>
                <Icon name="chevD" className="chev" />
              </button>
              {!foldRunning && (
                <div className="bgc-cards">
                  {runningTasks.map((bt) => (
                    <TaskCard
                      key={bt.taskId}
                      bt={bt}
                      now={now}
                      expanded={expandedIds.has(bt.taskId)}
                      onToggle={() => toggleCard(bt.taskId)}
                      onStop={onStop}
                    />
                  ))}
                </div>
              )}
            </div>
          )}

          {finishedTasks.length > 0 && (
            <div className="bgc-sec">
              <button
                type="button"
                className={`bgc-sec-h ${foldDone ? "folded" : ""}`}
                aria-expanded={!foldDone}
                data-testid="background-sec-done"
                onClick={() => setFoldDone((v) => !v)}
              >
                <span>{t("background.sectionDone")}</span>
                <span className="bgc-sec-cnt">{finishedTasks.length}</span>
                <Icon name="chevD" className="chev" />
              </button>
              {!foldDone && (
                <div className="bgc-cards">
                  {finishedTasks.map((bt) => (
                    <TaskCard
                      key={bt.taskId}
                      bt={bt}
                      now={now}
                      expanded={expandedIds.has(bt.taskId)}
                      onToggle={() => toggleCard(bt.taskId)}
                      onStop={onStop}
                    />
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      </section>
    </div>
  );
}

/** At the conversation's end, under the working line: how many background tasks still work; opens the panel. */
export function BackgroundLine({ tasks }: { tasks: readonly BackgroundTask[] }) {
  const { t } = useTranslation("thread");
  const st = useStore();
  if (tasks.length === 0) return null;
  const on = st.background ? "on" : "";
  return (
    <button
      type="button"
      className={`bgind ${on}`}
      aria-expanded={st.background}
      data-testid="chat-background-line"
      onClick={() => st.set({ background: !st.background, bgTask: null })}
    >
      <span>{t("background.runningLine", { count: tasks.length })}</span>
      <Icon name="chevR" />
    </button>
  );
}

function useKindLabel() {
  const { t } = useTranslation("thread");
  return (kind: BackgroundTaskKind): string => {
    switch (kind) {
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
}

function useStatusLabel() {
  const { t } = useTranslation("thread");
  return (status: BackgroundTaskStatus): string => {
    switch (status) {
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
}

function TaskCard({
  bt,
  now,
  expanded,
  onToggle,
  onStop,
}: {
  bt: BackgroundTask;
  now: Date;
  expanded: boolean;
  onToggle: () => void;
  onStop: (taskId?: string) => void;
}) {
  const { t } = useTranslation("thread");
  const kindLabel = useKindLabel();
  const statusLabel = useStatusLabel();
  const isRunning = bt.status === BackgroundTaskStatus.RUNNING;
  const label = taskLabel(bt);
  const peek = isRunning && !expanded ? latestOutputLine(bt.outputTail) : "";
  const cleanOutput = stripAnsi(bt.outputTail);
  const shownSummary = bt.summary.trim() !== "" && bt.summary.trim() !== "stopped by user" ? bt.summary.trim() : "";
  const briefText = bt.kind === BackgroundTaskKind.SUBAGENT ? (bt.command.trim() || bt.title.trim()) : bt.command.trim();
  const took = taskTime(bt, now);

  return (
    <div
      className={`bgc-card srow ${isRunning ? "running" : ""} ${expanded ? "expanded" : ""}`}
      data-testid={`bg-task-${bt.taskId}`}
    >
      <div className="bgc-card-head">
        <button
          type="button"
          className="bgc-card-main"
          aria-expanded={expanded}
          title={statusLabel(bt.status)}
          onClick={onToggle}
        >
          <span className="bgc-kicon">
            <Icon name={kindIconName(bt.kind)} />
          </span>

          <span className="bgc-cbody">
            <span className={`bgc-ctitle ${isRunning ? "run" : ""}`}>{label}</span>
            {peek !== "" && <span className="bgc-peek">{peek}</span>}
            <span className="bgc-cmeta">
              <span className="bgc-kind">{kindLabel(bt.kind)}</span>
              <CardStatusMeta bt={bt} took={took} />
            </span>
          </span>

          {!isRunning && (
            <span className="bgc-ctrail">
              <span className="bgc-chev" aria-hidden="true">
                <Icon name="chevR" />
              </span>
            </span>
          )}
        </button>

        {isRunning && (
          <StopRingButton bt={bt} onStop={onStop} testId={`bg-task-stop-${bt.taskId}`} />
        )}
      </div>

      {expanded && (
        <div className="bgc-detail">
          {briefText !== "" && (
            <Said
              caption={t(bt.kind === BackgroundTaskKind.SUBAGENT ? "background.brief" : "background.command")}
              text={briefText}
              mono={bt.kind !== BackgroundTaskKind.SUBAGENT}
              folds
            />
          )}
          {cleanOutput !== "" && (
            <TerminalOutput
              text={bt.outputTail}
              follow={isRunning}
              className="bgc-out sel-text"
              testId={`bg-task-output-${bt.taskId}`}
            />
          )}
          {shownSummary !== "" && (
            <div className="am">
              <Markdown text={shownSummary} />
            </div>
          )}
          {isRunning && cleanOutput === "" && shownSummary === "" && (
            <p className="bgc-note">{t("background.noOutput")}</p>
          )}
          {bt.status === BackgroundTaskStatus.COMPLETED && (
            <p className="bgc-done">
              <Icon name="check" className="sm" />
              <span>{[t("background.done"), took].filter(Boolean).join(" · ")}</span>
            </p>
          )}
          {bt.status === BackgroundTaskStatus.STOPPED && (
            <p className="bgc-note int">{[t("background.stoppedNote"), took].filter(Boolean).join(" · ")}</p>
          )}
          {bt.status === BackgroundTaskStatus.FAILED && (
            <p className="bgc-note bad">
              {bt.exitCode !== undefined && bt.exitCode !== 0
                ? t("background.failedExit", { code: bt.exitCode })
                : t("background.failedNote")}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function CardStatusMeta({ bt, took }: { bt: BackgroundTask; took: string }) {
  const { t } = useTranslation("thread");
  if (bt.status === BackgroundTaskStatus.RUNNING) {
    return <span className="bgc-dur">{took}</span>;
  }
  if (bt.status === BackgroundTaskStatus.COMPLETED) {
    return <span className="bgc-dur">{[t("background.statusCompleted"), took].filter(Boolean).join(" · ")}</span>;
  }
  if (bt.status === BackgroundTaskStatus.FAILED) {
    const label =
      bt.exitCode !== undefined && bt.exitCode !== 0
        ? t("background.failedExit", { code: bt.exitCode })
        : t("background.statusFailed");
    return <span className="bgc-st bad">{[label, took].filter(Boolean).join(" · ")}</span>;
  }
  return <span className="bgc-st warn">{[t("background.statusStopped"), took].filter(Boolean).join(" · ")}</span>;
}

function StopRingButton({
  bt,
  onStop,
  testId,
}: {
  bt: BackgroundTask;
  onStop: (taskId?: string) => void;
  testId?: string;
}) {
  const { t } = useTranslation("thread");
  const hint =
    bt.stopScope === BackgroundTaskStopScope.SESSION
      ? t("background.sessionStopHint")
      : t("background.stop");
  return (
    <button
      type="button"
      className="bgc-stop-ring"
      title={hint}
      aria-label={t("background.stop")}
      data-testid={testId}
      onClick={(e) => {
        e.stopPropagation();
        onStop(bt.taskId);
      }}
    >
      <svg className="ring-svg" viewBox="0 0 26 26" fill="none" aria-hidden="true">
        <circle cx="13" cy="13" r="10.2" stroke="currentColor" strokeOpacity=".26" strokeWidth="1.55" />
        <path
          className="spin-arc"
          d="M13 2.8 A10.2 10.2 0 0 1 22.6 9.6"
          stroke="currentColor"
          strokeOpacity=".88"
          strokeWidth="1.65"
          strokeLinecap="round"
        />
      </svg>
      <span className="stop-sq" aria-hidden="true" />
    </button>
  );
}

function Said({
  caption,
  text,
  mono = false,
  folds = false,
}: {
  caption: string;
  text: string;
  mono?: boolean;
  folds?: boolean;
}) {
  const { t } = useTranslation("thread");
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const copyTimer = useRef<number | null>(null);
  const long = folds && (text.split("\n").length > 3 || text.length > 140);

  const handleCopy = () => {
    void navigator.clipboard?.writeText(text).catch(() => {});
    setCopied(true);
    if (copyTimer.current) window.clearTimeout(copyTimer.current);
    copyTimer.current = window.setTimeout(() => setCopied(false), 1400);
  };

  return (
    <div className="bgc-said">
      <div className="bgc-said-h">
        <span className="bgc-cap">{caption}</span>
        <button type="button" className="bgc-copy" onClick={handleCopy} title={t("background.copy")}>
          <Icon name={copied ? "check" : "copy"} />
        </button>
      </div>
      <p className={`sel-text ${mono ? "mono" : ""} ${long && !open ? "clamp" : ""}`}>{text}</p>
      {long && (
        <button type="button" className="link" onClick={() => setOpen(!open)}>
          {open ? t("background.collapse") : t("background.expand")}
        </button>
      )}
    </div>
  );
}
