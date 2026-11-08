import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { botChat, busyBubble, pendingApproval, type BotItem, type WorkingAgent } from "@hermesh/her-client";
import type { BotSummary } from "@hermesh/her-protocol";
import type { HerWaiting } from "@hermesh/proto/local";
import { agoText } from "@hermesh/core";
import { local } from "../api";
import { Icon } from "../icons";
import { HEAVY_ROWS, releaseAfterLeaving } from "../memory";
import { Markdown } from "../session/Markdown";
import { ContentScope } from "../session/scope";
import { useVirtualRows, type VirtualRow } from "../session/useVirtualRows";
import { errorText, useNow, useStore } from "../store";
import { Approval, type SendOp } from "./Approval";
import { addAttachments, toInput, type Attachment } from "./attachments";
import { BotAvatar } from "./Avatar";
import { Composer, Tile } from "./Composer";
import type { Conversation as Conv } from "./connection";
import { useHer } from "./context";
import { envState, type EnvState } from "./environments";
import { FailureCard, PastFailure } from "./Failure";
import { messageTime, momentText, thingsText, timeLine } from "./format";
import { Question, SentFiles, type FileScope } from "./Messages";
import { splitAttached, waitingFor, type WaitingItem } from "./waiting";

/** A pause before a time line (bot-mode-design §6.6). */
const PAUSE = 10 * 60_000;

/** The text that goes in Her's composer from beside it (the routines panel), by environment. */
const composerText = new Map<string, (text: string) => void>();

/** How the conversation on screen brings a routine run's message into view (it may have to draw it first), by environment. */
const runShowers = new Map<string, (run: string) => void>();

/** Puts text in the composer of an environment's conversation, ready to type on. */
export const putInComposer = (machineId: string, text: string) => composerText.get(machineId)?.(text);

/** Brings a routine run's message into view in an environment's conversation, and flashes it. */
export const showRun = (machineId: string, run: string) => runShowers.get(machineId)?.(run);

/**
 * The conversation with Her, under the page's top
 * bar: bubbles on both sides, and the composer. Her says only what it sends. Offline, what was read
 * stays readable, and what is written meanwhile waits on this Mac.
 */
export function Conversation({ bot, conv, op, waiting, onWaiting }: { bot: BotSummary; conv: Conv; op: SendOp; waiting: HerWaiting[]; onWaiting: () => void }) {
  const her = useHer();
  useMarkRead(bot);
  const state = envState(her.machine);
  return (
    <ContentScope value={`${her.machine.machineId}/${conv.session ?? ""}`}>
      <div className="dwrap">
        <div className="dmain">
          <OfflineLine state={state} />
          <BotChat bot={bot} conv={conv} op={op} state={state} waiting={waiting} onWaiting={onWaiting} />
        </div>
      </div>
    </ContentScope>
  );
}

/** An environment that can't be reached: what was read stays, with when the machine was last seen. */
function OfflineLine({ state }: { state: EnvState }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const now = useNow(30_000);
  if (state.kind !== "offline") return null;
  const name = her.machine.machineName;
  return (
    <div className="her-note" role="status">
      <Icon name="wifi" className="sm" />
      <span>{state.seen ? t("env.offlineSeen", { name, ago: agoText(state.seen, now) }) : t("env.offline", { name })}</span>
    </div>
  );
}

/**
 * Nothing is unread while the owner looks: the assistant on screen and the window in front. What comes
 * meanwhile is unread until they are back (bots/read).
 */
function useMarkRead(bot: BotSummary) {
  const her = useHer();
  const unread = bot.unread ?? 0;
  const connected = her.env.rpc !== null;
  useEffect(() => {
    const read = () => {
      if ((bot.unread ?? 0) === 0 || !document.hasFocus() || !connected) return;
      her.call("bots/read", { bot_id: bot.id }).catch(() => {});
    };
    read();
    window.addEventListener("focus", read);
    return () => window.removeEventListener("focus", read);
  }, [bot.id, unread, connected]);
}

type Item = BotItem | WaitingItem;

/** A row of the conversation: its item, the time of the one before, and whether it sits close to its neighbours. */
type BotRow = VirtualRow & { item: Item; previous: number | undefined; side: "um" | "bm" | null; joinPrev: boolean; joinNext: boolean };

/** Each kind's height until rows of it are measured: only how long the scrollbar is depends on it. */
const rowHeights: Record<string, number> = { user: 56, waiting: 70, bot: 96, attachment: 160, question: 120, routine: 40, deck_edit: 0, failure: 60 };

/** Which bubble an item draws: the owner's, the assistant's, or neither (a line of its own). */
const sideOf = (item: Item): BotRow["side"] => (item.kind === "user" || item.kind === "waiting" ? "um" : item.kind === "bot" || item.kind === "attachment" || item.kind === "question" ? "bm" : null);

/** The rows: two bubbles in a row from the same side, with no time line between (one shows after a pause), sit close. */
function botRows(items: Item[]): BotRow[] {
  const close = (i: number) => i > 0 && sideOf(items[i]) !== null && sideOf(items[i]) === sideOf(items[i - 1]) && items[i].ts - items[i - 1].ts < PAUSE;
  return items.map((item, i) => ({ key: item.key, kind: item.kind, item, previous: items[i - 1]?.ts, side: sideOf(item), joinPrev: close(i), joinNext: i + 1 < items.length && close(i + 1) }));
}

function BotChat({ bot, conv, op, state, waiting, onWaiting }: {
  bot: BotSummary;
  conv: Conv;
  op: SendOp;
  state: EnvState;
  waiting: HerWaiting[];
  onWaiting: () => void;
}) {
  const { t } = useTranslation("her");
  const her = useHer();
  const { view, session } = conv;
  const chat = useMemo(() => (view ? botChat(view).filter((i) => i.kind !== "deck_edit") : []), [view]);
  const kept = useMemo(
    () => waitingFor(waiting, bot.id, chat.flatMap((i) => (i.kind === "user" ? [{ text: i.text, ts: i.ts }] : []))),
    [waiting, bot.id, chat],
  );
  const items = useMemo<Item[]>(() => [...chat, ...kept], [chat, kept]);
  const busy = view ? busyBubble(view) : null;
  const [dock, setDock] = useState(0);
  const scroll = useRef<HTMLDivElement>(null);
  const [scroller, setScroller] = useState<HTMLDivElement | null>(null);
  const [list, setList] = useState<HTMLDivElement | null>(null);
  const scrollRef = useCallback((el: HTMLDivElement | null) => {
    scroll.current = el;
    setScroller(el);
  }, []);
  // Messages that came after the page opened slide in, once.
  const arrived = useRef<{ last: string | undefined; at: Map<string, number> } | null>(null);
  arrived.current ??= { last: items.at(-1)?.key, at: new Map() };
  {
    const known = arrived.current;
    const from = known.last === undefined ? 0 : items.findIndex((i) => i.key === known.last) + 1;
    if (from > 0 || known.last === undefined) for (const item of items.slice(from)) if (!known.at.has(item.key)) known.at.set(item.key, performance.now());
    known.last = items.at(-1)?.key ?? known.last;
  }
  const fresh = (key: string) => performance.now() - (arrived.current!.at.get(key) ?? -Infinity) < 1000;

  // Earlier history, a page at a time, as the owner nears the top.
  const [paging, setPaging] = useState(false);
  const asking = useRef(false);
  const earlier = () => {
    if (asking.current || !view?.before) return;
    asking.current = true;
    setPaging(true);
    conv.loadEarlier().finally(() => {
      asking.current = false;
      setPaging(false);
    });
  };
  // Follow the conversation: open at its end, stay there as it grows unless the owner scrolled up.
  const atEnd = useRef(true);
  const [away, setAway] = useState(false);
  const firstKey = items[0]?.key;
  const fromEnd = useRef<number | null>(null);
  const lastFirst = useRef(firstKey);
  if (firstKey !== lastFirst.current && scroll.current) fromEnd.current = scroll.current.scrollHeight - scroll.current.scrollTop;
  useLayoutEffect(() => {
    const el = scroll.current;
    lastFirst.current = firstKey;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
    else if (el && fromEnd.current !== null) el.scrollTop = el.scrollHeight - fromEnd.current;
    fromEnd.current = null;
    if (el && el.scrollTop < el.clientHeight * 1.5) earlier();
  }, [items, busy?.typing, busy?.agents.length, dock]);
  const follow = () => {
    const el = scroll.current;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
  };
  const lastTop = useRef(0);
  const onScroll = () => {
    const el = scroll.current;
    if (!el) return;
    const below = el.scrollHeight - el.scrollTop - el.clientHeight;
    if (below < 80) atEnd.current = true;
    else if (el.scrollTop < lastTop.current) atEnd.current = false;
    lastTop.current = el.scrollTop;
    setAway(below > 240);
    if (el.scrollTop < el.clientHeight * 1.5) earlier();
  };
  // What grows on its own, a picture read after its message showed, stays in view at the end.
  const body = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!body.current) return;
    const ro = new ResizeObserver(() => {
      if (atEnd.current && scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight;
    });
    ro.observe(body.current);
    return () => ro.disconnect();
  }, []);
  const toEnd = () => {
    const el = scroll.current;
    if (!el) return;
    atEnd.current = true;
    el.scrollTo({ top: el.scrollHeight, behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  };

  const empty = items.length === 0 && !busy;
  const working = busy?.agents.length ?? 0;
  const rows = useMemo(() => botRows(items), [items]);
  const virtual = useVirtualRows({ scroller, list, rows, initial: rowHeights });
  const held = useRef(0);
  held.current = rows.length;
  useEffect(() => () => void (held.current >= HEAVY_ROWS && releaseAfterLeaving()), []);
  const key = her.machine.machineId;
  const { scrollToKey } = virtual;
  useEffect(() => {
    const show = (run: string) => {
      const item = items.find((i) => "source" in i && i.source?.run_id === run);
      if (!item) return;
      atEnd.current = false;
      scrollToKey(item.key, {
        then: (el) => {
          const bubble = el.querySelector<HTMLElement>("[data-run]") ?? el;
          bubble.classList.add("rtn-flash");
          setTimeout(() => bubble.classList.remove("rtn-flash"), 1500);
        },
      });
    };
    runShowers.set(key, show);
    return () => {
      if (runShowers.get(key) === show) runShowers.delete(key);
    };
  }, [key, items, scrollToKey]);
  const files: FileScope = { rpc: conv.rpc, session: session ?? "", bot: bot.id };
  const drawItem = (item: Item, previous: number | undefined) => {
    const line = <TimeSince at={item.ts} previous={previous} />;
    switch (item.kind) {
      case "user":
        return <UserBubble key={item.key} item={item} fresh={fresh(item.key)} line={line} />;
      case "waiting":
        return <WaitingBubble key={item.key} item={item} line={line} onDiscarded={onWaiting} />;
      case "routine":
        return <RoutineChange key={item.key} item={item} line={line} />;
      case "bot":
        return (
          <BotMessage key={item.key} item={item} fresh={fresh(item.key)} line={line} copy={item.text}>
            <div className="bbub">
              <Markdown text={item.text} />
            </div>
          </BotMessage>
        );
      case "attachment":
        return (
          <BotMessage key={item.key} item={item} fresh={fresh(item.key)} line={line} copy={item.text}>
            <SentFiles scope={files} files={item.files} text={item.text} />
          </BotMessage>
        );
      case "question":
        return (
          <BotMessage key={item.key} item={item} fresh={fresh(item.key)} line={line} copy={item.text}>
            <Question
              text={item.text}
              options={item.options}
              open={item.open && conv.rpc !== null}
              // A tapped answer is the owner's message: while the assistant works, it waits for the step under way.
              onAnswer={(label) => op({ type: "prompt", input: [{ type: "text", text: label }], mode: view?.live ? "steer" : undefined })}
            />
          </BotMessage>
        );
      case "failure":
        return item.last ? <FailureCard key={item.key} error={item.error} working={working} model={view?.info.model} op={op} stateId={`failure:${item.key}`} /> : <PastFailure key={item.key} error={item.error} stateId={`failure:${item.key}`} />;
      default:
        return null;
    }
  };
  return (
    <>
      <div className="cscroll" ref={scrollRef} onScroll={onScroll}>
        <div className="dbody" ref={body} style={{ paddingBottom: dock + 40 }}>
          {view?.before && (
            <div className="tdiv earlier">
              {paging ? <span className="mini-spin" aria-label={t("chat.loadingEarlier")} /> : <button className="link" onClick={earlier}>{t("chat.earlier")}</button>}
            </div>
          )}
          {empty ? (
            <EmptyPage bot={bot} state={state} loading={!view && state.kind === "ready" && !conv.noModel} />
          ) : (
            <div className="tr bots" role="log" aria-label={t("chat.log")} ref={setList} data-rows={rows.length}>
              {virtual.parts.flatMap((part) =>
                "space" in part
                  ? [<div key={`space:${part.at}`} className="vspace" style={{ height: part.space }} aria-hidden="true" />]
                  : rows.slice(part.from, part.to).map((r) => (
                      <div key={r.key} className={`vrow ${r.side ? `side-${r.side}` : ""} ${r.joinPrev ? "join-prev" : ""} ${r.joinNext ? "join-next" : ""}`} data-step-key={r.key} ref={virtual.measure(r.key)}>
                        {drawItem(r.item, r.previous)}
                      </div>
                    )),
              )}
              {busy && <BusyBubble busy={busy} retry={view?.live?.retry} onOpen={follow} />}
            </div>
          )}
        </div>
      </div>
      <button className={`tobottom ${away ? "on" : ""}`} style={{ "--dock": `${dock}px` } as CSSProperties} aria-label={t("chat.toBottom")} title={t("chat.toBottom")} tabIndex={away ? 0 : -1} aria-hidden={!away} onClick={toEnd}>
        <Icon name="arrowDown" />
      </button>
      <Dock bot={bot} conv={conv} op={op} state={state} waiting={kept.length > 0} onWaiting={onWaiting} onHeight={setDock} />
    </>
  );
}

/** A new assistant's page, or one whose conversation can't be read now: its avatar, its name, and a line. Her greets. */
function EmptyPage({ bot, state, loading }: { bot: BotSummary; state: EnvState; loading: boolean }) {
  const { t } = useTranslation("her");
  const her = useHer();
  if (loading) return <div className="bempty"><span className="mini-spin" /></div>;
  const name = her.machine.machineName;
  const line = state.kind === "offline" ? t("chat.unread", { name }) : t("chat.greeting", { name: bot.name, environment: name });
  return (
    <div className="bempty">
      <BotAvatar bot={bot} size={64} />
      <h3>{bot.name}</h3>
      <p>{line}</p>
    </div>
  );
}

/** A time line before a message after a pause of ten minutes, and before the first. */
function TimeSince({ at, previous }: { at: number; previous: number | undefined }) {
  const now = useNow(60_000);
  const text = timeLine(new Date(at), previous === undefined ? undefined : new Date(previous), now, PAUSE);
  return text ? <div className="tdiv">{text}</div> : null;
}

function MessageTime({ at }: { at: number }) {
  const now = useNow(60_000);
  return <span className="msg-time">{messageTime(new Date(at), now)}</span>;
}

/** A path's last part, a Mac's or a PC's. */
const baseName = (path: string) => path.split(/[\\/]/).filter(Boolean).pop() ?? path;

function UserBubble({ item, fresh, line }: { item: Extract<BotItem, { kind: "user" }>; fresh: boolean; line: ReactNode }) {
  const { t } = useTranslation("her");
  // A message hermeshd delivered names its files after its words.
  const said = item.files.length === 0 ? splitAttached(item.text) : { text: item.text, files: item.files };
  return (
    <>
      {line}
      <div className={`um ${fresh ? "add-anim" : ""} ${item.queued ? "queued" : ""}`} title={item.queued ? t("chat.queued") : undefined}>
        {(item.images.length > 0 || said.files.length > 0) && (
          <div className="um-att">
            {item.images.map((im, i) => (
              <Tile key={i} name={t("chat.image")} src={`data:${im.mime_type};base64,${im.data}`} />
            ))}
            {said.files.map((f) => (
              <Tile key={f} name={baseName(f)} />
            ))}
          </div>
        )}
        {said.text !== "" && <div className="bub sel-text">{said.text}</div>}
        <div className="msg-act">
          <span className="msg-meta">
            <MessageTime at={item.ts} />
          </span>
          <CopyButton text={said.text} />
        </div>
      </div>
    </>
  );
}

/**
 * A message written while the assistant's machine was offline, kept on this Mac until it is back
 * (docs/her-server.md §5.5): it says so, and why the last try failed when one did. One her keeps
 * refusing holds back those after it, so it can be discarded.
 */
function WaitingBubble({ item, line, onDiscarded }: { item: WaitingItem; line: ReactNode; onDiscarded: () => void }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const name = her.machine.machineName;
  const refused = item.error !== "" && item.error !== "offline";
  const discard = () =>
    local.deleteHerWaiting({ machineId: her.machine.machineId, id: item.id }).then(
      () => onDiscarded(),
      (e) => st.show(errorText(e)),
    );
  return (
    <>
      {line}
      <div className="um waiting">
        {item.files.length > 0 && (
          <div className="um-att">
            {item.files.map((f) => (
              <Tile key={f} name={f} />
            ))}
          </div>
        )}
        {item.text !== "" && <div className="bub sel-text">{item.text}</div>}
        <div className={`um-wait ${refused ? "warn" : ""}`} role="status">
          <Icon name={refused ? "alert" : "clock"} className="sm" />
          <span title={refused ? item.error : undefined}>{refused ? t("waiting.failed", { error: item.error }) : t("waiting.until", { name })}</span>
          <button className="link" onClick={() => void discard()}>
            {t("waiting.discard")}
          </button>
        </div>
      </div>
    </>
  );
}

/** What the assistant did to a routine, as a line in the chat that opens it (bot-mode-design §6.21). */
function RoutineChange({ item, line }: { item: Extract<BotItem, { kind: "routine" }>; line: ReactNode }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const now = useNow(60_000);
  const time = item.next_run ? momentText(new Date(item.next_run), now) : item.when;
  const gone = item.action === "delete";
  return (
    <>
      {line}
      <button className="rtn-line" onClick={() => her.setPanel({ kind: "routines", route: gone ? {} : { routine: item.routine_id } })}>
        <Icon name="clock" />
        <span>{t(`routines.line.${item.action}`, { name: item.name, time })}</span>
      </button>
    </>
  );
}

function CopyButton({ text }: { text: string }) {
  const { t } = useTranslation("her");
  const [done, setDone] = useState(false);
  return (
    <button className="tb" aria-label={t("chat.copy")} title={t("chat.copy")} onClick={() => navigator.clipboard.writeText(text).then(() => (setDone(true), setTimeout(() => setDone(false), 1500)))}>
      <Icon name={done ? "check" : "copy"} className="sm" />
    </button>
  );
}

type SentItem = Extract<BotItem, { kind: "bot" | "attachment" | "question" }>;

/** A message from the assistant: what it sent, with its time and buttons beside it on hover. */
function BotMessage({ item, fresh, line, copy, children }: { item: SentItem; fresh: boolean; line: ReactNode; copy?: string; children: ReactNode }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const source = item.source;
  return (
    <>
      {line}
      <div className={`bm ${fresh ? "add-anim" : ""}`} data-run={source?.run_id}>
        <div className="bm-b">
          <div className="bline">
            {children}
            <div className="msg-act">
              <span className="msg-meta">
                <MessageTime at={item.ts} />
              </span>
              {copy !== undefined && <CopyButton text={copy} />}
              {source && (
                <button
                  className="tb"
                  aria-label={t("routines.fromRoutine", { name: source.name })}
                  title={t("routines.fromRoutine", { name: source.name })}
                  onClick={() => her.setPanel({ kind: "routines", route: { routine: source.routine_id, run: source.run_id } })}
                >
                  <Icon name="clock" className="sm" />
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
    </>
  );
}

/**
 * The last bubble while the assistant is busy (bot-mode-design §6.7): three dots; with sub-agents at
 * work, they quieten and what the assistant called the work opens out after them, shimmering. Several
 * show as a count that opens to their names.
 */
function BusyBubble({ busy, retry, onOpen }: { busy: { typing: boolean; agents: WorkingAgent[] }; retry?: { at: number }; onOpen: () => void }) {
  const { t } = useTranslation("her");
  const now = useNow(1000).getTime();
  const [open, setOpen] = useState(false);
  const agents = busy.agents;
  const unstable = retry && now - retry.at > 10_000;
  const label = agents.length === 1 ? agents[0].label : unstable ? t("chat.retrying") : undefined;
  const several = agents.length > 1;
  const dots = (
    <span className={`dots ${several || label ? "quiet" : ""}`} aria-hidden="true">
      <i />
      <i />
      <i />
    </span>
  );
  const shown = open && several;
  useLayoutEffect(() => {
    if (shown) onOpen();
  }, [shown]);
  return (
    <div className="bm working">
      <div className="bm-b">
        <div className="bline">
          <div className={`bbub wbub ${shown ? "open" : ""}`}>
            {several ? (
              <button className="wb-h" aria-expanded={shown} title={shown ? t("chat.busyClose") : t("chat.busyOpen")} onClick={() => setOpen(!open)}>
                {dots}
                <span className="wb-w">
                  <span className="wb-t shimmer">{t("line.agents", { things: thingsText(agents.length) })}</span>
                </span>
                <Icon name="chevD" className="chev" />
              </button>
            ) : (
              <div className="wb-h" title={label ?? t("line.typing")}>
                {dots}
                {label && (
                  <span className="wb-w">
                    <span className="wb-t shimmer">{label}</span>
                  </span>
                )}
              </div>
            )}
            {shown && (
              <div className="wb-l">
                {agents.map((a) => (
                  <div key={a.id} className="wb-r shimmer">
                    {a.label}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

/** What the owner had written to each assistant and not sent yet, by environment and assistant. */
const drafts = new Map<string, { text: string; attachments: Attachment[] }>();
/** What failed tasks handed back that is in a composer already. */
const handed = new WeakSet<object>();

/**
 * The composer of an assistant's page: only Send (bot-mode-design §6.14); every message goes in as it
 * is sent. While the conversation is open, a message goes on it (session/op), its pictures in it and
 * its files uploaded first; otherwise, or behind messages that wait, it goes through hermeshd, which
 * delivers it now or keeps it here until the machine is back (HerSend).
 */
function Dock({ bot, conv, op, state, waiting, onWaiting, onHeight }: {
  bot: BotSummary;
  conv: Conv;
  op: SendOp;
  state: EnvState;
  waiting: boolean;
  onWaiting: () => void;
  onHeight: (h: number) => void;
}) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const view = conv.view;
  const key = her.machine.machineId;
  const asking = view && conv.rpc ? pendingApproval(view) : null;
  const [draft, setDraft] = useState(() => drafts.get(key) ?? { text: "", attachments: [] });
  const change = (d: { text: string; attachments: Attachment[] }) => {
    setDraft(d);
    drafts.set(key, d);
  };
  // What a failed task handed back goes back to the composer.
  useEffect(() => {
    const returned = view?.returned;
    if (!returned?.length || handed.has(returned)) return;
    handed.add(returned);
    const text = returned.map((m) => m.content.flatMap((c) => (c.type === "text" ? [c.text] : [])).join("\n")).filter(Boolean);
    const d = drafts.get(key) ?? { text: "", attachments: [] };
    change({ ...d, text: [...text, d.text.trim()].filter(Boolean).join("\n\n") });
  }, [view?.returned]);
  // What the routines panel writes for the owner to finish: ready to type on.
  useEffect(() => {
    const put = (text: string) => {
      const d = drafts.get(key) ?? { text: "", attachments: [] };
      change({ ...d, text: d.text.trim() ? `${d.text.trim()}\n${text}` : text });
      requestAnimationFrame(() => document.querySelector<HTMLElement>(".her .cmp-in")?.focus());
    };
    composerText.set(key, put);
    return () => {
      if (composerText.get(key) === put) composerText.delete(key);
    };
  }, [key]);
  const name = her.machine.machineName;
  const noModel = state.kind === "noModel" || conv.noModel;
  const send = async () => {
    const sent = draft;
    change({ text: "", attachments: [] });
    let ok = false;
    try {
      if (conv.rpc && conv.session && state.kind === "ready" && !waiting) {
        const uploaded: string[] = [];
        for (const a of sent.attachments) if (a.kind === "file") uploaded.push((await local.herUpload({ machineId: her.machine.machineId, botId: bot.id, localPath: a.path })).path);
        // While it answers, a message waits for the step under way and is answered next.
        ok = await op({ type: "prompt", input: toInput(sent.text, sent.attachments, uploaded), mode: view?.live ? "steer" : undefined });
      } else if (sent.attachments.some((a) => a.kind === "image" && !a.local)) {
        st.show(t("waiting.noPictures", { name }));
      } else {
        const paths = sent.attachments.map((a) => (a.kind === "file" ? a.path : a.local!));
        await local.herSend({ machineId: her.machine.machineId, botId: bot.id, text: sent.text.trim(), localPaths: paths });
        onWaiting();
        ok = true;
      }
    } catch (e) {
      st.show(errorText(e));
    }
    if (!ok) {
      const d = drafts.get(key) ?? { text: "", attachments: [] };
      change({ text: [sent.text, d.text].filter(Boolean).join("\n"), attachments: addAttachments(sent.attachments, d.attachments) });
    }
  };
  const above = asking ? (
    <Approval key={`${asking.agent?.id ?? ""}/${asking.approval.call_id}`} view={view!} op={op} />
  ) : noModel ? (
    <StateNote icon="key" title={t("env.noModel.title", { name })} body={t("env.noModel.body", { name })} action={t("model.openConsole")} onAction={her.openConsole} />
  ) : state.kind === "starting" ? (
    <StateNote icon="clock" title={t("env.starting", { name })} />
  ) : state.kind === "failed" ? (
    <StateNote icon="alert" title={t("env.failed", { name })} body={state.error} />
  ) : null;
  return (
    <Composer
      above={above}
      onHeight={onHeight}
      value={draft.text}
      onChange={(text) => change({ ...draft, text })}
      attachments={draft.attachments}
      onAttachments={(attachments) => change({ ...draft, attachments })}
      onSend={() => void send()}
      canSend={!noModel}
      placeholder={t("chat.placeholder", { name: bot.name })}
    />
  );
}

/** What keeps the environment from answering, above the composer in an approval's shape, with the way out. */
function StateNote({ icon, title, body, action, onAction }: { icon: string; title: string; body?: string; action?: string; onAction?: () => void }) {
  return (
    <div className="knote" role="status">
      <div className="knote-h">
        <Icon name={icon} className="knote-i" />
        <b>{title}</b>
      </div>
      {body && <p className="knote-b sel-text">{body}</p>}
      {action && (
        <div className="appr-f">
          <span className="appr-sp" />
          <button className="btn primary" onClick={onAction}>
            {action}
          </button>
        </div>
      )}
    </div>
  );
}
