import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import {
  agentBrief, agentCalls, agentElapsed, agentHistoryCursor, agentPost, agentState, agentTaskOf, agentTitles, agentTokens, messageBody, pendingApproval, progressGroups, stepsOf,
  toolObject, toolOutput, type AgentLaunch, type AgentPost, type AgentState, type AgentView, type ProgressGroup, type ProgressTask, type SessionView, type Step, type WorkItem,
} from "@hermesh/her-client";
import type { BotSummary } from "@hermesh/her-protocol";
import { clockText, elapsedSeconds, listTime, tokensText, turnTimeLine } from "@hermesh/core";
import { Icon } from "../icons";
import { layoutOf, setCard } from "../layout";
import { keys, ownKeys } from "../platform";
import { AgentText, ChatFrame, ControlBar, DoneLine, FailCard, FailLine, TimeLine, ToolFold, ToolLine, UserBubble } from "../session/Chat";
import { ContentScope } from "../session/scope";
import { useRowState } from "../session/useRowState";
import type { VirtualRow } from "../session/useVirtualRows";
import { useNow, useStore, type ProgressTaskRef } from "../store";
import { Approval, type SendOp } from "./Approval";
import { Cat } from "./Cat";
import type { Conversation as Conv } from "./connection";
import { useHer } from "./context";
import { OfflineLine } from "./Conversation";
import { envState } from "./environments";
import { durationText, messageTime, toolVerb, workText } from "./format";
import "./progress.css";

// Progress (docs/her-team.md §15): the work Her sent out to her sub-agents as a feed, one post a piece of work, with
// what set it off at its foot; behind each post the whole record of that piece of work. Everything is read from the
// conversation the Her page already follows (its sub-agents' own sessions come with it), so the page asks her for
// nothing more. Coding sessions she started are not here: they are on their own pages.

/** How a post stands: its sub-agent's state. */
type Stand = AgentState;

const atWork = (s: Stand) => s === "running" || s === "starting";
/** It waits for the owner, or could not go on: the posts that carry a mark, above the rest. */
const needsOwner = (s: Stand) => s === "waiting" || s === "failed";
const parentTime = (g: ProgressGroup) => (g.parent.kind === "earlier" ? 0 : g.parent.ts);

/** A piece of work as the feed shows it: the work, what set it off, how it stands, and what its post says (read once, here). */
interface Post {
  task: ProgressTask;
  group: ProgressGroup;
  stand: Stand;
  title: string;
  post: AgentPost;
  /** When it began its work (at work) or last changed (otherwise): the feed's order. */
  at: number;
}

function usePosts(view: SessionView | null): { groups: ProgressGroup[]; posts: Post[] } {
  const groups = useMemo(() => (view ? progressGroups(view) : []), [view]);
  const posts = useMemo(
    () =>
      groups
        .flatMap((g) => {
          const titles = agentTitles(g.tasks);
          return g.tasks.map((t): Post => ({ task: t, group: g, stand: agentState(t.launch), title: titles.get(t.key) ?? "", post: agentPost(t.launch), at: t.launch.agent?.since ?? parentTime(g) }));
        })
        .sort((a, b) => b.at - a.at),
    [groups],
  );
  return { groups, posts };
}

const sameTask = (task: ProgressTask, ref: ProgressTaskRef) => task.launch.agent?.id === ref.agent;

export function Progress({ bot, conv, op, task }: { bot: BotSummary; conv: Conv; op: SendOp; task?: ProgressTaskRef }) {
  const her = useHer();
  const { groups, posts } = usePosts(conv.view);
  // What set work off, shown whole over the page: a group's key.
  const [source, setSource] = useState<string | null>(null);
  const open = task && posts.find((p) => sameTask(p.task, task));
  const sourceGroup = source ? groups.find((g) => g.key === source) : undefined;
  return (
    <ContentScope value={`${her.machine.machineId}/${conv.session ?? ""}`}>
      {open ? <TaskPage key={open.task.key} bot={bot} post={open} conv={conv} op={op} onSource={setSource} backToHer={task?.fromHer ? bot.name : undefined} /> : <Board bot={bot} posts={posts} conv={conv} missing={!!task} onSource={setSource} />}
      {sourceGroup && <SourcePanel bot={bot} group={sourceGroup} posts={posts} here={open?.task.key} onClose={() => setSource(null)} />}
    </ContentScope>
  );
}

function useOpen() {
  const st = useStore();
  const her = useHer();
  const machine = her.machine.machineId;
  return {
    task: (ref: ProgressTaskRef) => st.set({ selection: { kind: "progress", machine, task: ref } }),
    board: () => st.set({ selection: { kind: "progress", machine } }),
    chat: () => st.set({ selection: { kind: "her", machine } }),
  };
}

const refOf = (task: ProgressTask): ProgressTaskRef | null => (task.launch.agent ? { agent: task.launch.agent.id } : null);

// MARK: The feed

/** The feed, a page of Her's (her top bar's Progress opens it): what needs the owner, then what is at work now, then what ended; the latest first in each. */
function Board({ bot, posts, conv, missing, onSource }: { bot: BotSummary; posts: Post[]; conv: Conv; missing: boolean; onSource: (key: string) => void }) {
  const { t } = useTranslation("her");
  const go = useOpen();
  const her = useHer();
  const st = useStore();
  const state = envState(her.machine);
  const now = useNow(30_000);
  // A post that is gone (its agent went back with the conversation, or a link to it is old) is said once.
  const { show } = st;
  useEffect(() => {
    if (missing && conv.view) show(t("progress.gone"));
  }, [missing, !!conv.view]);
  const [paging, setPaging] = useState(false);
  const earlier = () => {
    setPaging(true);
    conv.loadEarlier().finally(() => setPaging(false));
  };
  const view = conv.view;
  const sections = [
    { key: "need", title: t("progress.part.need"), posts: posts.filter((p) => needsOwner(p.stand)) },
    { key: "now", title: t("progress.part.now"), posts: posts.filter((p) => atWork(p.stand)) },
    { key: "earlier", title: t("progress.part.earlier"), posts: posts.filter((p) => !needsOwner(p.stand) && !atWork(p.stand)) },
  ].filter((x) => x.posts.length > 0);
  return (
    <>
      {/* A page of Her's: back to her conversation. */}
      <div className="ctool drag pt-bar">
        <button className="btn pt-back" onClick={go.chat}>
          <Icon name="chevL" />
          {bot.name}
        </button>
      </div>
      <OfflineLine state={state} />
      <div className="cscroll">
        <div className="pg">
          <div className="pg-h">
            <h1>{t("progress.title")}</h1>
            <span>{now.toLocaleDateString(undefined, { month: "long", day: "numeric", weekday: "long" })}</span>
          </div>
          {!view ? (
            <div className="pg-empty" role="status">
              {state.kind === "ready" || state.kind === "starting" ? <span className="mini-spin" /> : <span>{t("progress.notRead", { name: her.machine.machineName })}</span>}
            </div>
          ) : posts.length === 0 ? (
            <div className="pg-empty">
              <span className="pg-cat" aria-hidden="true"><Cat small /></span>
              <b>{t("progress.empty")}</b>
              <span>{conv.rpc || view.agents.length > 0 ? t("progress.emptySub") : t("progress.notRead", { name: her.machine.machineName })}</span>
            </div>
          ) : (
            <div className="pf" role="list" aria-label={t("progress.log")}>
              {sections.map((x) => (
                <section key={x.key} role="listitem" aria-label={x.title}>
                  <h3 className="pg-sec">{x.title}</h3>
                  {x.posts.map((p) => <PostView key={p.task.key} post={p} onSource={onSource} />)}
                </section>
              ))}
            </div>
          )}
          {view?.before && conv.rpc && (
            <div className="pg-more">
              {paging ? <span className="mini-spin" aria-label={t("progress.loadingWork")} /> : <button className="link" onClick={earlier}>{t("progress.earlierWork")}</button>}
            </div>
          )}
        </div>
      </div>
    </>
  );
}

/** "21 min": how long something has been at work, in its largest unit. */
function useWorked() {
  const { t } = useTranslation("her");
  return (ms: number) => {
    const s = Math.max(1, Math.floor(ms / 1000));
    return s < 60 ? t("time.seconds", { count: s }) : s < 3600 ? t("time.minutes", { count: Math.floor(s / 60) }) : t("time.hours", { count: Math.floor(s / 3600) });
  };
}

/**
 * A post: its title, a few lines of how it goes, one piece that shows where it stands, and at its foot one time (how
 * long it has worked, or how long ago it ended) with the tokens it used, and ⓘ, which shows what set it off over the
 * page. Only one that needs the owner carries a mark.
 */
function PostView({ post: p, onSource }: { post: Post; onSource: (key: string) => void }) {
  const { t } = useTranslation(["her", "common"]);
  const go = useOpen();
  const worked = useWorked();
  const now = useNow(atWork(p.stand) || p.stand === "waiting" ? 10_000 : 60_000);
  const ref = refOf(p.task);
  const open = () => ref && go.task(ref);
  const since = p.task.launch.agent?.since ?? p.at;
  const words = p.post.words;
  const tokens = p.task.launch.agent ? agentTokens(p.task.launch.agent) : 0;
  const stateWord = t(`progress.state.${p.stand}`);
  return (
    <article className="pf-p" aria-label={p.title}>
      <button className="pf-main" disabled={!ref} onClick={open} aria-label={[p.title, stateWord, words].filter(Boolean).join(" · ")}>
        <span className="pf-t">
          {needsOwner(p.stand) && <i className={p.stand === "waiting" ? "dot-w" : "dot-f"} aria-hidden="true" />}
          <b>{p.title}</b>
        </span>
        {words && <span className="pf-w">{words}</span>}
      </button>
      <Piece post={p} open={open} />
      <div className="pf-foot">
        {atWork(p.stand) || p.stand === "waiting" ? (
          <span className={`pf-time ${atWork(p.stand) ? "live" : ""}`}>{worked(now.getTime() - since)}</span>
        ) : (
          <span className="pf-time">{listTime(new Date(p.at), now)}</span>
        )}
        {tokens > 0 && <span className="pf-tok" aria-label={t("progress.tokens", { tokens: tokensText(tokens) })}>· {tokensText(tokens)}</span>}
        <button className="tb pf-why" aria-label={t("progress.why")} title={t("progress.why")} disabled={p.group.parent.kind === "earlier"} onClick={() => onSource(p.group.key)}>
          <Icon name="info" />
        </button>
      </div>
    </article>
  );
}

/** What shows where a post stands: the step it is on while it works, what waits for the owner, what it made. */
function Piece({ post: p, open }: { post: Post; open: () => void }) {
  const { t } = useTranslation("her");
  const post = p.post;
  switch (p.stand) {
    case "starting":
      return <div className="pf-well">{t("progress.starting")}</div>;
    case "running":
      return (
        <button className="pf-well pf-now" onClick={open}>
          <Beat count={post.calls} />
          {post.now ? (
            <span className="pf-step">
              <span>{toolVerb(post.now)}</span> {post.now.category === "execute" ? <code>{toolObject(post.now)}</code> : <b>{toolObject(post.now)}</b>}
            </span>
          ) : (
            <span className="pf-step">{t("progress.thinking")}</span>
          )}
        </button>
      );
    case "waiting":
      return (
        <div className="pf-well pf-ask">
          <span>{t("progress.state.waiting")}</span>
          <button className="btn primary" onClick={open}>{t("progress.review")}</button>
        </div>
      );
    case "done":
      if (!post.files.length) return null;
      return (
        <button className="pf-well pf-files" onClick={open}>
          <Icon name="doc" />
          <b>{post.files[0].split("/").at(-1)}</b>
          {post.files.length > 1 && <span>{t("progress.files", { count: post.files.length })}</span>}
        </button>
      );
    default:
      return null;
  }
}

/** A sub-agent's calls as bars, the last beating while it works: at most the last eight. */
function Beat({ count }: { count: number }) {
  const heights = [8, 12, 8, 16, 11, 8, 16, 12, 16, 11, 8, 14];
  const n = Math.min(Math.max(count, 1), 8);
  return (
    <span className="pf-beat" aria-hidden="true">
      {Array.from({ length: n }, (_, i) => (
        <i key={i} className={i === n - 1 ? "now" : ""} style={{ height: heights[(count - n + i + heights.length) % heights.length] }} />
      ))}
    </span>
  );
}

/**
 * What set work off, shown whole over the page: what the owner said and when, what Her said back, and everything
 * it sent out, each as it stands; a click opens one. Esc or a click outside closes it.
 */
function SourcePanel({ bot, group: g, posts, here, onClose }: { bot: BotSummary; group: ProgressGroup; posts: Post[]; here?: string; onClose: () => void }) {
  const { t } = useTranslation("her");
  const go = useOpen();
  const now = useNow(60_000);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    box.current?.focus();
    const key = (e: KeyboardEvent) => e.key === "Escape" && (e.stopPropagation(), onClose());
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [onClose]);
  const p = g.parent;
  const mine = posts.filter((x) => x.group.key === g.key);
  const title = p.kind === "routine" ? t("progress.fromRoutine") : t("progress.fromSaid");
  return (
    <>
      <button className="tm-scrim" aria-label={t("progress.close")} onClick={onClose} />
      <div className="tm-panel pf-src" role="dialog" aria-label={title} tabIndex={-1} ref={box}>
        <div className="pf-src-h">
          <div>
            <h2>{title}</h2>
            {p.kind !== "earlier" && <span>{p.kind === "user" ? t("progress.toHer", { time: messageTime(new Date(p.ts), now) }) : `${t("progress.routine", { name: p.name })} · ${messageTime(new Date(p.ts), now)}`}</span>}
          </div>
          <button className="tb" aria-label={t("progress.close")} onClick={onClose}><Icon name="x" /></button>
        </div>
        {p.kind === "user" && <p className="pf-me sel-text">{p.text}</p>}
        {g.said && (
          <div className="pf-her">
            <span className="pf-her-cat" aria-hidden="true"><Cat small /></span>
            <p className="sel-text" title={bot.name}>{g.said}</p>
          </div>
        )}
        {mine.length > 1 && (
          <section aria-label={t("progress.sent", { count: mine.length })}>
            <h3>{t("progress.sent", { count: mine.length })}</h3>
            {mine.map((x) => {
              const ref = refOf(x.task);
              const sel = x.task.key === here;
              return (
                <button key={x.task.key} className={`pf-sib ${sel ? "sel" : ""}`} aria-current={sel || undefined} disabled={!ref} onClick={() => (onClose(), ref && go.task(ref))}>
                  <SibMark stand={x.stand} />
                  <span>
                    <b>{x.title}</b>
                    <span>{t(`progress.state.${x.stand}`)}</span>
                  </span>
                  <Icon name="chevR" className="sm" />
                </button>
              );
            })}
          </section>
        )}
        <button className="btn pf-chat" onClick={() => (onClose(), go.chat())}>{t("progress.inChat")}</button>
      </div>
    </>
  );
}

// MARK: A post's page

/**
 * A post opened, as a session's page is (SessionPage): back to the feed (or to her conversation, where it was opened
 * from: `backToHer`, her name), its title, and the info card's button; then its record, with the info card beside it.
 */
function TaskPage({ bot, post: p, conv, op, onSource, backToHer }: { bot: BotSummary; post: Post; conv: Conv; op: SendOp; onSource: (key: string) => void; backToHer?: string }) {
  const { t } = useTranslation(["her", "session"]);
  const go = useOpen();
  const st = useStore();
  const { card } = layoutOf(st);
  const info = card !== "hidden";
  const a = p.task.launch.agent;
  return (
    <>
      <div className="ctool drag pt-bar">
        <button className="btn pt-back" onClick={backToHer ? go.chat : go.board}>
          <Icon name="chevL" />
          {backToHer ?? t("progress.back")}
        </button>
        <div className="ctitle">
          <h2>{p.title}</h2>
        </div>
        <button
          className={`tb ${info ? "on" : ""}`}
          title={t(info ? "session:page.hideInfo" : "session:page.showInfo", { keys: keys(ownKeys.toggleInspector) })}
          aria-label={t("session:page.info")}
          aria-pressed={info}
          onClick={() => setCard(st, !info)}
        >
          <Icon name="infoCard" />
        </button>
      </div>
      {a ? (
        <div className={`dwrap pt-rec ${card === "docked" ? "dock" : ""}`}>
          <div className="dmain">
            <AgentChat bot={bot} launch={p.task.launch} agent={a} title={p.title} conv={conv} op={op} />
          </div>
          {info && <AgentInspector bot={bot} post={p} agent={a} onSource={() => onSource(p.group.key)} />}
        </div>
      ) : (
        <div className="pg-empty"><span>{t("progress.gone")}</span></div>
      )}
    </>
  );
}

function SibMark({ stand }: { stand: Stand }) {
  switch (stand) {
    case "starting":
    case "running":
      return <span className="busy" aria-hidden="true"><i /></span>;
    case "waiting":
      return <i className="dot-w" aria-hidden="true" />;
    case "done":
      return <Icon name="check" className="tm-ok sm" />;
    case "failed":
      return <i className="dot-f" aria-hidden="true" />;
    default:
      return <i className="pt-off" aria-hidden="true" />;
  }
}

/**
 * The info card of a sub-agent, as a session's is (Inspector): who sent it out, how it stands, how long it has
 * worked or took, the tokens it used, its steps, and what set it off, which opens over the page.
 */
function AgentInspector({ bot, post: p, agent: a, onSource }: { bot: BotSummary; post: Post; agent: AgentView; onSource: () => void }) {
  const { t } = useTranslation(["her", "session"]);
  const running = a.status === "running";
  const now = useNow(running ? 1000 : 60_000);
  const took = agentElapsed(a, now.getTime());
  const tokens = agentTokens(a);
  const g = p.group.parent;
  const from = g.kind === "user" ? t("progress.fromYou", { time: messageTime(new Date(g.ts), now), text: g.text }) : g.kind === "routine" ? `${t("progress.routine", { name: g.name })} · ${messageTime(new Date(g.ts), now)}` : t("progress.earlier");
  return (
    <div className="insp-col">
      <aside className="insp" aria-label={t("session:inspector.label")}>
        <h3 className="insp-h">{t("progress.agent")}</h3>
        <ul className="insp-facts">
          <li>
            <span className="insp-cat" aria-hidden="true"><Cat small /></span>
            <span className="dim" title={bot.name}><Trans t={t} i18nKey="progress.sentBy" values={{ name: bot.name }} components={{ b: <b /> }} /></span>
          </li>
          <li>
            <span className="insp-mark"><SibMark stand={p.stand} /></span>
            <span>{t(`progress.state.${p.stand}`)}</span>
          </li>
          {(running || took > 0) && (
            <li>
              <Icon name="clock" />
              <span className="pt-time">{running ? t("progress.ran", { time: clockText(elapsedSeconds(new Date(a.since), now)) }) : t("progress.took", { time: durationText(took) })}</span>
            </li>
          )}
          {tokens > 0 && (
            <li>
              <Icon name="gauge" />
              <span>{t("progress.tokens", { tokens: tokensText(tokens) })}</span>
            </li>
          )}
          <li>
            <Icon name="checklist" />
            <span>{t("progress.steps", { count: agentCalls(a) })}</span>
          </li>
          <li>
            {g.kind === "earlier" ? (
              <>
                <Icon name="q" />
                <span className="dim">{from}</span>
              </>
            ) : (
              <button className="insp-pick" aria-label={t("progress.why")} title={g.kind === "user" ? g.text : undefined} onClick={onSource}>
                <Icon name="q" />
                <span>{from}</span>
              </button>
            )}
          </li>
        </ul>
      </aside>
    </div>
  );
}

/** A row of a sub-agent's record: a step of it, a time line before what it was told, or where earlier steps can be read. */
type AgentRow = VirtualRow & ({ kind: "step"; step: Step; end: boolean } | { kind: "brief"; text: string; ts?: number } | { kind: "turn"; at: Date; previous?: Date } | { kind: "earlier" });

/** Each kind's height until rows of it are measured: only how long the scrollbar is depends on it. */
const agentRowHeights: Record<string, number> = { step: 60, brief: 64, turn: 30, earlier: 28 };

/**
 * The rows of a sub-agent's record, as a session's conversation has them: what Her asked of it first (`brief`; its
 * first message is the task, shown so), then where earlier steps can be read (`more`), then what it thought, said
 * and called, and what Her told it after it began, each with a time line before it after a pause. A text that ends
 * a round is the end of its reply.
 */
function agentRows(all: Step[], brief: string, more: boolean, running: boolean, failed: boolean): AgentRow[] {
  const steps = all[0]?.kind === "user" ? all.slice(1) : all;
  const out: AgentRow[] = [];
  let previous: Date | undefined;
  const told = (key: string, ts: number | undefined) => {
    if (!ts) return;
    const at = new Date(ts);
    if (turnTimeLine(at, previous, new Date()) !== null) out.push({ key: `${key}:at`, kind: "turn", at, previous });
    previous = at;
  };
  const first = all[0]?.kind === "user" ? all[0].ts : undefined;
  if (brief) {
    told("brief", first);
    out.push({ key: "brief", kind: "brief", text: brief, ts: first });
  }
  if (more) out.push({ key: "earlier", kind: "earlier" });
  // The failure it stopped on is told once, under it all (FailCard); an earlier one in its line.
  const last = failed ? steps.findLastIndex((s) => s.kind === "end") : -1;
  steps.forEach((s, i) => {
    if (i === last) return;
    if (s.kind === "user" || s.kind === "agent_message") told(s.key, s.ts);
    if (s.kind !== "text" && s.kind !== "work" && s.kind !== "user" && s.kind !== "agent_message" && !(s.kind === "end" && s.end.outcome !== "completed")) return;
    const next = steps.slice(i + 1).find((x) => x.kind !== "work");
    const end = s.kind === "text" && !s.live && (next ? next.kind === "end" || next.kind === "user" || next.kind === "agent_message" : !running);
    out.push({ key: s.key, kind: "step", step: s, end });
  });
  return out;
}

/**
 * A sub-agent's record, as a session's conversation is (ChatFrame): what Her asked of it on the right, then its words
 * across the page and what it did in between as grey lines, folded until opened, and how it ended. While it works,
 * the control bar at the bottom says so, with what it asks the owner above, and Stop.
 */
function AgentChat({ bot, launch, agent: a, title, conv, op }: { bot: BotSummary; launch: AgentLaunch; agent: AgentView; title: string; conv: Conv; op: SendOp }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const stand = agentState(launch);
  const running = a.status === "running";
  const all = stepsOf(a.view);
  const brief = agentBrief((conv.view && agentTaskOf(conv.view, a)) || launch.task);
  const cursor = conv.rpc ? agentHistoryCursor(a) : undefined;
  const rows = useMemo(() => agentRows(all, brief, !!cursor, running, stand === "failed"), [all, brief, !!cursor, running, stand]);
  const [paging, setPaging] = useState(false);
  const earlier = () => {
    setPaging(true);
    conv.loadAgentEarlier(a.id).finally(() => setPaging(false));
  };
  const [busy, setBusy] = useState(false);
  const stop = async () => {
    setBusy(true);
    if (await op({ type: "agent_interrupt", agent: a.id })) st.show(t("progress.stopSent", { title }));
    setBusy(false);
  };
  const asking = conv.view && conv.rpc ? pendingApproval(conv.view) : null;
  const mine = asking?.agent?.id === a.id;
  const row = (r: AgentRow): ReactNode => {
    switch (r.kind) {
      case "brief":
        return <UserBubble text={r.text} at={r.ts ? new Date(r.ts) : undefined} />;
      case "turn":
        return <TimeLine at={r.at} previous={r.previous} />;
      case "earlier":
        return (
          <div className="pg-more">
            {paging ? <span className="mini-spin" aria-label={t("progress.loadingEarlier")} /> : <button className="link" onClick={earlier}>{t("progress.earlierSteps")}</button>}
          </div>
        );
      case "step":
        return <AgentStep step={r.step} end={r.end} />;
    }
  };
  return (
    <ChatFrame
      rows={rows}
      initial={agentRowHeights}
      label={t("progress.steps", { count: agentCalls(a) })}
      follow={a.status}
      row={row}
      foot={
        <>
          {stand === "waiting" && !mine && <div className="tnote">{t("progress.waitingBehind")}</div>}
          {stand === "failed" && <FailCard headline={t("progress.failedHere")} hints={[]} message={a.error?.message ?? ""} />}
          {stand === "stopped" && <div className="tnote">{t("progress.state.stopped")}</div>}
          {stand === "done" && <DoneLine text={t("progress.doneLine", { name: bot.name })} />}
        </>
      }
      control={
        running
          ? (onHeight) => (
              <ControlBar panel={mine && conv.view && <Approval key={asking!.approval.call_id} view={conv.view} op={op} />} onHeight={onHeight}>
                <AgentLive agent={a} waiting={stand === "waiting"} />
                {conv.rpc && (
                  <>
                    <span className="ctl-sep" />
                    <button className="ctl-btn" disabled={busy} onClick={() => void stop()}>
                      <Icon name="stop" />
                      {t("progress.stop")}
                    </button>
                  </>
                )}
              </ControlBar>
            )
          : undefined
      }
    />
  );
}

/** What runs now, in the control bar: the sub-agent at work and for how long, or that it waits for the owner. */
function AgentLive({ agent: a, waiting }: { agent: AgentView; waiting: boolean }) {
  const { t } = useTranslation("her");
  const now = useNow(1000);
  if (waiting) {
    return (
      <>
        <i className="dot-w" aria-hidden="true" />
        <b>{t("progress.state.waiting")}</b>
      </>
    );
  }
  return (
    <>
      <span className="live-logo pt-cat" aria-hidden="true"><Cat small /></span>
      <b>{t("progress.working")}</b>
      <span className="el">{clockText(elapsedSeconds(new Date(a.since), now))}</span>
    </>
  );
}

/** A step of the record, as a session's row of the same kind shows it. end: the last words of a round. */
function AgentStep({ step: s, end }: { step: Step; end: boolean }): ReactNode {
  const { t } = useTranslation("her");
  switch (s.kind) {
    case "work":
      return s.items.length === 1 ? (
        <WorkLine item={s.items[0]} />
      ) : (
        <ToolFold id={s.key} summary={workText(s.items)} running={s.items.some(atWorkNow)}>
          {s.items.map((it) => <WorkLine key={it.key} item={it} />)}
        </ToolFold>
      );
    case "text":
      return <AgentText text={s.text} end={end} at={new Date(s.ts)} live={s.live} />;
    // What Her told it after it began: as what was sent to it.
    case "user":
    case "agent_message":
      return <UserBubble text={messageBody(s.text)} at={new Date(s.ts)} />;
    case "end":
      return s.end.outcome === "interrupted" ? <div className="tnote">{t("progress.interrupted")}</div> : <FailLine id={s.key} title={t("progress.failedHere")} message={s.end.error?.message ?? ""} />;
    default:
      return null;
  }
}

const atWorkNow = (it: WorkItem) => (it.kind === "thinking" ? it.live : it.tool.state === "running" || it.tool.state === "pending" || it.tool.state === "streaming");

/** A thought or a call on its line (ToolLine): a thought opens to its words, a call to its output. */
function WorkLine({ item }: { item: WorkItem }) {
  const { t } = useTranslation("her");
  const [open, setOpen] = useRowState(`tool:${item.key}`, false);
  if (item.kind === "thinking") {
    const text = item.redacted ? "" : item.text.trim();
    const gist = item.redacted ? t("steps.thinkingHidden") : (text.split("\n").find((l) => l.trim() !== "") ?? "");
    return <ToolLine verb={t("steps.thought")} object={gist} title="" shell={false} running={item.live} failed={false} output={text} open={open} onToggle={() => setOpen(!open)} prose />;
  }
  const tool = item.tool;
  return (
    <ToolLine
      verb={toolVerb(tool)}
      object={toolObject(tool)}
      title={tool.title}
      shell={tool.category === "execute"}
      running={atWorkNow(item)}
      failed={tool.state === "failed" || tool.state === "denied"}
      output={toolOutput(tool)}
      open={open}
      onToggle={() => setOpen(!open)}
    />
  );
}
