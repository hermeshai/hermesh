import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  agentBrief, agentCalls, agentElapsed, agentHistoryCursor, agentPost, agentState, agentTaskOf, agentTitles, agentTokens, messageBody, pendingApproval, progressGroups, stepsOf,
  toolObject, type AgentLaunch, type AgentPost, type AgentState, type AgentView, type ProgressGroup, type ProgressTask, type SessionView, type Step,
} from "@hermesh/her-client";
import type { BotSummary } from "@hermesh/her-protocol";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { clockText, codingAgentShort, dateOf, displayTitle, elapsedSeconds, isActive, isFailed, listTime, pendingQuestion, tokensText, updatedDate } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { Icon } from "../icons";
import { Chat } from "../session/Chat";
import { ContentScope } from "../session/scope";
import { useSessionEvents } from "../session/events";
import { Markdown } from "../session/Markdown";
import { machineName } from "../Sidebar";
import { useNow, useStore, type ProgressTaskRef } from "../store";
import { Approval, type SendOp } from "./Approval";
import { Cat } from "./Cat";
import type { Conversation as Conv } from "./connection";
import { useHer } from "./context";
import { OfflineLine } from "./Conversation";
import { envState } from "./environments";
import { durationText, messageTime, toolVerb } from "./format";
import { StepRow } from "./Process";
import "./progress.css";

// Progress (docs/her-team.md §15): the work Her sent out as a feed, one post a piece of work, with what set it off
// at its foot; behind each post the whole record of that piece of work. Everything is read from the conversation
// the Her page already follows (its sub-agents' own sessions come with it), so the page asks her for nothing more;
// a coding session's record is the session's own page, shown here.

/** How a post stands: a sub-agent's state, or the same words for a coding session; gone: the window holds the session no more. */
type Stand = AgentState | "gone";

function sessionStand(s: Session | undefined): Stand {
  if (!s) return "gone";
  if (s.status === SessionStatus.WAITING_INPUT) return "waiting";
  if (isFailed(s)) return "failed";
  return isActive(s) ? "running" : "done";
}

const sessionTokens = (s: Session | undefined) => Number((s?.progress?.usage?.inputTokens ?? 0n) + (s?.progress?.usage?.outputTokens ?? 0n));
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
  /** A sub-agent's. */
  post: AgentPost | null;
  session?: Session;
  /** When it began its work (at work) or last changed (otherwise): the feed's order. */
  at: number;
}

function usePosts(view: SessionView | null): { groups: ProgressGroup[]; posts: Post[] } {
  const st = useStore();
  const groups = useMemo(() => (view ? progressGroups(view) : []), [view]);
  const posts = useMemo(
    () =>
      groups
        .flatMap((g) => {
          const titles = agentTitles(g.tasks);
          return g.tasks.map((t): Post => {
            if (t.kind === "agent") return { task: t, group: g, stand: agentState(t.launch), title: titles.get(t.key) ?? "", post: agentPost(t.launch), at: t.launch.agent?.since ?? parentTime(g) };
            const s = st.sessions.find((x) => x.sessionId === t.started.session_id);
            const at = s ? ((isActive(s) && dateOf(s.progress?.turnStartedAt)) || updatedDate(s)).getTime() : parentTime(g);
            return { task: t, group: g, stand: sessionStand(s), title: s ? displayTitle(s) : t.started.title, post: null, session: s, at };
          });
        })
        .sort((a, b) => b.at - a.at),
    [groups, st.sessions],
  );
  return { groups, posts };
}

const sameTask = (task: ProgressTask, ref: ProgressTaskRef) => ("agent" in ref ? task.kind === "agent" && task.launch.agent?.id === ref.agent : task.kind === "session" && task.started.session_id === ref.session);

export function Progress({ bot, conv, op, task }: { bot: BotSummary; conv: Conv; op: SendOp; task?: ProgressTaskRef }) {
  const her = useHer();
  const { groups, posts } = usePosts(conv.view);
  // What set work off, shown whole over the page: a group's key.
  const [source, setSource] = useState<string | null>(null);
  const open = task && posts.find((p) => sameTask(p.task, task));
  const sourceGroup = source ? groups.find((g) => g.key === source) : undefined;
  return (
    <ContentScope value={`${her.machine.machineId}/${conv.session ?? ""}`}>
      {open ? <TaskPage key={open.task.key} post={open} conv={conv} op={op} onSource={setSource} /> : <Board posts={posts} conv={conv} missing={!!task} onSource={setSource} />}
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

const refOf = (task: ProgressTask): ProgressTaskRef | null => (task.kind === "session" ? { session: task.started.session_id } : task.launch.agent ? { agent: task.launch.agent.id } : null);

// MARK: The feed

/** The feed: what needs the owner, then what is at work now, then what ended; the latest first in each. */
function Board({ posts, conv, missing, onSource }: { posts: Post[]; conv: Conv; missing: boolean; onSource: (key: string) => void }) {
  const { t } = useTranslation("her");
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
      <div className="ctool drag">
        <div className="ctitle" />
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
 * A post: its title and one time (how long it has worked, or how long ago it ended), a line of how it goes, one
 * piece that shows where it stands, and what set it off. Only one that needs the owner carries a mark.
 */
function PostView({ post: p, onSource }: { post: Post; onSource: (key: string) => void }) {
  const { t } = useTranslation(["her", "common"]);
  const go = useOpen();
  const worked = useWorked();
  const now = useNow(atWork(p.stand) || p.stand === "waiting" ? 10_000 : 60_000);
  const ref = refOf(p.task);
  const open = () => ref && go.task(ref);
  const s = p.session;
  const since = p.task.kind === "agent" ? (p.task.launch.agent?.since ?? p.at) : ((s && dateOf(s.progress?.turnStartedAt)?.getTime()) || p.at);
  const words =
    p.task.kind === "agent" ? p.post?.words
    : !s ? t("progress.sessionGone")
    : p.stand === "waiting" ? (pendingQuestion(s)?.items[0]?.text || t("common:team.waitingForAnswer"))
    : p.stand === "failed" ? (s.progress?.lastFailure?.message || t("common:team.lastTurnFailed"))
    : null;
  const tokens = p.task.kind === "agent" ? (p.task.launch.agent ? agentTokens(p.task.launch.agent) : 0) : sessionTokens(s);
  const stateWord = p.stand === "gone" ? t("progress.sessionGone") : t(`progress.state.${p.stand}`);
  return (
    <article className="pf-p" aria-label={p.title}>
      <button className="pf-main" disabled={!ref} onClick={open} aria-label={[p.title, stateWord, words].filter(Boolean).join(" · ")}>
        <span className="pf-t">
          {needsOwner(p.stand) && <i className={p.stand === "waiting" ? "dot-w" : "dot-f"} aria-hidden="true" />}
          <b>{p.title}</b>
          {atWork(p.stand) || p.stand === "waiting" ? (
            <span className={`pf-time ${atWork(p.stand) ? "live" : ""}`}>{worked(now.getTime() - since)}</span>
          ) : (
            <span className="pf-time">{listTime(new Date(p.at), now)}</span>
          )}
        </span>
        {words && <span className="pf-w">{words}</span>}
      </button>
      <Piece post={p} open={open} />
      <div className="pf-foot">
        <FromCapsule group={p.group} onClick={() => onSource(p.group.key)} />
        {tokens > 0 && <span className="pf-tok" aria-label={t("progress.tokens", { tokens: tokensText(tokens) })}>{tokensText(tokens)}</span>}
      </div>
    </article>
  );
}

/** What shows where a post stands: the step it is on while it works, what waits for the owner, what it made. */
function Piece({ post: p, open }: { post: Post; open: () => void }) {
  const { t } = useTranslation(["her", "common"]);
  const s = p.session;
  if (p.task.kind === "agent") {
    const post = p.post;
    switch (p.stand) {
      case "starting":
        return <div className="pf-well">{t("progress.starting")}</div>;
      case "running":
        return (
          <button className="pf-well pf-now" onClick={open}>
            <Beat count={post?.calls ?? 0} />
            {post?.now ? (
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
        if (!post?.files.length) return null;
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
  if (!s) return null;
  if (p.stand === "running")
    return (
      <button className="pf-well pf-now" onClick={open}>
        <span className="busy" aria-hidden="true"><i /></span>
        <span className="pf-step">{s.progress?.currentTool ? <code>{s.progress.currentTool}</code> : t("progress.working")}</span>
      </button>
    );
  const d = s.progress?.diffStat;
  if (p.stand === "done" && d && d.filesChanged > 0)
    return (
      <button className="pf-well pf-diff" onClick={open}>
        <b className="add">+{d.insertions}</b>
        <b className="del">−{d.deletions}</b>
        <span>{t("progress.files", { count: d.filesChanged })}</span>
      </button>
    );
  return null;
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

/** What set work off, as a capsule: "You 10:58 · …". */
function FromCapsule({ group: g, onClick }: { group: ProgressGroup; onClick: () => void }) {
  const { t } = useTranslation("her");
  const now = useNow(60_000);
  const p = g.parent;
  const text = p.kind === "user" ? t("progress.fromYou", { time: messageTime(new Date(p.ts), now), text: p.text }) : p.kind === "routine" ? `${t("progress.routine", { name: p.name })} · ${messageTime(new Date(p.ts), now)}` : t("progress.earlier");
  return (
    <button className="pf-from" disabled={p.kind === "earlier"} onClick={onClick} title={p.kind === "user" ? p.text : undefined}>
      <Icon name="q" />
      <span>{text}</span>
    </button>
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
                    <span>{x.stand === "gone" ? t("progress.sessionGone") : t(`progress.state.${x.stand}`)}</span>
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

/** A post opened: back to the feed and what set it off, then the record of that piece of work. */
function TaskPage({ post: p, conv, op, onSource }: { post: Post; conv: Conv; op: SendOp; onSource: (key: string) => void }) {
  const { t } = useTranslation("her");
  const go = useOpen();
  const st = useStore();
  const task = p.task;
  const session = p.session;
  return (
    <>
      <div className="ctool drag pt-bar">
        <button className="btn pt-back" onClick={go.board}>
          <Icon name="chevL" />
          {t("progress.back")}
        </button>
        <FromCapsule group={p.group} onClick={() => onSource(p.group.key)} />
        <span className="pt-gap" />
        {session && <button className="link" onClick={() => st.select(session.sessionId)}>{t("progress.openSession")}</button>}
      </div>
      {task.kind === "agent" && task.launch.agent ? (
        <AgentRecord launch={task.launch} agent={task.launch.agent} title={p.title} conv={conv} op={op} />
      ) : session ? (
        <SessionRecord s={session} />
      ) : (
        <div className="pg-empty"><span>{t(task.kind === "session" ? "progress.sessionGone" : "progress.gone")}</span></div>
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

/** How long and how much: "Running 4:12 · 55K tokens", "Took 13 min 7 s · 86K tokens". */
function useFacts(a: AgentView | undefined): string {
  const { t } = useTranslation("her");
  const running = a?.status === "running";
  const now = useNow(running ? 1000 : 3_600_000);
  if (!a) return "";
  const tokens = agentTokens(a);
  const took = agentElapsed(a, now.getTime());
  const time = running ? t("progress.ran", { time: clockText(elapsedSeconds(new Date(a.since), now)) }) : took > 0 ? t("progress.took", { time: durationText(took) }) : "";
  return [time, tokens > 0 ? t("progress.tokens", { tokens: tokensText(tokens) }) : ""].filter(Boolean).join(" · ");
}

/** A coding session's record is its own page's conversation, drawn here: nothing is read twice, and it answers its question as there. */
function SessionRecord({ s }: { s: Session }) {
  const st = useStore();
  const { events, draft } = useSessionEvents(s.sessionId, s.progress?.lastEventSeq ?? 0n);
  return (
    <div className="dwrap pt-session">
      <div className="dmain">
        <div className="pt-shead">
          <AgentLogo agent={s.agent} size={22} />
          <b>{displayTitle(s)}</b>
          <span>{codingAgentShort(s.agent)} · {machineName(st, s.machineId)}</span>
        </div>
        <Chat key={s.sessionId} s={s} events={events} draft={draft} />
      </div>
    </div>
  );
}

/**
 * A sub-agent's record: what Her asked of it, then everything it thought, said and called, in order, and how it
 * ended. Its thoughts show whole; a call is a line that opens to its output. While it works the page follows what
 * it does, as long as the owner has not scrolled away from the end.
 */
function AgentRecord({ launch, agent: a, title, conv, op }: { launch: AgentLaunch; agent: AgentView; title: string; conv: Conv; op: SendOp }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const stand = agentState(launch);
  const facts = useFacts(a);
  const all = stepsOf(a.view);
  // Its first message is the task, which shows as what Her asked.
  const steps = all[0]?.kind === "user" ? all.slice(1) : all;
  const brief = (conv.view && agentTaskOf(conv.view, a)) || launch.task;
  const [allOpen, setAllOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const stop = async () => {
    setBusy(true);
    if (await op({ type: "agent_interrupt", agent: a.id })) st.show(t("progress.stopSent", { title }));
    setBusy(false);
  };
  const asking = conv.view && conv.rpc ? pendingApproval(conv.view) : null;
  const scroller = useRef<HTMLDivElement>(null);
  const atEnd = useRef(a.status === "running");
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
  }, [all, asking]);
  const cursor = conv.rpc ? agentHistoryCursor(a) : undefined;
  const [paging, setPaging] = useState(false);
  const earlier = () => {
    const el = scroller.current;
    const from = el ? el.scrollHeight - el.scrollTop : 0;
    setPaging(true);
    conv.loadAgentEarlier(a.id).finally(() => {
      setPaging(false);
      // What was on screen stays where it was.
      requestAnimationFrame(() => el && (el.scrollTop = el.scrollHeight - from));
    });
  };
  const calls = agentCalls(a);
  const lastText = stand === "done" ? steps.findLastIndex((s) => s.kind === "text") : -1;
  return (
    <div className="dwrap pt-agent">
      <div className="dmain">
        <div className="cscroll" ref={scroller} onScroll={(e) => (atEnd.current = e.currentTarget.scrollHeight - e.currentTarget.scrollTop - e.currentTarget.clientHeight < 40)}>
          <div className="pt">
            <div className="pt-h">
              <div className="pt-hm">
                <h2>{title}</h2>
                <div className="pt-meta">
                  <span>{t("progress.agent")}</span>
                  <span className={`pt-pill ${stand}`}>{t(`progress.state.${stand}`)}</span>
                  <span className="pt-facts">{facts}</span>
                </div>
              </div>
              {a.status === "running" && conv.rpc && (
                <button className="btn" disabled={busy} onClick={() => void stop()}>
                  {t("progress.stop")}
                </button>
              )}
            </div>
            {brief && (
              <div className="pt-brief">
                <span className="pt-brief-cat" aria-hidden="true"><Cat small /></span>
                <div>
                  <span>{t("progress.brief")}</span>
                  <p className="sel-text">{agentBrief(brief)}</p>
                </div>
              </div>
            )}
            <div className="pt-steps-h">
              <span>{t("progress.steps", { count: calls })}</span>
              {calls > 0 && <button className="link" onClick={() => setAllOpen(!allOpen)}>{t(allOpen ? "progress.thinkingOnly" : "progress.expandAll")}</button>}
            </div>
            {cursor && (
              <div className="pg-more">
                {paging ? <span className="mini-spin" aria-label={t("progress.loadingEarlier")} /> : <button className="link" onClick={earlier}>{t("progress.earlierSteps")}</button>}
              </div>
            )}
            <div className="pt-steps" role="log" aria-label={t("progress.steps", { count: calls })}>
              {steps.map((s, i) => (
                <StepView key={s.key} step={s} allOpen={allOpen} report={i === lastText} />
              ))}
            </div>
            {stand === "waiting" && conv.view && asking?.agent?.id === a.id && (
              <div className="pt-ask">
                <Approval view={conv.view} op={op} />
              </div>
            )}
            {stand === "waiting" && asking?.agent?.id !== a.id && <p className="pt-note">{t("progress.waitingBehind")}</p>}
            {stand === "failed" && (
              <div className="pt-end fail" role="status">
                <span><i className="dot-f" aria-hidden="true" />{t("progress.failedHere")}</span>
                <p className="sel-text">{a.error?.message ?? ""}</p>
              </div>
            )}
            {stand === "stopped" && <p className="pt-note">{t("progress.state.stopped")}</p>}
          </div>
        </div>
      </div>
    </div>
  );
}

/** A step of the record. report: the agent's last words, which are its report to Her. */
function StepView({ step: s, allOpen, report }: { step: Step; allOpen: boolean; report: boolean }): ReactNode {
  const { t } = useTranslation("her");
  switch (s.kind) {
    case "work":
      return (
        <div className="pt-work">
          {s.items.map((it) => (
            // Opened again as the choice changes: each row keeps its own state after that.
            <StepRow key={`${it.key}:${allOpen}`} item={it} thinkingOpen outputOpen={allOpen} spin />
          ))}
        </div>
      );
    case "text":
      return report ? (
        <div className="pt-end">
          <span><Icon name="check" className="tm-ok sm" />{t("progress.report")}</span>
          <div className="am"><Markdown text={s.text} /></div>
        </div>
      ) : (
        <div className="am pt-say"><Markdown text={s.text} /></div>
      );
    // What Her told it after it began: as her words to it.
    case "user":
    case "agent_message":
      return (
        <div className="pt-again">
          <span>{t("progress.again")}</span>
          <p className="sel-text">{messageBody(s.text)}</p>
        </div>
      );
    case "end":
      return s.end.outcome === "interrupted" ? <p className="pt-note">{t("progress.interrupted")}</p> : null;
    default:
      return null;
  }
}
