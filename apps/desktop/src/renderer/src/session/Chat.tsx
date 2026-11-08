import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import type { Client, Machine } from "@hermesh/proto/local";
import { FailureKind, SessionStatus, type Failure, type Session, type SessionEvent, type ToolCall, type ToolResult } from "@hermesh/proto/session";
import {
  agoText, clockText, dateOf, elapsedSeconds, codingAgentTitle, createdDate, currentTurn, failureAdvice, failureHeadline, failureTitle, groupSteps, isActive, isArchivable, isFailed,
  interrupter, isInterruption, isInterruptible, pendingQuestion, phaseText, replyEnds, resolutionTitle, statusTitle, stepSummary, toolObject, toolSummary, toolVerb, transcript,
  turnStartedAt, turnTimeLine, type Step,
} from "@hermesh/core";
import { AgentLogo, Mark, agentName } from "@hermesh/ui";
import { StatusDot } from "../components";
import { Icon } from "../icons";
import { HEAVY_ROWS, releaseAfterLeaving } from "../memory";
import { keys } from "../platform";
import { archive, grantLabel, machineName, unarchive } from "../Sidebar";
import { useNow, useStore } from "../store";
import { interrupt } from "./actions";
import { Code } from "./Code";
import { Markdown } from "./Markdown";
import { partialOutput, useToolOutput } from "./output";
import { QuestionPanel } from "./Question";
import { ContentScope } from "./scope";
import { useRowState } from "./useRowState";
import { useVirtualRows, type VirtualRow } from "./useVirtualRows";

/**
 * The conversation, newest at the bottom, ending with the reply the agent is
 * still writing (draft); while a turn is live, the control area sits over
 * its bottom edge. Only the rows near what is in sight are drawn
 * (useVirtualRows, docs/desktop-performance.md §7).
 */
export function Chat({ s, events, draft }: { s: Session; events: SessionEvent[]; draft: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const grant = grantLabel(st, s);
  const running = isActive(s);
  const items = useMemo(() => groupSteps(transcript(events, running)), [events, running]);
  const ends = useMemo(() => replyEnds(items, running), [items, running]);
  const live = isInterruptible(s);
  const scroll = useRef<HTMLDivElement>(null);
  // The scroller and the list as elements too, for the window (useVirtualRows): set once, by refs that do not change.
  const [scroller, setScroller] = useState<HTMLDivElement | null>(null);
  const [list, setList] = useState<HTMLDivElement | null>(null);
  const scrollRef = useCallback((el: HTMLDivElement | null) => {
    scroll.current = el;
    setScroller(el);
  }, []);
  const [ctlHeight, setCtlHeight] = useState(0);

  // The session's current failure is told once, at the end; the rows leave it out.
  const failure = s.progress?.lastFailure;
  const current = isFailed(s) ? failure : undefined;
  // Each row with what it shows worked out from all the items; rows that would draw nothing are left out, as an
  // empty row in the list would still take a gap. The reply being written is the row its stored message will be
  // (the next event's), so finishing it keeps its code, tables and diagrams as they are.
  const settled = useMemo(() => rowsOf(items, ends, current), [items, ends, current?.kind, current?.message]);
  const next = (events.at(-1)?.seq ?? 0n) + 1n;
  const rows = useMemo(() => (draft === "" ? settled : [...settled, draftRow(next, draft)]), [settled, draft, next]);
  const virtual = useVirtualRows({ scroller, list, rows, initial: rowHeights });

  // Follow the conversation: open at its end, and stay there as it grows unless the person scrolled up;
  // scrolled well up, a button takes them back down.
  const atEnd = useRef(true);
  const lastTop = useRef(0);
  // When the person last acted on the conversation itself. A held pointer (the scrollbar) counts until it lets go.
  const touched = useRef(-Infinity);
  const touch = () => {
    touched.current = performance.now();
  };
  const hold = () => {
    touched.current = Infinity;
    window.addEventListener("pointerup", touch, { once: true });
    window.addEventListener("pointercancel", touch, { once: true });
  };
  const [away, setAway] = useState(false);
  useLayoutEffect(() => {
    const el = scroll.current;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
    if (el) lastTop.current = el.scrollTop;
  }, [rows, ctlHeight, s.status]);
  // Fonts, formulas, diagrams and tables can resize without a new event. Reading above the end, the row at the top
  // of the view stays where it is; at the end, the end stays in view.
  const anchor = useRef<{ key: string; top: number } | null>(null);
  const rememberAnchor = () => {
    const el = scroll.current;
    if (!el) return;
    // At the end there is nothing to hold, and finding the row measures every one above it, at each scroll that follows.
    if (atEnd.current) {
      anchor.current = null;
      return;
    }
    const top = el.getBoundingClientRect().top;
    const row = [...el.querySelectorAll<HTMLElement>("[data-step-key]")].find((node) => node.getBoundingClientRect().bottom > top);
    anchor.current = row ? { key: row.dataset.stepKey!, top: row.getBoundingClientRect().top - top } : null;
  };
  // Nothing of a conversation that has events yet: they are still on their way, or can't come.
  const unread = items.length === 0 && draft === "" && (s.progress?.lastEventSeq ?? 0n) > 0n;
  useEffect(() => {
    const el = scroll.current;
    const body = el?.firstElementChild;
    if (!el || !body) return;
    let frame = 0;
    const observer = new ResizeObserver(() => {
      // Following the end needs no frame of its own: this runs before the frame it resized is painted.
      if (atEnd.current) {
        el.scrollTop = el.scrollHeight;
        lastTop.current = el.scrollTop;
        return;
      }
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (anchor.current) {
          const row = el.querySelector<HTMLElement>(`[data-step-key="${CSS.escape(anchor.current.key)}"]`);
          if (row) el.scrollTop += row.getBoundingClientRect().top - el.getBoundingClientRect().top - anchor.current.top;
        }
        lastTop.current = el.scrollTop;
        rememberAnchor();
      });
    });
    observer.observe(body);
    return () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
    };
  }, [unread]);
  const onScroll = () => {
    const el = scroll.current;
    if (!el) return;
    const below = el.scrollHeight - el.scrollTop - el.clientHeight;
    // Only the person going up leaves the end. Rows settle their heights after the scroll that followed them,
    // which moves the position with no one touching anything: that goes back to the end.
    if (below < 80) atEnd.current = true;
    else if (el.scrollTop < lastTop.current && performance.now() - touched.current < 1000) atEnd.current = false;
    else if (atEnd.current) el.scrollTop = el.scrollHeight;
    lastTop.current = el.scrollTop;
    setAway(!atEnd.current && below > 240);
    rememberAnchor();
  };
  const toEnd = () => {
    const el = scroll.current;
    if (!el) return;
    atEnd.current = true;
    el.scrollTo({ top: el.scrollHeight, behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  };

  // Leaving a conversation read far back: what it held goes back once the next page settles (memory.ts).
  const held = useRef(0);
  held.current = rows.length;
  useEffect(() => () => void (held.current >= HEAVY_ROWS && releaseAfterLeaving()), []);

  return (
    <ContentScope value={s.sessionId}>
      <div className="cscroll markdown-chat" ref={scrollRef} onScroll={onScroll} onWheel={touch} onTouchMove={touch} onKeyDown={touch} onPointerDown={hold}>
        {unread ? <Unread s={s} /> : <div className="dbody" style={live ? { paddingBottom: ctlHeight + 40 } : undefined}>
          {s.status === SessionStatus.ARCHIVED && (
            <div className="anote">
              <Icon name="archive" className="sm" />
              {t("chat.archived")}
              <button className="link" onClick={() => unarchive(st, s)}>{t("chat.unarchive")}</button>
            </div>
          )}
          <div className="tr" role="log" aria-label={t("chat.log")} ref={setList} data-rows={rows.length}>
            {/* One flat list of keyed rows and spaces: a row keeps its element (and what is selected in it) however the parts change. */}
            {virtual.parts.flatMap((part) =>
              "space" in part
                ? [<div key={`space:${part.at}`} className="vspace" style={{ height: part.space }} aria-hidden="true" />]
                : rows.slice(part.from, part.to).map((r) => (
                    <div key={r.key} className="vrow" data-step-key={r.key} ref={virtual.measure(r.key)}>
                      <Row it={r.it} rowKey={r.key} end={r.end} live={r.live} sessionId={s.sessionId} clients={st.clients} grant={grant} previous={r.previous} />
                    </div>
                  )),
            )}
          </div>
          <Stale s={s} behind={(events.at(-1)?.seq ?? 0n) < (s.progress?.lastEventSeq ?? 0n)} />
          {isFailed(s) && (failure || s.error !== "") && (
            <Failed s={s} failure={failure} grant={grant} />
          )}
          {s.status === SessionStatus.IDLE && !isFailed(s) && !isInterruption(failure) && items.length > 0 && (
            <div className="livebar" style={{ justifyContent: "center", paddingTop: 18 }}>
              <Icon name="check" className="sm" />
              <span>{t("chat.turnDone", { grant })}</span>
            </div>
          )}
        </div>}
      </div>
      <button
        className={`tobottom ${away ? "on" : ""}`}
        style={{ "--dock": `${live ? ctlHeight : 0}px` } as CSSProperties}
        aria-label={t("chat.toBottom")}
        title={t("chat.toBottom")}
        tabIndex={away ? 0 : -1}
        aria-hidden={!away}
        onClick={toEnd}
      >
        <Icon name="arrowDown" />
      </button>
      {live && <ControlArea s={s} grant={grant} events={events} onHeight={setCtlHeight} />}
    </ContentScope>
  );
}

/** How long a conversation may take to come before the page says it can't be read. */
const unreadWait = 15_000;

type Unreadable = { kind: "notConnected" } | { kind: "offline"; machine: Machine } | { kind: "noAnswer" };

/**
 * Why a conversation can't be read now: it is kept on the machine the
 * session runs on, and this Mac isn't connected, or that machine is
 * offline, or (a while after the page opened, still behind) it doesn't
 * answer. The stream keeps trying meanwhile.
 */
function useUnreadable(s: Session, behind: boolean): Unreadable | undefined {
  const st = useStore();
  const now = useNow(1000);
  const [since] = useState(() => Date.now());
  const m = st.machines.find((x) => x.machineId === s.machineId);
  if (m?.local) return undefined;
  if (st.status?.relayConnected === false) return { kind: "notConnected" };
  if (m && !m.online) return { kind: "offline", machine: m };
  return behind && now.getTime() - since >= unreadWait ? { kind: "noAnswer" } : undefined;
}

/** In place of a conversation not read yet: why, or the wait. */
function Unread({ s }: { s: Session }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const why = useUnreadable(s, true);
  const now = useNow(30_000);
  const name = machineName(st, s.machineId);
  if (!why) return <div className="empty"><Mark size={40} variant="pulse" /></div>;
  let title: string, detail: string;
  if (why.kind === "notConnected") {
    title = t("chat.unread.notConnected");
    detail = t("chat.unread.notConnectedDetail", { name });
  } else if (why.kind === "offline") {
    const seen = dateOf(why.machine.lastSeen);
    title = t("chat.unread.offline", { name });
    detail = seen ? t("chat.unread.offlineSeen", { ago: agoText(seen, now) }) : t("chat.unread.offlineDetail");
  } else {
    title = t("chat.unread.noAnswer", { name });
    detail = t("chat.unread.noAnswerDetail");
  }
  return (
    <div className="empty" role="status">
      <Mark size={40} variant="wait" />
      <b>{title}</b>
      <span>{detail}</span>
    </div>
  );
}

/** Under a conversation kept from before that can't be brought up to date now: a quiet line that says why it may be old. */
function Stale({ s, behind }: { s: Session; behind: boolean }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const why = useUnreadable(s, behind);
  if (!why) return null;
  return (
    <div className="livebar stale" role="status" style={{ justifyContent: "center", paddingTop: 18 }}>
      <Icon name="wifi" className="sm" />
      <span>{t(`chat.stale.${why.kind}`, { name: machineName(st, s.machineId) })}</span>
    </div>
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

/** A row of the list: its item, whether it ends a turn's reply, whether it is the reply being written, and the turn before it. */
type ChatRow = VirtualRow & { it: Step; end: boolean; live: boolean; previous: Date | undefined };

/** Each kind's height until rows of it are measured: only how long the scrollbar is depends on it. */
const rowHeights: Record<string, number> = { turn: 30, user: 64, agent: 220, tools: 32, question: 80, turnEnd: 36, message: 100, error: 36 };

/**
 * The rows the conversation draws, in order: the items but those that draw nothing (a time line within half an hour of
 * the turn before, a question still pending, a turn that went well, the session's current failure, told at the end).
 */
function rowsOf(items: Step[], ends: Set<bigint>, current: Failure | undefined): ChatRow[] {
  const out: ChatRow[] = [];
  items.forEach((it, i) => {
    const previous = previousTurn(items, i);
    if (!draws(it, previous, current)) return;
    out.push({ key: String(it.seq), kind: it.kind, it, end: ends.has(it.seq), live: false, previous });
  });
  return out;
}

/** The row of the reply the agent is still writing: keyed as its stored message will be, the event after the last. */
function draftRow(seq: bigint, text: string): ChatRow {
  return { key: String(seq), kind: "agent", it: { kind: "agent", seq, text, at: undefined }, end: false, live: true, previous: undefined };
}

/** Whether an item draws anything (RowView returns null for those that don't). */
function draws(it: Step, previous: Date | undefined, current: Failure | undefined): boolean {
  switch (it.kind) {
    case "turn":
      return !!it.at && turnTimeLine(it.at, previous, new Date()) !== null;
    case "question":
      return !!it.resolution;
    case "turnEnd":
      return it.end.isError && (isInterruption(it.end.failure) || !sameFailure(current, it.end.failure));
    case "error":
      return isInterruption(it.error.failure) || !sameFailure(current, it.error.failure);
    default:
      return true;
  }
}

type RowProps = { it: Step; rowKey: string; end: boolean; live: boolean; sessionId: string; clients: Client[]; grant: string; previous: Date | undefined };

/**
 * A row renders again only when what it shows changed: the conversation's
 * items are made anew from the events each time, but an event never changes
 * (the reply being written only grows).
 */
const Row = memo(
  RowView,
  (a, b) =>
    sameStep(a.it, b.it) && a.rowKey === b.rowKey && a.end === b.end && a.live === b.live && a.sessionId === b.sessionId &&
    a.clients === b.clients && a.grant === b.grant && a.previous?.getTime() === b.previous?.getTime(),
);

function sameStep(a: Step, b: Step): boolean {
  if (a.kind !== b.kind || a.seq !== b.seq) return false;
  switch (a.kind) {
    case "turn":
      return a.at?.getTime() === (b as typeof a).at?.getTime();
    case "agent":
      return a.text === (b as typeof a).text;
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

function RowView({ it, rowKey, end, live, sessionId, clients, grant, previous }: RowProps) {
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
      // A turn's reply is one, however many times it writes: when it is over, its last words say when and Copy.
      return (
        <div className={end ? "am end" : "am"}>
          <Markdown text={it.text} streaming={live} />
          {end && <Copy text={it.text} at={it.at} />}
        </div>
      );
    case "tools":
      return it.calls.length === 1 ? (
        <ToolRow id={String(it.calls[0].seq)} call={it.calls[0].call} result={it.calls[0].result} resultSeq={it.calls[0].resultSeq} running={it.calls[0].running} sessionId={sessionId} />
      ) : (
        <ToolGroup id={rowKey} calls={it.calls} sessionId={sessionId} />
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
      if (end.isError && isInterruption(end.failure)) return <InterruptNote failure={end.failure} clients={clients} grant={grant} />;
      // The session's current failure is told once, at the end (rowsOf leaves it out); an earlier one (a later turn went on) in one quiet line.
      if (end.isError) return <FailLine id={rowKey} title={t("chat.turnFailed", { turn: end.turn, title: failureTitle(end.failure?.kind ?? 0) })} message={end.failure?.message ?? ""} />;
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
      // Stopped before its turn began (queued or starting): an interruption, not a failure.
      if (isInterruption(e.failure)) return <InterruptNote failure={e.failure} clients={clients} grant={grant} />;
      return <FailLine id={rowKey} title={e.failure ? t("chat.failedWith", { title: failureTitle(e.failure.kind) }) : t("chat.failed")} message={e.message} />;
    }
  }
}

/** A turn stopped on purpose: by you, by one of your agents, or by Hermesh. */
function InterruptNote({ failure, clients, grant }: { failure: Failure | undefined; clients: Client[]; grant: string }) {
  const { t } = useTranslation("session");
  const by = interrupter(failure);
  if (by === "owner") return <div className="tnote">{t("chat.interruptedByYou", { grant })}</div>;
  const label = clients.find((c) => c.clientId === by)?.label;
  return <div className="tnote">{label ? t("chat.interruptedBy", { name: agentName(label), grant }) : t("chat.interrupted", { grant })}</div>;
}

/** A time line before a turn, for the first one and after a pause. */
function TimeLine({ at, previous }: { at: Date | undefined; previous: Date | undefined }) {
  const now = useNow(60_000);
  const text = at ? turnTimeLine(at, previous, now) : null;
  return text ? <div className="tdiv">{text}</div> : null;
}

/** Under a message on hover: when it was sent, and Copy. */
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
function ToolGroup({ id, calls, sessionId }: { id: string; calls: Calls; sessionId: string }) {
  const { t } = useTranslation("session");
  // Open or not outlives its row not being drawn (useRowState).
  const [open, setOpen] = useRowState(`group:${id}`, false);
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
            <ToolRow key={c.call.callId || String(c.seq)} id={String(c.seq)} call={c.call} result={c.result} resultSeq={c.resultSeq} running={c.running} sessionId={sessionId} />
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
function ToolRow({ id, call, result, resultSeq, running, sessionId }: { id: string; call: ToolCall; result: ToolResult | undefined; resultSeq?: bigint; running: boolean; sessionId: string }) {
  const { t } = useTranslation("session");
  const denied = call.decision === "deny";
  const failed = denied || result?.isError === true;
  const [open, setOpen] = useRowState(`tool:${id}`, false);
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
 * A session that stopped on a failure, where the agent's reply would be: a
 * quiet card telling what happened to the coding agent, what to do when it
 * is the owner's to fix, and that the agent driving the session goes on
 * from here; then Details (the agent's own words, folded) and Archive.
 */
function Failed({ s, failure, grant }: { s: Session; failure: Failure | undefined; grant: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const [open, setOpen] = useState(false);
  const message = failure?.message ?? s.error;
  const kind = failure?.kind ?? FailureKind.UNSPECIFIED;
  const advice = failure && !adviceResumes.has(kind) ? failureAdvice(failure) : "";
  return (
    <div className="qcard" role="alert">
      <Icon name="alert" className="qf-ic" />
      <div className="qf-main">
        <b className="qf-title">{failureHeadline(kind, codingAgentTitle(s.agent))}</b>
        {advice !== "" && <div className="qf-hint">{advice}</div>}
        <div className="qf-hint">{t("chat.resume", { grant: agentName(grant) })}</div>
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
    </div>
  );
}

/** An earlier failure in the conversation: one red line, its words folded. */
function FailLine({ id, title, message }: { id: string; title: string; message: string }) {
  const [open, setOpen] = useRowState(`failure:${id}`, false);
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
        <button className="ctl-btn" title={t("control.interruptTitle", { keys: keys("CmdOrCtrl+.") })} onClick={() => interrupt(st, s)}>
          <Icon name="stop" />
          {t("control.interrupt")}
          <span className="kbd">{keys("CmdOrCtrl+.")}</span>
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

