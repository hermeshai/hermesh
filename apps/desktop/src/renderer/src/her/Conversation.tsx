import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { botChat, busyBubble, pendingApproval, replyOf, runningAgents, type BotItem, type ReplyQuote } from "@hermesh/her-client";
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
import { BusyBubble } from "./Busy";
import { addAttachments, type Attachment } from "./attachments";
import { cardOf } from "./cards";
import { ChromeWait, useWaitsForChrome } from "./ChromeWait";
import { Composer, Tile } from "./Composer";
import type { Conversation as Conv } from "./connection";
import { useHer } from "./context";
import { envState, type EnvState } from "./environments";
import { FailureCard, PastFailure } from "./Failure";
import { messageTime, momentText, timeLine } from "./format";
import { Question, SentFiles, useSaveFile, type FileScope } from "./Messages";
import { FilePreview, previews } from "./preview/FilePreview";
import { QuoteCard, quoteOf, quoteText, ReplyButton, ReplyPill, Replying } from "./reply";
import { Onboarding, useOnboarding } from "./ModelOnboarding";
import { onboardingAfter, withOnboarding } from "./onboarding";
import { SecureCard } from "./SecureCard";
import { SessionCard, WorkCard } from "./TeamCards";
import { outboxFor, splitAttached, stillSending, type Sending, type SendingItem, type WaitingItem } from "./waiting";

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
 * How the conversation on screen brings a message into view by its key, loading earlier history until it is there,
 * by environment (docs/her-deliverables.md §3.1). `session`: the conversation the message was sent in.
 */
const messageShowers = new Map<string, (key: string, session: string) => void>();

/** Brings a message of an environment's conversation into view, and flashes it: Show in Conversation. */
export const showMessage = (machineId: string, key: string, session: string) => messageShowers.get(machineId)?.(key, session);

/** Puts a reply in the composer of an environment's conversation (docs/her-reply.md), from beside it. */
export const replyInComposer = (machineId: string, reply: ReplyQuote) => composerReply.get(machineId)?.(reply);

/** A message not sent, back into the composer of an environment's conversation (Edit). */
const composerReturn = new Map<string, (sent: Draft) => void>();

/** What the next message replies to, into the composer of an environment's conversation (docs/her-reply.md). */
const composerReply = new Map<string, (reply: ReplyQuote) => void>();

type Draft = { text: string; attachments: Attachment[]; reply?: ReplyQuote };

/**
 * What this Mac sent and HerSend has not answered for, or refused, by environment (docs/her-sending.md §5.2): with the
 * draft each was, to send again or edit. Kept here, not in a page, so that a message being sent still shows after
 * going to another page and back.
 */
/** delivered: HerSend answered that her took it; due: a while ago, so the conversation would show it by now. */
type Outgoing = Sending & { botId: string; draft: Draft; delivered?: boolean; due?: boolean };
const outbox = (() => {
  const by = new Map<string, Outgoing[]>();
  const listeners = new Set<() => void>();
  const set = (machineId: string, list: Outgoing[]) => {
    by.set(machineId, list);
    for (const l of listeners) l();
  };
  return {
    list: (machineId: string) => by.get(machineId) ?? none,
    get: (machineId: string, id: string) => by.get(machineId)?.find((m) => m.id === id),
    add: (machineId: string, m: Outgoing) => set(machineId, [...(by.get(machineId) ?? []), m]),
    refuse: (machineId: string, id: string, refused: string | null) => set(machineId, (by.get(machineId) ?? []).map((m) => (m.id === id ? { ...m, refused } : m))),
    /** Taken by her: the conversation shows it in a moment, unless it never will (a command such as /context). */
    taken: (machineId: string, id: string) => {
      const mark = (patch: Partial<Outgoing>) => set(machineId, (by.get(machineId) ?? []).map((m) => (m.id === id ? { ...m, ...patch } : m)));
      mark({ delivered: true });
      setTimeout(() => by.get(machineId)?.some((m) => m.id === id) && mark({ due: true }), 5_000);
    },
    drop: (machineId: string, ids: string[]) => {
      const list = by.get(machineId) ?? [];
      if (list.some((m) => ids.includes(m.id))) set(machineId, list.filter((m) => !ids.includes(m.id)));
    },
    subscribe: (l: () => void) => {
      listeners.add(l);
      return () => void listeners.delete(l);
    },
  };
})();
const none: never[] = [];

const useOutbox = (machineId: string) => useSyncExternalStore(outbox.subscribe, () => outbox.list(machineId));

/** A message id: the outbox's and her's for it. */
const messageId = () => `hw_${crypto.randomUUID().replaceAll("-", "")}`;

/**
 * The conversation with Her, under the page's top
 * bar: bubbles on both sides, and the composer. Her says only what it sends. Offline, what was read
 * stays readable, and what is written meanwhile waits on this Mac.
 */
export function Conversation({ bot, conv, op, waiting, onWaiting }: { bot: BotSummary; conv: Conv; op: SendOp; waiting: HerWaiting[]; onWaiting: () => Promise<HerWaiting[]> }) {
  const her = useHer();
  useMarkRead(bot);
  const state = envState(her.machine);
  return (
    <ContentScope value={`${her.machine.machineId}/${conv.session ?? ""}`}>
      <div className="dwrap">
        <div className="dmain">
          <OfflineLine state={state} />
          <ConnectingLine state={state} reconnecting={conv.reconnecting} />
          <BotChat bot={bot} conv={conv} op={op} state={state} waiting={waiting} onWaiting={onWaiting} />
        </div>
      </div>
    </ContentScope>
  );
}

/** An environment that can't be reached: what was read stays, with when the machine was last seen. */
export function OfflineLine({ state }: { state: EnvState }) {
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

/** The conversation's connection opening again for more than a moment, while the machine answers: a reply may take a
 * while, and the page says why (docs/her-sending.md §5.4). */
function ConnectingLine({ state, reconnecting }: { state: EnvState; reconnecting: boolean }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    setSlow(false);
    if (!reconnecting) return;
    const timer = setTimeout(() => setSlow(true), 2000);
    return () => clearTimeout(timer);
  }, [reconnecting]);
  if (state.kind !== "ready" || !reconnecting || !slow) return null;
  return (
    <div className="her-note" role="status" data-testid="her-connecting">
      <span className="mini-spin" aria-hidden="true" />
      <span>{t("waiting.connecting", { name: her.machine.machineName })}</span>
    </div>
  );
}

/**
 * Nothing is unread while the owner looks: Her on screen and the window in front, as the main process
 * says it (main.tsx keeps html.blurred; document.hasFocus can be told otherwise, by a test's driver
 * for one). What comes meanwhile is unread until they are back (bots/read), and notified.
 */
function useMarkRead(bot: BotSummary) {
  const her = useHer();
  const unread = bot.unread ?? 0;
  const connected = her.env.rpc !== null;
  useEffect(() => {
    const read = () => {
      if ((bot.unread ?? 0) === 0 || document.documentElement.classList.contains("blurred") || !connected) return;
      her.call("bots/read", { bot_id: bot.id }).catch(() => {});
    };
    read();
    return window.hermesh?.onWindowFocus((focused) => focused && read());
  }, [bot.id, unread, connected]);
}

/** The onboarding while no model works there (ModelOnboarding.tsx), as one row of the conversation. */
type OnboardingItem = { kind: "onboarding"; key: string; ts: number };

type Item = BotItem | WaitingItem | SendingItem | OnboardingItem;

/** A row of the conversation: its item, the time of the one before, and whether it sits close to its neighbours. */
type BotRow = VirtualRow & { item: Item; previous: number | undefined; side: "um" | "bm" | null; joinPrev: boolean; joinNext: boolean };

/** Each kind's height until rows of it are measured: only how long the scrollbar is depends on it. */
const rowHeights: Record<string, number> = { user: 56, waiting: 70, bot: 96, attachment: 160, question: 120, card: 240, session: 76, work: 170, routine: 40, deck_edit: 0, failure: 60, onboarding: 160 };

/** Which bubble an item draws: the owner's, the assistant's, or neither (a line of its own). */
const sideOf = (item: Item): BotRow["side"] => (item.kind === "user" || item.kind === "waiting" || item.kind === "sending" ? "um" : item.kind === "bot" || item.kind === "attachment" || item.kind === "question" || item.kind === "card" || item.kind === "session" || item.kind === "work" ? "bm" : null);

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
  onWaiting: () => Promise<HerWaiting[]>;
}) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const { view, session } = conv;
  const chat = useMemo(() => (view ? botChat(view).filter((i) => i.kind !== "deck_edit") : []), [view]);
  const sent = useOutbox(her.machine.machineId);
  const owners = useMemo(() => chat.flatMap((i) => (i.kind === "user" ? [{ key: i.key, text: i.text, ts: i.ts }] : [])), [chat]);
  const kept = useMemo(() => outboxFor(waiting, bot.id, owners, sent.filter((m) => m.botId === bot.id)), [waiting, bot.id, owners, sent]);
  // What was sent goes once the conversation has it, and what her took that it doesn't have and won't: it would by
  // now, or the conversation opened afresh without it.
  const opened = useRef(conv.connectionId);
  useEffect(() => {
    const fresh = conv.connectionId !== null && conv.connectionId !== opened.current;
    opened.current = conv.connectionId;
    const mine = sent.filter((m) => m.botId === bot.id);
    const left = new Set(stillSending(mine, owners, (m) => fresh || (!!m.due && conv.rpc !== null)).map((m) => m.id));
    outbox.drop(her.machine.machineId, mine.flatMap((m) => (left.has(m.id) ? [] : [m.id])));
  }, [owners, sent, conv.connectionId, conv.rpc]);
  const said = useMemo(() => [...chat, ...kept], [chat, kept]);
  // No model works there: Her's onboarding, the app speaking for Her, until the conversation opens on one.
  const noModel = state.kind === "noModel" || conv.noModel;
  const flow = useOnboarding(her.machine.machineId, {
    confirmed: conv.noModel,
    reported: state.kind === "noModel",
    opened: state.kind === "ready" && conv.rpc && view && !conv.noModel ? view.info.model : null,
    session,
    last: chat.at(-1)?.key ?? null,
  });
  const after = onboardingAfter(flow, { noModel, session, last: said.at(-1)?.key ?? null });
  const items = useMemo<Item[]>(() => withOnboarding<Item, OnboardingItem>(said, after, { kind: "onboarding", key: "onboarding", ts: flow?.at ?? 0 }), [said, after, flow?.at]);
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
  }, [items, !!busy, dock]);
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
  // A choice in the onboarding is the owner's word: the conversation goes to its end, where Her answers
  // it (a key's field, say), even when the owner had scrolled up to a long list's first choices.
  const chose = flow?.said.filter((s) => s.who === "owner").length ?? 0;
  useLayoutEffect(() => {
    if (chose) toEnd();
  }, [chose]);

  const empty = items.length === 0 && !busy;
  const working = view ? runningAgents(view).length : 0;
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
  // A message asked for from beside the conversation (the files Her sent): into view, marked a moment, once the
  // history holds it; earlier pages are read until it does, or until there are none.
  const seeking = useRef<{ key: string; pages: number } | null>(null);
  const seek = () => {
    const want = seeking.current;
    if (!want) return;
    if (items.some((i) => i.key === want.key)) {
      seeking.current = null;
      atEnd.current = false;
      scrollToKey(want.key, {
        then: (el) => {
          el.classList.add("rtn-flash");
          setTimeout(() => el.classList.remove("rtn-flash"), 1500);
        },
      });
    } else if (view?.before && want.pages < 50) {
      if (asking.current) return;
      want.pages++;
      earlier();
    } else {
      seeking.current = null;
      st.show(t("files.notInConversation"));
    }
  };
  useEffect(seek, [items, paging]);
  useEffect(() => {
    const show = (key: string, from: string) => {
      if (session && from !== session) return st.show(t("files.earlierConversation"));
      seeking.current = { key, pages: 0 };
      seek();
    };
    messageShowers.set(key, show);
    return () => {
      if (messageShowers.get(key) === show) messageShowers.delete(key);
    };
  });
  // A reply goes to the composer; the message one replied to comes into view, marked a moment. A core that is not
  // this app's yet (it restarts once its sessions end) would drop what a message replies to: it says so instead.
  const canReply = st.status?.abilities?.includes("her_reply") !== false;
  const replying = useMemo(
    () => ({
      name: bot.name,
      start: (reply: ReplyQuote) => (canReply ? composerReply.get(key)?.(reply) : st.show(t("reply.needsUpdate"))),
      show: (ref: string) => {
        if (!items.some((i) => i.key === ref)) return st.show(t("reply.gone"));
        atEnd.current = false;
        scrollToKey(ref, {
          then: (el) => {
            el.classList.add("rtn-flash");
            setTimeout(() => el.classList.remove("rtn-flash"), 1500);
          },
        });
      },
    }),
    [bot.name, canReply, key, items, scrollToKey, st.show, t],
  );
  const files: FileScope = { rpc: conv.rpc, session: session ?? "", bot: bot.id };
  const drawItem = (item: Item, previous: number | undefined) => {
    const line = <TimeSince at={item.ts} previous={previous} />;
    switch (item.kind) {
      case "user":
        return <UserBubble key={item.key} item={item} fresh={fresh(item.key)} line={line} scope={files} />;
      case "waiting":
        return <WaitingBubble key={item.key} item={item} line={line} onDiscarded={onWaiting} />;
      case "sending":
        return <SendingBubble key={item.key} item={item} line={line} />;
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
      case "card": {
        // Her's words in her bubble, the card under them; the card alone when she wrote none.
        const card = cardOf(item.card);
        const drawn = card && view && <SecureCard card={card} view={view} rpc={conv.rpc} session={session} />;
        return (
          <BotMessage key={item.key} item={item} fresh={fresh(item.key)} line={line} copy={item.text} after={item.text !== undefined ? drawn : undefined}>
            {item.text !== undefined ? (
              <div className="bbub">
                <Markdown text={item.text} />
              </div>
            ) : (
              drawn
            )}
          </BotMessage>
        );
      }
      case "session":
        return (
          <div key={item.key} className="bm">
            {line}
            <SessionCard started={item.started} />
          </div>
        );
      case "work":
        return (
          <div key={item.key} className="bm">
            {line}
            <WorkCard work={item.work} />
          </div>
        );
      case "failure":
        return item.last ? <FailureCard key={item.key} error={item.error} working={working} model={view?.info.model} op={op} stateId={`failure:${item.key}`} /> : <PastFailure key={item.key} error={item.error} stateId={`failure:${item.key}`} />;
      case "onboarding":
        return flow && <Onboarding key={item.key} flow={flow} bot={bot} />;
      default:
        return null;
    }
  };
  return (
    <Replying.Provider value={replying}>
      <div className="cscroll" ref={scrollRef} onScroll={onScroll}>
        <div className="dbody" ref={body} style={{ paddingBottom: dock + 40 }}>
          {view?.before && (
            <div className="tdiv earlier">
              {paging ? <span className="mini-spin" aria-label={t("chat.loadingEarlier")} /> : <button className="link" onClick={earlier}>{t("chat.earlier")}</button>}
            </div>
          )}
          {empty ? (
            // Without a model, Her waits for her's word before it greets (the onboarding).
            <EmptyPage bot={bot} state={state} loading={(!view && state.kind === "ready" && !conv.noModel) || noModel} />
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
              {busy && <BusyBubble busy={busy} retry={view?.live?.retry} />}
            </div>
          )}
        </div>
      </div>
      <button className={`tobottom ${away ? "on" : ""}`} style={{ "--dock": `${dock}px` } as CSSProperties} aria-label={t("chat.toBottom")} title={t("chat.toBottom")} tabIndex={away ? 0 : -1} aria-hidden={!away} onClick={toEnd}>
        <Icon name="arrowDown" />
      </button>
      <ReplyPill root={scroller} />
      <Dock bot={bot} conv={conv} op={op} state={state} onWaiting={onWaiting} onHeight={setDock} />
    </Replying.Provider>
  );
}

/** A new assistant's page, or one whose conversation can't be read now: a line, under its avatar and name at the page's top. Her greets. */
function EmptyPage({ bot, state, loading }: { bot: BotSummary; state: EnvState; loading: boolean }) {
  const { t } = useTranslation("her");
  const her = useHer();
  if (loading) return <div className="bempty"><span className="mini-spin" /></div>;
  const name = her.machine.machineName;
  const line = state.kind === "offline" ? t("chat.unread", { name }) : t("chat.greeting", { name: bot.name, environment: name });
  return (
    <div className="bempty">
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

function UserBubble({ item, fresh, line, scope }: { item: Extract<BotItem, { kind: "user" }>; fresh: boolean; line: ReactNode; scope: FileScope }) {
  const { t } = useTranslation("her");
  const save = useSaveFile(scope.bot);
  // A PDF or Markdown sent opens over the window, from where it was uploaded on the assistant's machine.
  const [shown, setShown] = useState<string | null>(null);
  // A message hermeshd delivered names its files after its words.
  const said = item.files.length === 0 ? splitAttached(item.text) : { text: item.text, files: item.files };
  return (
    <>
      {line}
      <div className={`um ${fresh ? "add-anim" : ""} ${item.queued ? "queued" : ""}`} title={item.queued ? t("chat.queued") : undefined} data-reply-key={said.text !== "" ? item.key : undefined} data-reply-own="">
        {(item.images.length > 0 || said.files.length > 0) && (
          <div className="um-att">
            {item.images.map((im, i) => (
              <Tile key={i} name={t("chat.image")} src={`data:${im.mime_type};base64,${im.data}`} />
            ))}
            {said.files.map((f) => (
              <Tile key={f} name={baseName(f)} views={previews(f)} onOpen={previews(f) ? () => setShown(f) : undefined} />
            ))}
          </div>
        )}
        {shown && <FilePreview scope={scope} path={shown} name={baseName(shown)} onSave={() => void save(shown, baseName(shown))} onClose={() => setShown(null)} />}
        <OwnBubble text={said.text} reply={item.reply} />
        <div className="msg-act">
          <span className="msg-meta">
            <MessageTime at={item.ts} />
          </span>
          {said.text !== "" && <ReplyButton quote={() => quoteOf(item.key, true, said.text)} />}
          <CopyButton text={said.text} />
        </div>
      </div>
    </>
  );
}

/** The owner's words in their bubble, under what they reply to. */
function OwnBubble({ text, reply }: { text: string; reply?: ReplyQuote }) {
  if (text === "" && !reply) return null;
  return (
    <div className="bub sel-text">
      {reply && <QuoteCard reply={reply} />}
      {text !== "" && <span className="bub-t">{text}</span>}
    </div>
  );
}

/**
 * A message written while the assistant's machine was offline, kept on this Mac until it is back
 * (docs/her-server.md §5.5): it says so, and why the last try failed when one did. One her keeps
 * refusing holds back those after it, so it can be discarded.
 */
/** A message this Mac is sending, or couldn't: Try Again sends it again as it was, Edit puts it back in the composer. */
function SendingBubble({ item, line }: { item: SendingItem; line: ReactNode }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const machineId = her.machine.machineId;
  return (
    <>
      {line}
      <div className={`um ${item.refused === null ? "waiting" : ""}`} data-testid="her-sending">
        {(item.files.length > 0 || item.images.length > 0) && (
          <div className="um-att">
            {item.files.map((f) => (
              <Tile key={f} name={f} />
            ))}
          </div>
        )}
        <OwnBubble text={item.text} reply={item.reply} />
        <div className={`um-wait ${item.refused !== null ? "warn" : ""}`} role="status">
          {item.refused === null ? (
            <span>{t("waiting.sending")}</span>
          ) : (
            <>
              <Icon name="alert" className="sm" />
              <span title={item.refused}>{t("waiting.failed", { error: item.refused })}</span>
              <button className="link" onClick={() => sendAgain(machineId, item.id)}>
                {t("waiting.retry")}
              </button>
              <button className="link" onClick={() => editAgain(machineId, item.id)}>
                {t("waiting.edit")}
              </button>
            </>
          )}
        </div>
      </div>
    </>
  );
}

/** Sends what was sent again, by the delivery the composer registered for the environment. */
const deliveries = new Map<string, (id: string) => void>();
const sendAgain = (machineId: string, id: string) => {
  outbox.refuse(machineId, id, null);
  deliveries.get(machineId)?.(id);
};
const editAgain = (machineId: string, id: string) => {
  const m = outbox.get(machineId, id);
  if (!m) return;
  outbox.drop(machineId, [id]);
  composerReturn.get(machineId)?.(m.draft);
};

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
        <OwnBubble text={item.text} reply={item.reply} />
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
      <Icon name={done ? "check" : "copy"} className={done ? "sm tick-in" : "sm"} />
    </button>
  );
}

type SentItem = Extract<BotItem, { kind: "bot" | "attachment" | "question" | "card" }>;

/** A message from the assistant: what it sent, with its time and buttons beside it on hover; `after`, under it. */
function BotMessage({ item, fresh, line, copy, after, children }: { item: SentItem; fresh: boolean; line: ReactNode; copy?: string; after?: ReactNode; children: ReactNode }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const source = item.source;
  return (
    <>
      {line}
      <div className={`bm ${fresh ? "add-anim" : ""}`} data-run={source?.run_id} data-reply-key={copy ? item.key : undefined}>
        <div className="bm-b">
          <div className="bline">
            {children}
            <div className="msg-act">
              <span className="msg-meta">
                <MessageTime at={item.ts} />
              </span>
              {copy && <ReplyButton quote={() => quoteOf(item.key, false, copy)} />}
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
          {after}
        </div>
      </div>
    </>
  );
}

/** What the owner had written to each assistant and not sent yet, by environment and assistant. */
const drafts = new Map<string, Draft>();
/** What failed tasks handed back that is in a composer already. */
const handed = new WeakSet<object>();

/**
 * The composer of an assistant's page: only Send (bot-mode-design §6.14); every message goes in as it
 * is sent, and the composer is free at once (docs/her-sending.md). A message shows as being sent under
 * an id of its own, and goes through hermeshd, which keeps it, then delivers it, on the conversation
 * this page has open or on one of its own, now or when the machine is back; sent again, her takes it
 * once.
 */
function Dock({ bot, conv, op, state, onWaiting, onHeight }: {
  bot: BotSummary;
  conv: Conv;
  op: SendOp;
  state: EnvState;
  onWaiting: () => Promise<HerWaiting[]>;
  onHeight: (h: number) => void;
}) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const view = conv.view;
  const key = her.machine.machineId;
  const asking = view && conv.rpc ? pendingApproval(view) : null;
  const waitsForChrome = useWaitsForChrome(conv.rpc, conv.session);
  const [draft, setDraft] = useState<Draft>(() => drafts.get(key) ?? { text: "", attachments: [] });
  const change = (d: Draft) => {
    setDraft(d);
    drafts.set(key, d);
  };
  // What a failed task handed back goes back to the composer.
  useEffect(() => {
    const returned = view?.returned;
    if (!returned?.length || handed.has(returned)) return;
    handed.add(returned);
    // What one replied to goes back as that, not as words.
    const text = returned.map((m) => m.content.flatMap((c) => (c.type === "text" && !replyOf(c.text) ? [c.text] : [])).join("\n")).filter(Boolean);
    const reply = returned.flatMap((m) => m.content.flatMap((c) => (c.type === "text" ? [replyOf(c.text)] : []))).find((r) => r !== null);
    const d: Draft = drafts.get(key) ?? { text: "", attachments: [] };
    change({ ...d, text: [...text, d.text.trim()].filter(Boolean).join("\n\n"), ...(reply && !d.reply && { reply }) });
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
  // A message not sent, edited: back into the composer, before what is there.
  useEffect(() => {
    const put = (sent: Draft) => {
      const d: Draft = drafts.get(key) ?? { text: "", attachments: [] };
      const reply = sent.reply ?? d.reply;
      change({ text: [sent.text, d.text].filter(Boolean).join("\n"), attachments: addAttachments(sent.attachments, d.attachments), ...(reply && { reply }) });
    };
    composerReturn.set(key, put);
    return () => {
      if (composerReturn.get(key) === put) composerReturn.delete(key);
    };
  }, [key]);
  // What the next message replies to, from a message's Reply or from words selected in one.
  useEffect(() => {
    const put = (reply: ReplyQuote) => {
      change({ ...(drafts.get(key) ?? { text: "", attachments: [] }), reply });
      requestAnimationFrame(() => document.querySelector<HTMLElement>(".her .cmp-in")?.focus());
    };
    composerReply.set(key, put);
    return () => {
      if (composerReply.get(key) === put) composerReply.delete(key);
    };
  }, [key]);
  const name = her.machine.machineName;
  const noModel = state.kind === "noModel" || conv.noModel;
  // The conversation open now, read when a message goes (a retry goes on the one open then).
  const open = useRef(conv);
  open.current = conv;
  // HerSend for a message being sent. Kept in the outbox, the outbox shows it; taken, the conversation will; an error
  // says it is in neither, unless the outbox has it after all (the answer was lost, not the message).
  const deliver = useCallback(
    async (id: string) => {
      const m = outbox.get(key, id);
      if (!m) return;
      const c = open.current;
      // Files go by their path here; pictures by their bytes, kept by hermeshd with the message, which her reads as pictures.
      const paths = m.draft.attachments.flatMap((a) => (a.kind === "file" ? [a.path] : []));
      const pictures = m.draft.attachments.flatMap((a) => (a.kind === "image" ? [{ name: a.name, data: Uint8Array.from(atob(a.data), (ch) => ch.charCodeAt(0)) }] : []));
      try {
        const r = await local.herSend({
          id,
          machineId: key,
          botId: m.botId,
          text: m.draft.text.trim(),
          localPaths: paths,
          pictures,
          reply: m.draft.reply,
          connectionId: c.rpc && c.session ? (c.connectionId ?? "") : "",
          sessionId: c.rpc && c.connectionId ? (c.session ?? "") : "",
        });
        if (!r.delivered) {
          await onWaiting();
          outbox.drop(key, [id]);
        } else outbox.taken(key, id);
      } catch (e) {
        const list = await onWaiting();
        if (list.some((w) => w.id === id)) outbox.drop(key, [id]);
        else outbox.refuse(key, id, errorText(e));
      }
    },
    [key, onWaiting],
  );
  useEffect(() => {
    const again = (id: string) => void deliver(id);
    deliveries.set(key, again);
    return () => {
      if (deliveries.get(key) === again) deliveries.delete(key);
    };
  }, [key, deliver]);
  const send = () => {
    const sent = draft;
    if (!sent.text.trim() && sent.attachments.length === 0) return;
    change({ text: "", attachments: [] });
    const id = messageId();
    outbox.add(key, {
      id,
      botId: bot.id,
      text: sent.text.trim(),
      files: sent.attachments.map((a) => a.name),
      images: sent.attachments.flatMap((a) => (a.kind === "image" ? [{ mime_type: a.mime_type, data: a.data }] : [])),
      ts: Date.now(),
      refused: null,
      ...(sent.reply && { reply: sent.reply }),
      draft: sent,
    });
    void deliver(id);
  };
  // Without a model, the onboarding in the conversation says what to do; the composer waits.
  const above = asking ? (
    <Approval key={`${asking.agent?.id ?? ""}/${asking.approval.call_id}`} view={view!} op={op} />
  ) : waitsForChrome && conv.rpc && conv.session ? (
    <ChromeWait
      rpc={conv.rpc}
      session={conv.session}
      onConnect={() => void window.hermesh?.openHerConsole(her.machine.machineId, "browser/connect").catch((e: unknown) => st.show(errorText(e)))}
    />
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
      quote={draft.reply && { label: draft.reply.own ? t("reply.own") : t("reply.to", { name: bot.name }), text: quoteText(draft.reply.text) }}
      onUnquote={() => change({ text: draft.text, attachments: draft.attachments })}
      onSend={send}
      canSend={!noModel}
      placeholder={noModel ? t("onboard.placeholder") : t("chat.placeholder", { name: bot.name })}
    />
  );
}

/** What keeps the environment from answering, above the composer in an approval's shape. */
function StateNote({ icon, title, body }: { icon: string; title: string; body?: string }) {
  return (
    <div className="knote" role="status">
      <div className="knote-h">
        <Icon name={icon} className="knote-i" />
        <b>{title}</b>
      </div>
      {body && <p className="knote-b sel-text">{body}</p>}
    </div>
  );
}
