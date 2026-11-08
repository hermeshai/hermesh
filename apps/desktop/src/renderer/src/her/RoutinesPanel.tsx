import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { conversation, routineGroups, routineLine, runLate, runRows, runTime, runTook, totals, viewOfRun, type RunRow, type SessionView } from "@hermesh/her-client";
import type { BotRoutineRunResult, BotSummary, RoutineDetail, RoutineRunInfo, RoutineSummary, SessionInfo } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { Markdown } from "../session/Markdown";
import { useNow, useStore } from "../store";
import { useHer, type RoutinesRoute } from "./context";
import { durationText, momentText, tokensText } from "./format";
import { Process } from "./Process";

/** A page of a routine's runs. */
const PAGE = 20;

/**
 * An assistant's routines, beside its conversation in
 * the place its settings take: the list, a routine, and one of its runs. Only this assistant's. New
 * routines and changes to them go through the chat: these put a sentence in the composer. They run on
 * the assistant's machine.
 */
export function RoutinesPanel({ bot, view, route, onDraft, onShowBubble, onClose }: {
  bot: BotSummary;
  view: SessionView | null;
  route: RoutinesRoute;
  onDraft: (text: string) => void;
  onShowBubble: (run: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation("her");
  const her = useHer();
  const routines = bot.routines ?? [];
  const routine = routines.find((r) => r.id === route.routine);
  const go = (next: RoutinesRoute) => her.setPanel({ kind: "routines", route: next });
  // Deleted, or the panel stayed while another assistant came up: back to the list.
  useEffect(() => {
    if (route.routine && !routine) go({});
  }, [route.routine, routine === undefined]);
  const close = (
    <button className="tb" title={t("panel.close")} aria-label={t("panel.close")} onClick={onClose}>
      <Icon name="x" />
    </button>
  );
  if (routine && route.run) {
    return (
      <Shell title={t("routines.run.title")} back={() => go({ routine: routine.id })} close={close}>
        <RunPage key={route.run} bot={bot} routine={routine} id={route.run} info={view?.info} onShowBubble={onShowBubble} />
      </Shell>
    );
  }
  if (routine) {
    return (
      <Shell title={routine.name} back={() => go({})} close={close}>
        <RoutinePage key={routine.id} bot={bot} routine={routine} onOpenRun={(run) => go({ routine: routine.id, run })} onDraft={onDraft} />
      </Shell>
    );
  }
  const create = () => onDraft(t("routines.newDraft"));
  return (
    <Shell
      title={t("routines.title")}
      close={close}
      action={
        <button className="tb" title={t("routines.new")} aria-label={t("routines.new")} onClick={create}>
          <Icon name="plus" />
        </button>
      }
    >
      <ListPage bot={bot} routines={routines} onOpen={(id) => go({ routine: id })} onCreate={create} />
    </Shell>
  );
}

function Shell({ title, back, action, close, children }: { title: string; back?: () => void; action?: ReactNode; close: ReactNode; children: ReactNode }) {
  const { t } = useTranslation("her");
  return (
    <aside className="her-panel rtn" aria-label={t("routines.title")}>
      <div className={`her-panel-top drag rtn-top ${back ? "rtn-sub" : ""}`}>
        {back && (
          <button className="tb" title={t("routines.back")} aria-label={t("routines.back")} onClick={back}>
            <Icon name="chevL" />
          </button>
        )}
        <span className="rtn-tt">{title}</span>
        {action}
        {close}
      </div>
      <div className="her-panel-body">{children}</div>
    </aside>
  );
}

function ListPage({ bot, routines, onOpen, onCreate }: { bot: BotSummary; routines: RoutineSummary[]; onOpen: (id: string) => void; onCreate: () => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  if (bot.routines_error) {
    return (
      <div className="rtn-empty">
        <b>{t("routines.unavailable")}</b>
        {bot.routines_error}
      </div>
    );
  }
  if (routines.length === 0) {
    return (
      <div className="rtn-empty">
        <b>{t("routines.empty.title")}</b>
        {t("routines.empty.body", { name: bot.name })}
        <br />
        <button className="btn" onClick={onCreate}>
          <Icon name="plus" />
          {t("routines.new")}
        </button>
      </div>
    );
  }
  const { active, paused, finished } = routineGroups(routines);
  const group = (title: string | null, rows: RoutineSummary[]) =>
    rows.length > 0 && (
      <section>
        {title && <h3 className="fhead">{title}</h3>}
        <div className="rtn-rows">
          {rows.map((r) => (
            <RoutineRow key={r.id} bot={bot} routine={r} onOpen={() => onOpen(r.id)} />
          ))}
        </div>
      </section>
    );
  return (
    <div className="form">
      {group(null, active)}
      {group(t("routines.paused"), paused)}
      {group(t("routines.finished"), finished)}
      <p className="set-note">{t("routines.note", { name: her.machine.machineName })}</p>
    </div>
  );
}

function RoutineRow({ bot, routine, onOpen }: { bot: BotSummary; routine: RoutineSummary; onOpen: () => void }) {
  return (
    <div className={`rtn-row ${routine.state !== "active" ? "off" : ""}`} role="button" tabIndex={0} onClick={onOpen} onKeyDown={(e) => e.key === "Enter" && onOpen()}>
      <div className="rtn-t">
        <div className="rtn-n">{routine.name}</div>
        <SecondLine routine={routine} />
      </div>
      <EnabledSwitch bot={bot} routine={routine} />
    </div>
  );
}

function SecondLine({ routine }: { routine: RoutineSummary }) {
  const { t } = useTranslation("her");
  const line = routineLine(routine);
  const now = useNow(line.kind === "running" ? 1000 : 60_000);
  switch (line.kind) {
    case "running":
      return (
        <div className="rtn-d">
          <span className="shimmer">{line.since ? t("routines.runningLine", { time: durationText(now.getTime() - line.since.getTime()) }) : t("routines.running")}</span>
        </div>
      );
    case "failed":
      return <div className="rtn-d warn">{t("routines.lastFailed", { time: momentText(line.at, now) })}</div>;
    case "next":
      return <div className="rtn-d">{t("routines.whenNext", { when: line.when, time: momentText(line.at, now) })}</div>;
    case "when":
      return <div className="rtn-d">{line.when}</div>;
  }
}

/** Paused and resumed right here; a one-off that has run has nothing left to switch. */
function EnabledSwitch({ bot, routine }: { bot: BotSummary; routine: RoutineSummary }) {
  const { t } = useTranslation("her");
  const her = useHer();
  if (routine.state === "finished") return null;
  const on = routine.state === "active";
  return (
    <button
      className={`sw ${on ? "on" : ""}`}
      role="switch"
      aria-checked={on}
      aria-label={t("routines.enabled")}
      onClick={(e) => {
        e.stopPropagation();
        void her.run(() => her.call("bots/routine_set_enabled", { bot_id: bot.id, routine_id: routine.id, enabled: !on }));
      }}
    />
  );
}

function RoutinePage({ bot, routine, onOpenRun, onDraft }: { bot: BotSummary; routine: RoutineSummary; onOpenRun: (run: string) => void; onDraft: (text: string) => void }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const now = useNow(routine.running ? 1000 : 60_000);
  const [detail, setDetail] = useState<RoutineDetail | null>(null);
  const [runs, setRuns] = useState<RoutineRunInfo[] | null>(null);
  const [more, setMore] = useState(false);
  const [unfolded, setUnfolded] = useState<ReadonlySet<string>>(new Set());
  const ask = { bot_id: bot.id, routine_id: routine.id };
  // The row changes with every run: that is when the records have something new.
  const changed = JSON.stringify(routine);
  useEffect(() => {
    let gone = false;
    her.call("bots/routine", ask).then((r) => gone || setDetail(r.routine), () => {});
    her.call("bots/routine_runs", { ...ask, limit: PAGE }).then(
      (r) => {
        if (gone) return;
        setRuns(r.runs);
        setMore(r.runs.length === PAGE);
      },
      () => {},
    );
    return () => {
      gone = true;
    };
  }, [changed, her.env.rpc]);
  const earlier = () =>
    void her.run(async () => {
      const page = await her.call("bots/routine_runs", { ...ask, limit: PAGE, before: runs?.at(-1)?.id });
      setRuns([...(runs ?? []), ...page.runs]);
      setMore(page.runs.length === PAGE);
    });
  const remove = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("routines.delete.title", { name: routine.name }),
        message: t("routines.delete.message"),
        button: t("routines.delete.button"),
        run: async () => void (await her.call("bots/routine_delete", ask)),
        done: t("routines.delete.done", { name: routine.name }),
      },
    });
  const upcoming = detail?.upcoming ?? [];
  const line = routineLine(routine);
  return (
    <div className="form">
      <section>
        <div className="fgroup rtn-g">
          {routine.state !== "finished" && (
            <div className="frow">
              <span className="fl">{t("routines.enabled")}</span>
              <div className="fv">
                <EnabledSwitch bot={bot} routine={routine} />
              </div>
            </div>
          )}
          {line.kind === "running" && line.since && (
            <div className="frow">
              <span className="fl">{t("routines.runningFor")}</span>
              <div className="fv rtn-num">{durationText(now.getTime() - line.since.getTime())}</div>
            </div>
          )}
          <div className="frow">
            <span className="fl">{t("routines.when")}</span>
            <div className="fv">{routine.when}</div>
          </div>
          {upcoming.length > 0 && (
            <div className="frow">
              <span className="fl">{t("routines.upcoming")}</span>
              <div className="fv">
                <span>
                  {upcoming.map((at, i) => (
                    <span className="rtn-at" key={at}>
                      {momentText(new Date(at), now)}
                      {i < upcoming.length - 1 && t("routines.listSeparator")}
                    </span>
                  ))}
                </span>
              </div>
            </div>
          )}
        </div>
      </section>
      <section>
        <h3 className="fhead">{t("routines.prompt")}</h3>
        <div className="rtn-prompt sel-text">{detail?.prompt ?? ""}</div>
      </section>
      <section className="rtn-acts">
        {routine.running ? (
          <button className="btn" onClick={() => void her.run(() => her.call("bots/routine_stop", ask))}>
            <Icon name="stop" />
            {t("routines.stop")}
          </button>
        ) : (
          <button className="btn" onClick={() => void her.run(() => her.call("bots/routine_run_now", ask))}>
            <Icon name="play" />
            {t("routines.runNow")}
          </button>
        )}
        <button className="btn" onClick={() => onDraft(t("routines.editDraft", { name: routine.name }))}>
          {t("routines.edit")}
        </button>
        <button className="btn danger" onClick={remove}>
          {t("routines.delete.button")}
        </button>
        <p className="set-note">{t("routines.runNowNote")}</p>
      </section>
      <section>
        <h3 className="fhead">{t("routines.runs")}</h3>
        {runs && runs.length > 0 ? (
          <div className="rtn-rows">
            {runRows(runs, unfolded).map((row) => (
              <RunRowView key={row.kind === "run" ? row.run.id : row.key} row={row} bot={bot} newest={row.kind === "run" && row.run.id === runs[0].id} onOpen={onOpenRun} onUnfold={(key) => setUnfolded(new Set([...unfolded, key]))} />
            ))}
          </div>
        ) : (
          runs && <p className="set-note">{t("routines.noRuns")}</p>
        )}
        {more && (
          <button className="link rtn-more" onClick={earlier}>
            {t("routines.earlier")}
          </button>
        )}
      </section>
    </div>
  );
}

/** What a run came to, in the owner's words. */
function useResult(run: RoutineRunInfo, relaying: boolean): { text: string; warn: boolean } {
  const { t } = useTranslation("her");
  switch (run.outcome) {
    case "reported":
      if (run.delivered) return { text: t("routines.result.delivered"), warn: false };
      return relaying ? { text: t("routines.result.relaying"), warn: false } : { text: t("routines.result.undelivered"), warn: true };
    case "quiet":
      return { text: t("routines.result.quiet"), warn: false };
    case "failed":
      return { text: t("routines.result.failed"), warn: true };
    case "skipped":
      return { text: t("routines.result.skipped"), warn: false };
    case "interrupted":
      return { text: t("routines.result.interrupted"), warn: false };
    default:
      return { text: t("routines.running"), warn: false };
  }
}

function RunRowView({ row, bot, newest, onOpen, onUnfold }: { row: RunRow; bot: BotSummary; newest: boolean; onOpen: (run: string) => void; onUnfold: (key: string) => void }) {
  const { t } = useTranslation("her");
  const now = useNow(60_000);
  if (row.kind === "quiet") {
    const last = row.runs[0];
    const first = row.runs.at(-1)!;
    return (
      <div className="rtn-runrow" role="button" tabIndex={0} onClick={() => onUnfold(row.key)} onKeyDown={(e) => e.key === "Enter" && onUnfold(row.key)}>
        <div className="rtn-t">
          <div className="rtn-rt">{t("routines.result.quietRange", { from: momentText(runTime(first), now), to: momentText(runTime(last), now) })}</div>
          <div className="rtn-d">{t("routines.result.quietCount", { count: row.runs.length })}</div>
        </div>
        <div className="rtn-res">{t("routines.result.quiet")}</div>
      </div>
    );
  }
  return <OneRun run={row.run} relaying={newest && !!bot.typing} onOpen={onOpen} />;
}

function OneRun({ run, relaying, onOpen }: { run: RoutineRunInfo; relaying: boolean; onOpen: (run: string) => void }) {
  const { t } = useTranslation("her");
  const now = useNow(run.outcome ? 60_000 : 1000);
  const result = useResult(run, relaying);
  const took = runTook(run, now);
  const late = runLate(run);
  const skipped = run.outcome === "skipped";
  const said = run.outcome === "failed" ? run.report : skipped ? t("routines.result.skippedWhy") : run.note;
  const second = [took !== null ? t("routines.ranFor", { time: durationText(took) }) : "", late !== null ? t("routines.late", { time: durationText(late) }) : "", said ?? ""].filter(Boolean).join(" · ");
  // A skipped run did nothing, so there is nothing to open.
  const open = skipped ? undefined : () => onOpen(run.id);
  return (
    <div className={`rtn-runrow ${skipped ? "still" : ""}`} role={open ? "button" : undefined} tabIndex={open ? 0 : undefined} onClick={open} onKeyDown={(e) => e.key === "Enter" && open?.()}>
      <div className="rtn-t">
        <div className="rtn-rt">{momentText(runTime(run), now)}</div>
        {second && <div className="rtn-d">{second}</div>}
      </div>
      <div className={`rtn-res ${result.warn ? "warn" : ""}`}>{result.text}</div>
    </div>
  );
}

/** One run: how it went, what it said, and its steps, which answer "why did it not tell me". */
function RunPage({ bot, routine, id, info, onShowBubble }: { bot: BotSummary; routine: RoutineSummary; id: string; info: SessionInfo | undefined; onShowBubble: (run: string) => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const [found, setFound] = useState<BotRoutineRunResult | null>(null);
  const now = useNow(found && !found.run.outcome ? 1000 : 60_000);
  const changed = JSON.stringify(routine.last_run);
  useEffect(() => {
    let gone = false;
    her.call("bots/routine_run", { bot_id: bot.id, routine_id: routine.id, run_id: id }).then((r) => gone || setFound(r), () => {});
    return () => {
      gone = true;
    };
  }, [id, changed, her.env.rpc]);
  const steps = useMemo(() => {
    if (!found) return [];
    const all = conversation(viewOfRun(info ?? NO_INFO, id, found.transcript)).filter((s) => s.kind === "work" || s.kind === "text");
    // Its last words are its report or what it found, told above.
    return all.at(-1)?.kind === "text" ? all.slice(0, -1) : all;
  }, [found, id]);
  if (!found) return <div className="empty"><span className="mini-spin" /></div>;
  const run = found.run;
  const late = runLate(run);
  const took = runTook(run, now);
  const tokens = run.usage ? totals(run.usage).tokens : 0;
  return (
    <div className="form">
      <section>
        <div className="fgroup rtn-g">
          <RunResult run={run} relaying={!!bot.typing} onShowBubble={onShowBubble} />
          {late !== null && (
            <div className="frow">
              <span className="fl">{t("routines.run.scheduled")}</span>
              <div className="fv">{`${momentText(new Date(run.scheduled), now)} · ${t("routines.late", { time: durationText(late) })}`}</div>
            </div>
          )}
          <div className="frow">
            <span className="fl">{t("routines.run.started")}</span>
            <div className="fv">{run.started ? momentText(new Date(run.started), now) : "—"}</div>
          </div>
          <div className="frow">
            <span className="fl">{t("routines.run.took")}</span>
            <div className="fv">{took === null ? "—" : durationText(took)}</div>
          </div>
          <div className="frow">
            <span className="fl">{t("routines.run.usage")}</span>
            <div className="fv">{tokens > 0 ? `${tokensText(tokens)} tokens` : "—"}</div>
          </div>
        </div>
      </section>
      {run.outcome === "quiet" && run.note && (
        <section>
          <h3 className="fhead">{t("routines.run.found")}</h3>
          <div className="rtn-prompt sel-text">{run.note}</div>
        </section>
      )}
      {run.report && (
        <section>
          <h3 className="fhead">{run.outcome === "failed" ? t("routines.run.why") : t("routines.run.report")}</h3>
          <div className="rtn-prompt md sel-text">
            <Markdown text={run.report} />
          </div>
        </section>
      )}
      {steps.length > 0 && (
        <section>
          <h3 className="fhead">{t("routines.run.steps")}</h3>
          <Process steps={steps} />
        </section>
      )}
    </div>
  );
}

function RunResult({ run, relaying, onShowBubble }: { run: RoutineRunInfo; relaying: boolean; onShowBubble: (run: string) => void }) {
  const { t } = useTranslation("her");
  const now = useNow(60_000);
  const result = useResult(run, relaying);
  return (
    <div className="frow">
      <span className="fl">{t("routines.run.result")}</span>
      <div className="fv">
        <span className={`rtn-res ${result.warn ? "warn" : ""}`}>
          {result.text}
          {run.delivered ? ` ${momentText(new Date(run.delivered), now)}` : ""}
        </span>
        {run.delivered && (
          <button className="link" onClick={() => onShowBubble(run.id)}>
            {t("routines.run.showInChat")}
          </button>
        )}
      </div>
    </div>
  );
}

/** For a run's steps while the assistant's conversation is still loading: nothing shown depends on it. */
const NO_INFO: SessionInfo = { session_id: "", cwd: "", session_file: "", model: "", thinking: "off", permission_mode: "manual", context_window: 0, archived: false };
