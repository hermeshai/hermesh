import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Client } from "@hermesh/proto/local";
import { FailureKind, SessionStatus, type Failure, type Session, type SessionEvent, type ToolCall, type ToolResult } from "@hermesh/proto/session";
import {
  agoText, clockText, elapsedSeconds, codingAgentTitle, createdDate, currentTurn, failureAdvice, failureTitle, groupSteps, isActive, isArchivable, isFailed,
  isInterruption, isInterruptible, pendingQuestion, phaseText, resolutionTitle, statusTitle, stepSummary, toolObject, toolSummary, toolVerb, transcript,
  turnStartedAt, turnTimeLine, type Step,
} from "@hermesh/core";
import { AgentLogo, agentName } from "@hermesh/ui";
import { StatusDot } from "../components";
import { Icon } from "../icons";
import { archive, grantLabel, unarchive } from "../Sidebar";
import { useNow, useStore } from "../store";
import { interrupt } from "./actions";
import { Code } from "./Code";
import { DraftMarkdown, Markdown } from "./Markdown";
import { partialOutput, useToolOutput } from "./output";
import { QuestionPanel } from "./Question";

/**
 * The conversation, newest at the bottom, ending with the reply the agent is
 * still writing (draft); while a turn is live, the control area sits over
 * its bottom edge.
 */
export function Chat({ s, events, draft }: { s: Session; events: SessionEvent[]; draft: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const grant = grantLabel(st, s);
  const running = isActive(s);
  const items = useMemo(() => groupSteps(transcript(events, running)), [events, running]);
  const live = isInterruptible(s);
  const scroll = useRef<HTMLDivElement>(null);
  const [ctlHeight, setCtlHeight] = useState(0);

  // Follow the conversation: open at its end, and stay there as it grows unless the person scrolled up.
  const atEnd = useRef(true);
  useLayoutEffect(() => {
    const el = scroll.current;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
  }, [items.length, draft.length, ctlHeight, s.status]);
  const onScroll = () => {
    const el = scroll.current;
    if (el) atEnd.current = el.scrollTop + el.clientHeight > el.scrollHeight - 80;
  };

  const failure = s.progress?.lastFailure;
  return (
    <>
      <div className="cscroll" ref={scroll} onScroll={onScroll}>
        <div className="dbody" style={live ? { paddingBottom: ctlHeight + 40 } : undefined}>
          {s.status === SessionStatus.ARCHIVED && (
            <div className="anote">
              <Icon name="archive" className="sm" />
              {t("chat.archived")}
              <button className="link" onClick={() => unarchive(st, s)}>{t("chat.unarchive")}</button>
            </div>
          )}
          <div className="tr" role="log" aria-label={t("chat.log")}>
            {items.map((it, i) => (
              <Row
                key={String(it.seq)}
                it={it}
                sessionId={s.sessionId}
                failure={isFailed(s) ? s.progress?.lastFailure : undefined}
                clients={st.clients}
                grant={grant}
                previous={previousTurn(items, i)}
              />
            ))}
            {draft !== "" && (
              <div className="am">
                <DraftMarkdown text={draft} />
              </div>
            )}
          </div>
          {isFailed(s) && (failure || s.error !== "") && (
            <Failed s={s} title={failure ? failureTitle(failure.kind) : t("chat.failed")} message={failure?.message ?? s.error} advice={failure ? failureAdvice(failure) : ""} grant={grant} />
          )}
          {s.status === SessionStatus.IDLE && !isFailed(s) && !isInterruption(failure) && items.length > 0 && (
            <div className="livebar" style={{ justifyContent: "center", paddingTop: 18 }}>
              <Icon name="check" className="sm" />
              <span>{t("chat.turnDone", { grant })}</span>
            </div>
          )}
        </div>
      </div>
      {live && <ControlArea s={s} grant={grant} events={events} onHeight={setCtlHeight} />}
    </>
  );
}

/** When the turn before the one starting at items[i] started (for its time line). */
function previousTurn(items: Step[], i: number): Date | undefined {
  if (items[i].kind !== "turn") return undefined;
  for (let j = i - 1; j >= 0; j--) {
    const it = items[j];
    if (it.kind === "turn") return it.at;
  }
  return undefined;
}

type RowProps = { it: Step; sessionId: string; failure: Failure | undefined; clients: Client[]; grant: string; previous: Date | undefined };

/**
 * A row renders again only when what it shows changed: the conversation's
 * items are made anew from the events each time, but an event never changes.
 */
const Row = memo(
  RowView,
  (a, b) =>
    sameStep(a.it, b.it) && a.sessionId === b.sessionId && (a.failure === b.failure || sameFailure(a.failure, b.failure)) &&
    a.clients === b.clients && a.grant === b.grant && a.previous?.getTime() === b.previous?.getTime(),
);

function sameStep(a: Step, b: Step): boolean {
  if (a.kind !== b.kind || a.seq !== b.seq) return false;
  switch (a.kind) {
    case "turn":
      return a.at?.getTime() === (b as typeof a).at?.getTime();
    case "tools": {
      const c = (b as typeof a).calls;
      return a.calls.length === c.length && a.calls.every((x, i) => x.call === c[i].call && x.result === c[i].result && x.running === c[i].running);
    }
    case "question":
      return a.resolution === (b as typeof a).resolution;
    default:
      return true;
  }
}

function RowView({ it, sessionId, failure, clients, grant, previous }: RowProps) {
  const { t } = useTranslation("session");
  switch (it.kind) {
    case "turn":
      return <TimeLine at={it.at} previous={previous} />;
    case "user":
      // The one who started the session speaks on the right; no name (the card says who).
      return (
        <div className="um">
          <div className="bub sel-text">{it.text}</div>
          <Copy text={it.text} at={it.at} />
        </div>
      );
    case "agent":
      return (
        <div className="am">
          <Markdown text={it.text} />
          <Copy text={it.text} />
        </div>
      );
    case "tools":
      return it.calls.length === 1 ? (
        <ToolRow call={it.calls[0].call} result={it.calls[0].result} resultSeq={it.calls[0].resultSeq} running={it.calls[0].running} sessionId={sessionId} />
      ) : (
        <ToolGroup calls={it.calls} sessionId={sessionId} />
      );
    case "question":
      // The pending question is in the control area; a settled one stays in the conversation.
      if (!it.resolution) return null;
      return (
        <div className="tq">
          <Icon name="q" className="sm" /><b>{t("chat.agentAsked")}</b>
          <span className="sel-text">{it.question.items.map((x) => x.text).join(" ")}</span>
          <div className="ans">{resolutionTitle(it.resolution, (by) => { const l = clients.find((c) => c.clientId === by)?.label; return l ? agentName(l) : by; })}</div>
        </div>
      );
    case "turnEnd": {
      const end = it.end;
      if (end.isError && isInterruption(end.failure)) {
        const byOwner = end.failure?.message === "interrupted by owner";
        return <div className="tnote">{t(byOwner ? "chat.interruptedByYou" : "chat.interrupted", { grant })}</div>;
      }
      if (end.isError) {
        // The session's current failure is told once, at the end; an earlier one (a later turn went on) in one quiet line.
        if (sameFailure(failure, end.failure)) return null;
        return <FailLine title={t("chat.turnFailed", { turn: end.turn, title: failureTitle(end.failure?.kind ?? 0) })} message={end.failure?.message ?? ""} />;
      }
      // A turn that went well needs no line: the next message shows it went on.
      return null;
    }
    case "message":
      // A message between the session and another of the owner's agents: who, and what became of it.
      return (
        <div className={it.direction === "in" ? "msg in" : "msg out"}>
          <div className="msg-head">
            <Icon name="mail" className="sm" />
            <b>{t(it.direction === "in" ? "chat.messageFrom" : "chat.messageTo", { name: it.name || it.address })}</b>
            {it.delivered === "queued" && <span className="msg-note">{t("chat.messageQueued")}</span>}
          </div>
          <div className="msg-body sel-text"><Markdown text={it.text} /></div>
        </div>
      );
    case "error": {
      const e = it.error;
      return <FailLine title={e.failure ? t("chat.failedWith", { title: failureTitle(e.failure.kind) }) : t("chat.failed")} message={e.message} />;
    }
  }
}

/** A time line before a turn, for the first one and after a pause. */
function TimeLine({ at, previous }: { at: Date | undefined; previous: Date | undefined }) {
  const now = useNow(60_000);
  const text = at ? turnTimeLine(at, previous, now) : null;
  return text ? <div className="tdiv">{text}</div> : null;
}

/** Under a message on hover: when it was sent (the owner's), and Copy. */
function Copy({ text, at }: { text: string; at?: Date }) {
  const { t } = useTranslation("session");
  const [done, setDone] = useState(false);
  return (
    <div className="msg-act">
      {at && <SentAt at={at} />}
      <button
        className="tb"
        aria-label={t("chat.copy")}
        title={t("chat.copy")}
        onClick={() => navigator.clipboard.writeText(text).then(() => (setDone(true), setTimeout(() => setDone(false), 1500)))}
      >
        <Icon name={done ? "check" : "copy"} className="sm" />
      </button>
    </div>
  );
}

function SentAt({ at }: { at: Date }) {
  const now = useNow(60_000);
  return <span className="msg-time">{agoText(at, now)}</span>;
}

type Calls = Extract<Step, { kind: "tools" }>["calls"];

/** Tool calls in a row as one grey line ("Read 3 files, ran 1 command"), opened to show each; folded until the owner opens it. */
function ToolGroup({ calls, sessionId }: { calls: Calls; sessionId: string }) {
  const { t } = useTranslation("session");
  const [open, setOpen] = useState(false);
  const running = calls.some((c) => c.running);
  return (
    <div className={`tgroup ${open ? "open" : ""}`}>
      <button className="tg-h" aria-expanded={open} onClick={() => setOpen(!open)}>
        {running && <span className="mini-spin" role="img" aria-label={t("chat.inProgress")} />}
        <span>{stepSummary(calls)}</span>
        <Icon name="caret" className="chev" />
      </button>
      {open && (
        <div className="tg-b">
          {calls.map((c) => (
            <ToolRow key={c.call.callId || String(c.seq)} call={c.call} result={c.result} resultSeq={c.resultSeq} running={c.running} sessionId={sessionId} />
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * One tool call: what it did, and its output when opened, read whole then
 * when the conversation holds only its start. Folded until opened, failed
 * ones too; the one running spins.
 */
function ToolRow({ call, result, resultSeq, running, sessionId }: { call: ToolCall; result: ToolResult | undefined; resultSeq?: bigint; running: boolean; sessionId: string }) {
  const { t } = useTranslation("session");
  const denied = call.decision === "deny";
  const failed = denied || result?.isError === true;
  const [open, setOpen] = useState(false);
  const whole = useToolOutput(sessionId, result, resultSeq, open);
  // A machine from before whole outputs kept only their start.
  const note = whole.failed ? t("chat.outputUnreadable") : result && !partialOutput(result) && result.truncated ? t("chat.truncated") : "";
  const output = denied ? call.reason : whole.text !== "" ? whole.text + (note ? `\n${note}` : "") : "";
  return (
    <div className={`tool ${failed ? "err" : ""} ${open && output ? "open" : ""}`}>
      <button className="th" onClick={() => output && setOpen(!open)} aria-expanded={output ? open : undefined}>
        {(running || whole.reading) && <span className="mini-spin" role="img" aria-label={t("chat.inProgress")} />}
        <span className="vb">{toolVerb(call, running)}</span>
        {call.kind === "shell" ? (
          <code className="sm2 cmd" title={call.summary}>
            <Code text={toolSummary(toolObject(call), sessionId)} lang="bash" />
          </code>
        ) : (
          <span className="sm2" title={call.summary}>{toolSummary(toolObject(call), sessionId)}</span>
        )}
        {output && <Icon name="caret" className="chev" />}
      </button>
      {output && <div className="out sel-text">{output}</div>}
    </div>
  );
}

/** Failures whose advice already says to send another message. */
const adviceResumes = new Set([
  FailureKind.RATE_LIMIT,
  FailureKind.PROVIDER_ERROR,
  FailureKind.NETWORK,
  FailureKind.CONTEXT_OVERFLOW,
  FailureKind.RESUME_REJECTED,
  FailureKind.AGENT_CRASHED,
  FailureKind.STALLED,
]);

function sameFailure(a: Failure | undefined, b: Failure | undefined): boolean {
  return !!a && !!b && a.kind === b.kind && a.message === b.message;
}

/**
 * A session that stopped on a failure, in the voice of the steps above it:
 * one red line (what), one grey line (what to do), then Details (the agent's own
 * words, folded) and Archive.
 */
function Failed({ s, title, message, advice, grant }: { s: Session; title: string; message: string; advice: string; grant: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const [open, setOpen] = useState(false);
  const crashed = s.status === SessionStatus.CRASHED;
  const resume = t("chat.resume", { grant });
  const hint = advice !== "" && !adviceResumes.has(s.progress?.lastFailure?.kind ?? FailureKind.UNSPECIFIED) ? t("chat.adviceAndResume", { advice, resume }) : resume;
  return (
    <div className="qfail" role="alert">
      <div className="qf-line">
        <Icon name="info" className="sm" />
        <b>{t(crashed ? "chat.crashedWith" : "chat.failedWith", { title })}</b>
      </div>
      <div className="qf-hint">{hint}</div>
      <div className="qf-act">
        {message !== "" && (
          <button className="link" aria-expanded={open} onClick={() => setOpen(!open)}>
            {t("chat.details")} <Icon name="chevR" className={`sm chev ${open ? "open" : ""}`} />
          </button>
        )}
        {isArchivable(s) && <button className="link" onClick={() => archive(st, s)}>{t("chat.archive")}</button>}
      </div>
      {open && <div className="qf-raw sel-text">{message}</div>}
    </div>
  );
}

/** An earlier failure in the conversation: one red line, its words folded. */
function FailLine({ title, message }: { title: string; message: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="qfail past">
      <button className="qf-line" aria-expanded={message !== "" ? open : undefined} onClick={() => message !== "" && setOpen(!open)}>
        <Icon name="info" className="sm" />
        <span>{title}</span>
        {message !== "" && <Icon name="chevR" className={`sm chev ${open ? "open" : ""}`} />}
      </button>
      {open && <div className="qf-raw sel-text">{message}</div>}
    </div>
  );
}

/** The bottom of a live turn: a pending question above, what runs now and Interrupt below. */
function ControlArea({ s, grant, events, onHeight }: { s: Session; grant: string; events: SessionEvent[]; onHeight: (h: number) => void }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const ref = useRef<HTMLDivElement>(null);
  const q = pendingQuestion(s);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => onHeight(el.offsetHeight));
    ro.observe(el);
    return () => {
      ro.disconnect();
      onHeight(0);
    };
  }, [onHeight]);
  return (
    <div className={`cbar ${q ? "q" : ""}`} role="region" aria-label={t("control.label")} ref={ref}>
      {q && <QuestionPanel key={q.questionId} s={s} q={q} />}
      <div className="cbar-row">
        <LiveStatus s={s} grant={grant} events={events} />
        <span className="ctl-sep" />
        <button className="ctl-btn" title={t("control.interruptTitle")} onClick={() => interrupt(st, s)}>
          <Icon name="stop" />
          {t("control.interrupt")}
          <span className="kbd">⌘.</span>
        </button>
      </div>
    </div>
  );
}

function LiveStatus({ s, grant, events }: { s: Session; grant: string; events: SessionEvent[] }) {
  const { t } = useTranslation("session");
  const now = useNow(1000);
  switch (s.status) {
    case SessionStatus.WAITING_INPUT:
      return (
        <>
          <StatusDot s={s} />
          <b>{t("waiting")}</b>
          <span className="ph">{t("control.waitingDetail", { grant, turn: currentTurn(s) })}</span>
        </>
      );
    case SessionStatus.RUNNING:
      return (
        <>
          <span className="live-logo"><AgentLogo agent={s.agent} size={18} /></span>
          <b>{t("control.working", { agent: codingAgentTitle(s.agent) })}</b>
          <span className="el">{clockText(elapsedSeconds(turnStart(s, events), now))}</span>
        </>
      );
    default:
      return (
        <>
          <span className="live-logo"><AgentLogo agent={s.agent} size={18} /></span>
          <b>{statusTitle(s.status)}</b>
          {phaseText(s) !== statusTitle(s.status) && <span className="ph">{phaseText(s)}</span>}
        </>
      );
  }
}

/** When the turn in progress started: from its events, else when the session was created. */
export function turnStart(s: Session, events: SessionEvent[]): Date {
  return turnStartedAt(events) ?? createdDate(s);
}

