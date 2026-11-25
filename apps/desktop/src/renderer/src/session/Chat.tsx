import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Client, Machine } from "@hermesh/proto/local";
import { FailureKind, type Failure, type Session, type ToolCall, type ToolResult } from "@hermesh/proto/session";
import {
  agoText, codingAgentTitle, dateOf, failureAdvice, failureHeadline, failureTitle, isArchivable,
  interrupter, isInterruption, resolutionTitle, stepSummary, toolObject, toolSummary, toolVerb,
  turnTimeLine, workspaceDetails, workspaceTitle, type Step, type WorkspaceRow,
} from "@hermesh/core";
import { Mark, agentName } from "@hermesh/ui";
import { GrantAvatar } from "../components";
import { Icon } from "../icons";
import { HEAVY_ROWS, releaseAfterLeaving } from "../memory";
import { archive, machineName, senderName } from "../Sidebar";
import { useNow, useStore } from "../store";
import { Code } from "./Code";
import { Markdown } from "./Markdown";
import { partialOutput, useToolOutput } from "./output";
import { useRowKept, useRowState } from "./useRowState";
import { useVirtualRows, type VirtualRow } from "./useVirtualRows";

/**
 * A conversation's frame, as a session's page and a sub-agent's record (her/Progress.tsx) have it: its rows, newest
 * at the bottom, only those near what is in sight drawn (useVirtualRows, docs/desktop-performance.md §7), between
 * `head` and `foot`; `unread` in their place while there is nothing to show. It opens at its end and stays there as
 * it grows (or `follow` changes) unless the person scrolled up; scrolled well up, a button takes them back down.
 * `control`, while a turn is live, sits over its bottom edge.
 */
export function ChatFrame<R extends VirtualRow>({ rows, initial, label, row, head, foot, unread, control, follow }: {
  rows: R[];
  initial: Record<string, number>;
  label: string;
  row: (r: R) => ReactNode;
  head?: ReactNode;
  foot?: ReactNode;
  unread?: ReactNode;
  control?: (onHeight: (h: number) => void) => ReactNode;
  follow?: unknown;
}) {
  const { t } = useTranslation("session");
  const live = control !== undefined;
  const scroll = useRef<HTMLDivElement>(null);
  // The scroller and the list as elements too, for the window (useVirtualRows): set once, by refs that do not change.
  const [scroller, setScroller] = useState<HTMLDivElement | null>(null);
  const [list, setList] = useState<HTMLDivElement | null>(null);
  const scrollRef = useCallback((el: HTMLDivElement | null) => {
    scroll.current = el;
    setScroller(el);
  }, []);
  const [ctlHeight, setCtlHeight] = useState(0);
  const virtual = useVirtualRows({ scroller, list, rows, initial });

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
  }, [rows, ctlHeight, follow]);
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
  const empty = unread !== undefined;
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
  }, [empty]);
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
    <>
      <div className="cscroll markdown-chat" ref={scrollRef} onScroll={onScroll} onWheel={touch} onTouchMove={touch} onKeyDown={touch} onPointerDown={hold}>
        {empty ? unread : <div className="dbody" style={live ? { paddingBottom: ctlHeight + 40 } : undefined}>
          {head}
          <div className="tr" role="log" aria-label={label} ref={setList} data-rows={rows.length}>
            {/* One flat list of keyed rows and spaces: a row keeps its element (and what is selected in it) however the parts change. */}
            {virtual.parts.flatMap((part) =>
              "space" in part
                ? [<div key={`space:${part.at}`} className="vspace" style={{ height: part.space }} aria-hidden="true" />]
                : rows.slice(part.from, part.to).map((r) => (
                    <div key={r.key} className="vrow" data-step-key={r.key} ref={virtual.measure(r.key)}>
                      {row(r)}
                    </div>
                  )),
            )}
          </div>
          {foot}
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
      {control?.(setCtlHeight)}
    </>
  );
}

/** Under a conversation whose last turn went well: a quiet line that says so. */
export function DoneLine({ text }: { text: string }) {
  return (
    <div className="livebar" style={{ justifyContent: "center", paddingTop: 18 }}>
      <Icon name="check" className="sm" />
      <span>{text}</span>
    </div>
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
export function Unread({ s }: { s: Session }) {
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
export function Stale({ s, behind }: { s: Session; behind: boolean }) {
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
export type ChatRow = VirtualRow & { it: Step; end: boolean; live: boolean; previous: Date | undefined };

/** Each kind's height until rows of it are measured: only how long the scrollbar is depends on it. */
export const rowHeights: Record<string, number> = { turn: 30, user: 64, agent: 220, tools: 32, question: 80, turnEnd: 36, message: 100, error: 36, workspace: 32 };

/**
 * The rows the conversation draws, in order: the items but those that draw nothing (a time line within half an hour of
 * the turn before, a question still pending, a turn that went well, the session's current failure, told at the end).
 */
export function rowsOf(items: Step[], ends: Set<bigint>, current: Failure | undefined): ChatRow[] {
  const out: ChatRow[] = [];
  items.forEach((it, i) => {
    const previous = previousTurn(items, i);
    if (!draws(it, previous, current)) return;
    out.push({ key: String(it.seq), kind: it.kind, it, end: ends.has(it.seq), live: false, previous });
  });
  return out;
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

/** starter: who started the session (a client id, or "owner"), for a prompt whose turn doesn't say who asked. */
type RowProps = { it: Step; rowKey: string; end: boolean; live: boolean; sessionId: string; clients: Client[]; starter: string; previous: Date | undefined };

/**
 * A row renders again only when what it shows changed: the conversation's
 * items are made anew from the events each time, but an event never changes
 * (the reply being written only grows).
 */
export const Row = memo(
  RowView,
  (a, b) =>
    sameStep(a.it, b.it) && a.rowKey === b.rowKey && a.end === b.end && a.live === b.live && a.sessionId === b.sessionId &&
    a.clients === b.clients && a.starter === b.starter && a.previous?.getTime() === b.previous?.getTime(),
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
    case "workspace":
      return a.state === (b as typeof a).state && a.step === (b as typeof a).step && a.failure === (b as typeof a).failure;
    default:
      return true;
  }
}

function RowView({ it, rowKey, end, live, sessionId, clients, starter, previous }: RowProps) {
  const { t } = useTranslation("session");
  switch (it.kind) {
    case "turn":
      return <TimeLine at={it.at} previous={previous} />;
    case "user": {
      // Who asked for the turn speaks on the right: the owner in the blue bubble, with nothing on it; another of the
      // owner's agents (Her, a granted one) in a quiet bubble with its face on the corner. Each turn says its own: a
      // session Her started can hold the owner's words too.
      const by = it.asker || starter;
      const from = by === "owner" ? undefined : { name: senderName({ clients }, by), client: clients.find((c) => c.clientId === by) };
      return <UserBubble text={it.text} at={it.at} from={from} />;
    }
    case "agent":
      // A turn's reply is one, however many times it writes: when it is over, its last words say when and Copy.
      return <AgentText text={it.text} end={end} at={it.at} live={live} />;
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
      if (end.isError && isInterruption(end.failure)) return <InterruptNote failure={end.failure} clients={clients} />;
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
    case "workspace":
      return <WorkspaceLine id={rowKey} row={it} />;
    case "error": {
      const e = it.error;
      // Stopped before its turn began (queued or starting): an interruption, not a failure.
      if (isInterruption(e.failure)) return <InterruptNote failure={e.failure} clients={clients} />;
      return <FailLine id={rowKey} title={e.failure ? t("chat.failedWith", { title: failureTitle(e.failure.kind) }) : t("chat.failed")} message={e.message} />;
    }
  }
}

/** Who sent work in when it isn't the owner: Her, or another of the owner's agents (its grant, while still known). */
export type PromptSender = { name: string; client: Client | undefined };

/**
 * What was sent to the agent, on the right. The owner's is a blue bubble with nothing on it; one another of the
 * owner's agents sent (`from`) is a quiet one with that agent's face on its corner. On hover, who sent it (not the
 * owner), when, and Copy.
 */
export function UserBubble({ text, at, from, waiting = false, note }: { text: string; at?: Date; from?: PromptSender; waiting?: boolean; note?: ReactNode }) {
  const { t } = useTranslation("session");
  return (
    <div className={`um ${from ? "from" : ""} ${waiting ? "waiting" : ""}`}>
      {from && (
        <span className="um-badge" role="img" aria-label={t("chat.sentBy", { name: from.name })} title={t("chat.sentBy", { name: from.name })} data-testid="prompt-sender">
          <GrantAvatar c={from.client ?? { label: from.name }} />
        </span>
      )}
      <div className="bub sel-text">{text}</div>
      {note}
      {!note && <Copy text={text} at={at} from={from?.name} />}
    </div>
  );
}

/**
 * Under the owner's message while it is not in the conversation, in Her's words for hers: on its way (said once that
 * takes 400 ms), waiting for its machine to come back (Discard), or not sent and why (Try Again, Edit).
 */
export function PromptNote({ kind, machine = "", why = "", onRetry, onEdit, onDiscard }: {
  kind: "sending" | "waiting" | "refused";
  machine?: string;
  why?: string;
  onRetry?: () => void;
  onEdit?: () => void;
  onDiscard?: () => void;
}) {
  const { t } = useTranslation("her");
  const shown = useDelayed(kind === "sending");
  if (kind === "sending") return <div className="um-wait" role="status" data-testid="prompt-note">{shown && <span>{t("waiting.sending")}</span>}</div>;
  if (kind === "waiting") {
    return (
      <div className="um-wait" role="status" data-testid="prompt-note">
        <Icon name="clock" className="sm" />
        <span>{t("waiting.until", { name: machine })}</span>
        <button className="link" data-testid="prompt-discard" onClick={onDiscard}>{t("waiting.discard")}</button>
      </div>
    );
  }
  return (
    <div className="um-wait warn" role="status" data-testid="prompt-note">
      <Icon name="alert" className="sm" />
      <span title={why}>{t("waiting.failed", { error: why })}</span>
      <button className="link" data-testid="prompt-retry" onClick={onRetry}>{t("waiting.retry")}</button>
      <button className="link" data-testid="prompt-edit" onClick={onEdit}>{t("waiting.edit")}</button>
    </div>
  );
}

/** True once `active` has held 400 ms: a wait shorter than that shows nothing. */
function useDelayed(active: boolean): boolean {
  const [shown, setShown] = useState(false);
  useEffect(() => {
    if (!active) {
      setShown(false);
      return;
    }
    const timer = setTimeout(() => setShown(true), 400);
    return () => clearTimeout(timer);
  }, [active]);
  return shown;
}

/**
 * Where the session works, set up by its first turn (core WorkspaceRow): a step's line, the branch as its object;
 * opened, how, the branch, its base and the folder, or why it failed. A step under 400 ms shows by its result.
 */
function WorkspaceLine({ id, row }: { id: string; row: WorkspaceRow }) {
  const [open, setOpen] = useRowState(`workspace:${id}`, false);
  const shown = useDelayed(row.state === "running");
  if (row.state === "running" && !shown) return null;
  const { text, code } = workspaceTitle(row);
  const details = row.state === "running" ? "" : workspaceDetails(row).map((d) => `${d.label}  ${d.value}`).join("\n");
  return (
    <div data-testid="thread-workspace" data-state={row.state}>
      <ToolLine verb={text} object={code} title={code} shell={false} running={row.state === "running"} failed={row.state === "failed"}
                output={details} open={open} onToggle={() => setOpen(!open)} />
    </div>
  );
}

/** The agent's words across the page. end: the last of its reply, which says when on hover, and Copy. live: still being written. */
export function AgentText({ text, end, at, live = false }: { text: string; end: boolean; at?: Date; live?: boolean }) {
  return (
    <div className={end ? "am end" : "am"}>
      <Markdown text={text} streaming={live} />
      {end && <Copy text={text} at={at} />}
    </div>
  );
}

/** A turn stopped on purpose: by you, by one of your agents, or by Hermesh. That it can go on, the box under it shows. */
function InterruptNote({ failure, clients }: { failure: Failure | undefined; clients: Client[] }) {
  const { t } = useTranslation("session");
  const by = interrupter(failure);
  if (by === "owner") return <div className="tnote">{t("chat.interruptedByYou")}</div>;
  const label = clients.find((c) => c.clientId === by)?.label;
  return <div className="tnote">{label ? t("chat.interruptedBy", { name: agentName(label) }) : t("chat.interrupted")}</div>;
}

/** A time line before a turn, only for the first one and after a pause. */
export function TimeLine({ at, previous }: { at: Date | undefined; previous: Date | undefined }) {
  const now = useNow(60_000);
  const text = at ? turnTimeLine(at, previous, now) : null;
  return text ? <div className="tdiv">{text}</div> : null;
}

/** Under a message on hover: when it was sent, and Copy. */
/** On hover under a message: who sent it (`from`, when it isn't the owner), when, and Copy. */
function Copy({ text, at, from }: { text: string; at?: Date; from?: string }) {
  const { t } = useTranslation("session");
  const [done, setDone] = useState(false);
  return (
    <div className="msg-act">
      {from && <span className="msg-time">{at ? t("chat.sentByAt", { name: from }) : t("chat.sentBy", { name: from })}</span>}
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

const callKey = (c: Calls[number]) => c.call.callId || String(c.seq);

/** Tool calls in a row as one grey line ("Read 3 files, ran 1 command"), opened to show each; folded until the owner opens it. */
function ToolGroup({ id, calls, sessionId }: { id: string; calls: Calls; sessionId: string }) {
  return (
    <ToolFold
      id={id}
      summary={stepSummary(calls)}
      running={calls.some((c) => c.running)}
      items={calls}
      keyOf={callKey}
      row={(c) => <ToolRow id={String(c.seq)} call={c.call} result={c.result} resultSeq={c.resultSeq} running={c.running} sessionId={sessionId} />}
    />
  );
}

/** Steps in a row as one grey line (`summary`), opened to show each (`children`); folded until opened, as it stays while its row is not drawn (useRowState). */
export function ToolFold<T>({ id, summary, running, items, keyOf, row }: {
  id: string;
  summary: string;
  running: boolean;
  items: readonly T[];
  /** Each item's key: the same function every time (its rows are made again only when the items change). */
  keyOf: (item: T) => string;
  row: (item: T) => ReactNode;
}) {
  const [open, setOpen] = useRowState(`group:${id}`, false);
  // Where its card is scrolled to, kept while its row is not drawn; opened anew, it is at its newest step.
  const kept = useRowKept(`card:${id}`, () => ({ follow: true, top: 0 }));
  const toggle = () => {
    if (!open) kept.follow = true;
    setOpen(!open);
  };
  return (
    <div className={`tgroup ${open ? "open" : ""}`}>
      <button className={`tg-h ${running ? "shimmer" : ""}`} aria-expanded={open} aria-busy={running || undefined} onClick={toggle}>
        <span>{summary}</span>
        <Icon name="caret" className="chev" />
      </button>
      {open && <StepsCard kept={kept} items={items} keyOf={keyOf} row={row} running={running} />}
    </div>
  );
}

/** A step's height in the card until it is measured: a line of 13px text with its padding and hairline (.tg-s is ten and a half of them high). */
const stepHeights: Record<string, number> = { step: 35 };

/**
 * An opened group's card: a row per step. Past about ten rows it stops growing (CSS, .tg-s) and scrolls inside,
 * drawing only the rows near what is in sight (useVirtualRows, as the conversation does), so a turn of hundreds of
 * steps opens at once. It opens at the newest step and, while the turn runs, stays there as steps come, until the
 * person scrolls up or opens a step; then a small arrow takes them back. Where there is more above or below, that
 * edge fades. Where it was scrolled to is kept while its row is not drawn (useRowKept), as what it has open is.
 */
function StepsCard<T>({ kept, items, keyOf, row, running }: {
  /** Whether it stays at the newest step and, when it does not, where it was left (ToolFold keeps it). */
  kept: { follow: boolean; top: number };
  items: readonly T[];
  keyOf: (item: T) => string;
  row: (item: T) => ReactNode;
  running: boolean;
}) {
  const { t } = useTranslation("session");
  // keyOf is the same function every time (ToolFold), so the rows follow the items alone.
  const rows = useMemo(() => items.map((it) => ({ key: keyOf(it), kind: "step" })), [items]);
  const [scroller, setScroller] = useState<HTMLDivElement | null>(null);
  const [list, setList] = useState<HTMLDivElement | null>(null);
  const card = useRef<HTMLDivElement>(null);
  const virtual = useVirtualRows({ scroller, list, rows, initial: stepHeights });
  // When the person last acted in it (a scroll they made, not one rows made). A held pointer (the scroll bar, a
  // selection dragged past an edge) counts until it lets go, as in the conversation.
  const touched = useRef(-Infinity);
  const touch = () => {
    touched.current = performance.now();
  };
  const hold = () => {
    touched.current = Infinity;
    window.addEventListener("pointerup", touch, { once: true });
    window.addEventListener("pointercancel", touch, { once: true });
  };
  // The edges and the arrow, on the element itself: scrolling draws no rows unless the window moves.
  const mark = () => {
    const el = scroller;
    const c = card.current;
    if (!el || !c) return;
    const below = el.scrollHeight - el.scrollTop - el.clientHeight;
    const away = !kept.follow && running && below > 1;
    c.classList.toggle("more-up", el.scrollTop > 1);
    c.classList.toggle("more-down", below > 1);
    c.classList.toggle("away", away);
    // Hidden, the arrow is out of the tab order and of what a screen reader reads, as the conversation's is.
    const back = c.querySelector<HTMLButtonElement>(".tg-end");
    if (back) {
      back.tabIndex = away ? 0 : -1;
      back.setAttribute("aria-hidden", String(!away));
    }
  };
  // Drawn again after its row was not: back where it was left (its rows above are as high as they were guessed).
  const restored = useRef(false);
  useLayoutEffect(() => {
    if (!scroller) return;
    if (kept.follow) scroller.scrollTop = scroller.scrollHeight;
    else if (!restored.current) scroller.scrollTop = kept.top;
    restored.current = true;
    mark();
  });
  const onScroll = () => {
    const el = scroller;
    if (!el) return;
    const below = el.scrollHeight - el.scrollTop - el.clientHeight;
    if (below < 8) kept.follow = true;
    else if (performance.now() - touched.current < 1000) kept.follow = false;
    kept.top = el.scrollTop;
    mark();
  };
  const toEnd = () => {
    const el = scroller;
    if (!el) return;
    kept.follow = true;
    el.scrollTo({ top: el.scrollHeight, behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
    mark();
  };
  // Opening a step stops following (its output would scroll away), and brings the output into view in the card.
  const onClick = (e: MouseEvent) => {
    const head = (e.target as Element).closest(".th[aria-expanded]");
    if (!head) return;
    kept.follow = false;
    const opening = head.getAttribute("aria-expanded") === "false";
    requestAnimationFrame(() => {
      if (opening) head.parentElement?.scrollIntoView({ block: "nearest", behavior: "smooth" });
      mark();
    });
  };
  return (
    <div className="tg-b" ref={card}>
      <div className="tg-s" ref={setScroller} onScroll={onScroll} onWheel={touch} onTouchMove={touch} onKeyDown={touch} onPointerDown={hold} onClick={onClick}>
        <div className="tg-l" ref={setList}>
          {virtual.parts.flatMap((part) =>
            "space" in part
              ? [<div key={`space:${part.at}`} className="vspace" style={{ height: part.space }} aria-hidden="true" />]
              : rows.slice(part.from, part.to).map((r, i) => (
                  <div key={r.key} className="vrow" ref={virtual.measure(r.key)}>
                    {row(items[part.from + i])}
                  </div>
                )),
          )}
        </div>
      </div>
      {running && (
        <button className="tg-end" aria-label={t("chat.toBottom")} title={t("chat.toBottom")} tabIndex={-1} aria-hidden="true" onClick={toEnd}>
          <Icon name="arrowDown" />
        </button>
      )}
    </div>
  );
}

/**
 * One tool call: what it did, and its output when opened, read whole then
 * when the conversation holds only its start. Folded until opened, failed
 * ones too; the one running shimmers.
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
    <ToolLine
      verb={toolVerb(call, running)}
      object={toolSummary(toolObject(call), sessionId)}
      title={call.summary}
      shell={call.kind === "shell"}
      running={running || whole.reading}
      failed={failed}
      denied={denied}
      output={output}
      open={open}
      onToggle={() => setOpen(!open)}
    />
  );
}

/**
 * A step on its line: its verb, what it acted on (a command as code; `title`, all of it), and what it gave when
 * opened (`output`; nothing to open without one). The one at work shimmers; a failed one is red. prose: its output is
 * words, not a program's (a thought).
 */
export function ToolLine({ verb, object, title, shell, running, failed, denied = false, output, open, onToggle, prose = false }: {
  verb: string;
  object: string;
  title: string;
  shell: boolean;
  running: boolean;
  failed: boolean;
  denied?: boolean;
  output: string;
  open: boolean;
  onToggle: () => void;
  prose?: boolean;
}) {
  return (
    <div className={`tool ${denied ? "denied" : failed ? "err" : ""} ${prose ? "thought" : ""} ${open && output ? "open" : ""}`}>
      <button className={`th ${running ? "shimmer" : ""}`} onClick={() => output && onToggle()} aria-expanded={output ? open : undefined} aria-busy={running || undefined}>
        <span className="vb">{verb}</span>
        {shell ? (
          <code className="sm2 cmd" title={title}>
            <Code text={object} lang="bash" />
          </code>
        ) : (
          <span className="sm2" title={title}>{object}</span>
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
export function Failed({ s, failure, grant }: { s: Session; failure: Failure | undefined; grant: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const message = failure?.message ?? s.error;
  const kind = failure?.kind ?? FailureKind.UNSPECIFIED;
  const advice = failure && !adviceResumes.has(kind) ? failureAdvice(failure) : "";
  return (
    <FailCard
      headline={failureHeadline(kind, codingAgentTitle(s.agent))}
      hints={[advice, t("chat.resume", { grant: agentName(grant) })]}
      message={message}
      actions={isArchivable(s) && <button className="link" onClick={() => archive(st, s)}>{t("chat.archive")}</button>}
    />
  );
}

/** Where a conversation stopped on a failure: a quiet card with what happened, what to do (`hints`), then Details (`message`, folded) and `actions`. */
export function FailCard({ headline, hints, message, actions }: { headline: string; hints: string[]; message: string; actions?: ReactNode }) {
  const { t } = useTranslation("session");
  const [open, setOpen] = useState(false);
  return (
    <div className="qcard" role="alert">
      <Icon name="alert" className="qf-ic" />
      <div className="qf-main">
        <b className="qf-title">{headline}</b>
        {hints.filter((h) => h !== "").map((h) => <div key={h} className="qf-hint">{h}</div>)}
        <div className="qf-act">
          {message !== "" && (
            <button className="link" aria-expanded={open} onClick={() => setOpen(!open)}>
              {t("chat.details")} <Icon name="chevR" className={`sm chev ${open ? "open" : ""}`} />
            </button>
          )}
          {actions}
        </div>
        {open && <div className="qf-raw sel-text">{message}</div>}
      </div>
    </div>
  );
}

/** An earlier failure in the conversation: one red line, its words folded. */
export function FailLine({ id, title, message }: { id: string; title: string; message: string }) {
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

/** The bottom of a live turn, over the conversation: what is asked (`panel`) above, the row of what runs now and how to stop it below. */
export function ControlBar({ panel, onHeight, children }: { panel?: ReactNode; onHeight: (h: number) => void; children: ReactNode }) {
  const { t } = useTranslation("session");
  const ref = useRef<HTMLDivElement>(null);
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
    <div className={`cbar ${panel ? "q" : ""}`} role="region" aria-label={t("control.label")} ref={ref}>
      {panel}
      <div className="cbar-row">{children}</div>
    </div>
  );
}

