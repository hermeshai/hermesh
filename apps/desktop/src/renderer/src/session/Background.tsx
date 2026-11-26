import { useLayoutEffect, useRef, useState } from "react";
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
import { TitleShine } from "../runningMotion";
import { useNow, useStore } from "../store";
import { formatTaskTiming, stripAnsi, taskLabel, taskSecondary, type TimeTranslate } from "./backgroundModel";
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

/**
 * The background card: in the info card's place, over the conversation's top right under its own
 * bar, the same glass card and width, so the tasks read as the session's own. Tasks are rows like
 * the sidebar's sessions: a working one's title shimmers and its time gives way on hover to stop;
 * a stopped or failed one has a dot. Clicking a row opens its execution in the card's place,
 * following the end while it runs; back returns to the list.
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
  const running = tasks.filter((x) => x.status === BackgroundTaskStatus.RUNNING).length;
  const now = useNow(running > 0 ? 1000 : 30_000);
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
  const sel = st.bgTask ? tasks.find((x) => x.taskId === st.bgTask) : undefined;

  return (
    <div className="insp-col bgc-stack" role="complementary" aria-label={t("background.title")} data-testid="background-panel">
      <section className="insp bgc">
        {sel ? (
          <Process key={sel.taskId} bt={sel} now={now} onStop={onStop} />
        ) : (
          <>
            <div className="bgc-h">
              <h3>{t("background.title")}</h3>
              {running > 0 && (
                <button
                  type="button"
                  className="bgc-stopall"
                  data-testid="background-stop-all"
                  onClick={() => onStop("")}
                >
                  {t("background.stopAll")}
                </button>
              )}
            </div>
            <div className="bgc-sub">
              <span>{sub}</span>
            </div>
            <div className="bgc-list">
              {tasks.map((bt) => (
                <Row key={bt.taskId} bt={bt} now={now} onStop={onStop} />
              ))}
            </div>
          </>
        )}
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

function Row({
  bt,
  now,
  onStop,
}: {
  bt: BackgroundTask;
  now: Date;
  onStop: (taskId?: string) => void;
}) {
  const st = useStore();
  const kindLabel = useKindLabel();
  const statusLabel = useStatusLabel();
  const isRunning = bt.status === BackgroundTaskStatus.RUNNING;
  const label = taskLabel(bt);
  const second = taskSecondary(bt, kindLabel(bt.kind));
  const open = () => st.set({ bgTask: bt.taskId });
  return (
    <div
      className={`srow ${isRunning ? "has-act" : ""}`}
      role="button"
      tabIndex={0}
      title={statusLabel(bt.status)}
      data-testid={`bg-task-${bt.taskId}`}
      onClick={open}
      onKeyDown={(e) =>
        (e.key === "Enter" || e.key === " ") &&
        e.target === e.currentTarget &&
        (e.preventDefault(), open())
      }
    >
      <span className="smain">
        <span className="sl1">
          <span className={`stitle ${isRunning ? "run" : ""}`}>
            {label}
            {isRunning && <TitleShine text={label} />}
          </span>
          <Meta bt={bt} now={now} />
          {isRunning && (
            <StopButton
              bt={bt}
              onStop={onStop}
              className="rowact stop"
              testId={`bg-task-stop-${bt.taskId}`}
            />
          )}
        </span>
        <span className="sl2">
          <span className={`sl2t ${second.mono ? "k" : ""}`}>{second.text}</span>
        </span>
      </span>
    </div>
  );
}

function StopButton({
  bt,
  onStop,
  className,
  testId,
}: {
  bt: BackgroundTask;
  onStop: (taskId?: string) => void;
  className: string;
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
      className={className}
      title={hint}
      aria-label={t("background.stop")}
      data-testid={testId}
      onClick={(e) => {
        e.stopPropagation();
        onStop(bt.taskId);
      }}
    >
      <Icon name="stop" className="fill" />
    </button>
  );
}

function Meta({ bt, now }: { bt: BackgroundTask; now: Date }) {
  const statusLabel = useStatusLabel();
  const dot =
    bt.status === BackgroundTaskStatus.STOPPED
      ? "w"
      : bt.status === BackgroundTaskStatus.FAILED
        ? "c"
        : "";
  return (
    <span className={`smeta ${dot}`}>
      {dot ? (
        <>
          <span className={`sdot ${dot}`} />
          {statusLabel(bt.status)}
        </>
      ) : (
        taskTime(bt, now)
      )}
    </span>
  );
}

/** The selected task's execution in the card's place: back to the list, stop while running, and its live output. */
function Process({
  bt,
  now,
  onStop,
}: {
  bt: BackgroundTask;
  now: Date;
  onStop: (taskId?: string) => void;
}) {
  const { t } = useTranslation("thread");
  const st = useStore();
  const kindLabel = useKindLabel();
  const log = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const cleanOutput = stripAnsi(bt.outputTail);
  const shownSummary = bt.summary.trim() !== "" && bt.summary.trim() !== "stopped by user" ? bt.summary.trim() : "";

  useLayoutEffect(() => {
    const el = log.current;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [cleanOutput, shownSummary, bt.status]);

  const label = taskLabel(bt);
  const isRunning = bt.status === BackgroundTaskStatus.RUNNING;
  const briefText = bt.kind === BackgroundTaskKind.SUBAGENT ? (bt.command.trim() || bt.title.trim()) : bt.command.trim();
  const took = bt.status === BackgroundTaskStatus.COMPLETED ? taskTime(bt, now) : "";

  return (
    <>
      <div className="bgc-h">
        <button
          type="button"
          className="bgc-btn"
          title={t("background.back")}
          aria-label={t("background.back")}
          data-testid="background-back"
          onClick={() => st.set({ bgTask: null })}
        >
          <Icon name="chevL" />
        </button>
        <h3 className="bgc-name" title={label}>{label}</h3>
        {isRunning && (
          <StopButton
            bt={bt}
            onStop={onStop}
            className="bgc-btn stop"
            testId={`bg-task-stop-${bt.taskId}`}
          />
        )}
      </div>
      <div className="bgc-sub">
        <span>{kindLabel(bt.kind)}</span>
        <Meta bt={bt} now={now} />
      </div>
      <div
        className="bgc-log"
        ref={log}
        onScroll={(e) => {
          const el = e.currentTarget;
          follow.current = el.scrollTop + el.clientHeight > el.scrollHeight - 40;
        }}
      >
        {briefText !== "" && (
          <Said
            caption={t(bt.kind === BackgroundTaskKind.SUBAGENT ? "background.brief" : "background.command")}
            text={briefText}
            mono={bt.kind !== BackgroundTaskKind.SUBAGENT}
            folds
          />
        )}
        {cleanOutput !== "" && (
          <TerminalOutput text={bt.outputTail} follow={isRunning} className="bgc-out sel-text" testId={`bg-task-output-${bt.taskId}`} />
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
          <p className="bgc-note int">{t("background.stoppedNote")}</p>
        )}
        {bt.status === BackgroundTaskStatus.FAILED && (
          <p className="bgc-note bad">
            {bt.exitCode !== undefined && bt.exitCode !== 0
              ? t("background.failedExit", { code: bt.exitCode })
              : t("background.failedNote")}
          </p>
        )}
      </div>
    </>
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
  const long = folds && (text.split("\n").length > 3 || text.length > 120);
  return (
    <div className="bgc-said">
      <span className="bgc-cap">{caption}</span>
      <p className={`sel-text ${mono ? "mono" : ""} ${long && !open ? "clamp" : ""}`}>{text}</p>
      {long && (
        <button type="button" className="link" onClick={() => setOpen(!open)}>
          {open ? t("background.collapse") : t("background.expand")}
        </button>
      )}
    </div>
  );
}
