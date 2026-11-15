import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  agentBrief, agentCalls, agentElapsed, agentHistoryCursor, agentLine, agentState, agentTaskOf, agentTitles, agentTokens, messageBody, pendingApproval, progressGroups, stepsOf,
  toolObject, type AgentLaunch, type AgentState, type AgentView, type ProgressGroup, type ProgressTask, type SessionView, type StartedSession, type Step,
} from "@hermesh/her-client";
import type { BotSummary } from "@hermesh/her-protocol";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { clockText, codingAgentShort, createdDate, dateOf, displayTitle, elapsedSeconds, isActive, isFailed, pendingQuestion, tokensText } from "@hermesh/core";
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

// Progress (docs/her-team.md §15): the work Her sent out, as cards under what the owner said that set it off, and
// behind each card the whole record of that piece of work. Everything is read from the conversation the Her page
// already follows (its sub-agents' own sessions come with it), so the page asks her for nothing more; a coding
// session's record is the session's own page, shown here.

/** How a card stands: a sub-agent's state, or the same words for a coding session; gone: the window holds the session no more. */
type Stand = AgentState | "gone";

function sessionStand(s: Session | undefined): Stand {
  if (!s) return "gone";
  if (s.status === SessionStatus.WAITING_INPUT) return "waiting";
  if (isFailed(s)) return "failed";
  return isActive(s) ? "running" : "done";
}

const sessionTokens = (s: Session | undefined) => Number((s?.progress?.usage?.inputTokens ?? 0n) + (s?.progress?.usage?.outputTokens ?? 0n));

/** A group as the page draws it: its cards' states, and where it stands as a whole. */
type GroupView = ProgressGroup & { stands: Stand[]; stand: "waiting" | "running" | "ended"; tokens: number; titles: Map<string, string> };

function useGroups(view: SessionView | null): GroupView[] {
  const st = useStore();
  const groups = useMemo(() => (view ? progressGroups(view) : []), [view]);
  return useMemo(
    () =>
      groups.map((g) => {
        const sessions = g.tasks.map((t) => (t.kind === "session" ? st.sessions.find((s) => s.sessionId === t.started.session_id) : undefined));
        const stands = g.tasks.map((t, i): Stand => (t.kind === "agent" ? agentState(t.launch) : sessionStand(sessions[i])));
        const tokens = g.tasks.reduce((n, t, i) => n + (t.kind === "agent" ? (t.launch.agent ? agentTokens(t.launch.agent) : 0) : sessionTokens(sessions[i])), 0);
        const stand = stands.includes("waiting") ? "waiting" : stands.some((s) => s === "running" || s === "starting") ? "running" : "ended";
        return { ...g, stands, stand, tokens, titles: agentTitles(g.tasks) };
      }),
    [groups, st.sessions],
  );
}

const sameTask = (task: ProgressTask, ref: ProgressTaskRef) => ("agent" in ref ? task.kind === "agent" && task.launch.agent?.id === ref.agent : task.kind === "session" && task.started.session_id === ref.session);

export function Progress({ bot, conv, op, task }: { bot: BotSummary; conv: Conv; op: SendOp; task?: ProgressTaskRef }) {
  const her = useHer();
  const groups = useGroups(conv.view);
  const open = task && groups.flatMap((g) => g.tasks.map((t) => ({ group: g, task: t }))).find((x) => sameTask(x.task, task));
  return (
    <ContentScope value={`${her.machine.machineId}/${conv.session ?? ""}`}>
      {open ? <TaskPage key={open.task.key} group={open.group} task={open.task} conv={conv} op={op} /> : <Board bot={bot} groups={groups} conv={conv} missing={!!task} />}
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

// MARK: The board

function Board({ bot, groups, conv, missing }: { bot: BotSummary; groups: GroupView[]; conv: Conv; missing: boolean }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const state = envState(her.machine);
  const now = useNow(30_000);
  // A card that is gone (its agent went back with the conversation, or a link to it is old) is said once.
  const { show } = st;
  useEffect(() => {
    if (missing && conv.view) show(t("progress.gone"));
  }, [missing, !!conv.view]);
  // Those that wait and those at work first, the latest first; then the ended ones.
  const order = { waiting: 0, running: 1, ended: 2 };
  const sorted = useMemo(() => [...groups].reverse().sort((a, b) => order[a.stand] - order[b.stand]), [groups]);
  const firstEnded = sorted.findIndex((g) => g.stand === "ended");
  const count = (stand: Stand[]) => groups.reduce((n, g) => n + g.stands.filter((s) => stand.includes(s)).length, 0);
  const parts = [
    count(["running", "starting"]) > 0 ? t("progress.sumRunning", { count: count(["running", "starting"]) }) : "",
    count(["waiting"]) > 0 ? t("progress.sumWaiting", { count: count(["waiting"]) }) : "",
    count(["done"]) > 0 ? t("progress.sumDone", { count: count(["done"]) }) : "",
  ].filter(Boolean);
  const [paging, setPaging] = useState(false);
  const earlier = () => {
    setPaging(true);
    conv.loadEarlier().finally(() => setPaging(false));
  };
  const view = conv.view;
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
          {parts.length > 0 && <p className="pg-sum">{t("progress.sumLead") + parts.join(t("progress.sumSep")) + t("progress.sumEnd")}</p>}
          {!view ? (
            <div className="pg-empty" role="status">
              {state.kind === "ready" || state.kind === "starting" ? <span className="mini-spin" /> : <span>{t("progress.notRead", { name: her.machine.machineName })}</span>}
            </div>
          ) : groups.length === 0 ? (
            <div className="pg-empty">
              <span className="pg-cat" aria-hidden="true"><Cat small /></span>
              <b>{t("progress.empty")}</b>
              <span>{conv.rpc || view.agents.length > 0 ? t("progress.emptySub") : t("progress.notRead", { name: her.machine.machineName })}</span>
            </div>
          ) : (
            <div className="pg-list" role="list" aria-label={t("progress.log")}>
              {sorted.map((g, i) => (
                <div key={g.key} role="listitem">
                  {i === firstEnded && <div className="pg-sec">{t("progress.ended")}</div>}
                  <Group bot={bot} group={g} />
                </div>
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

/**
 * What the owner said (or what else set the work off), Her's last words since, and under them the cards. A group that
 * had ended when the page came up is folded until asked for; one that ends while it shows stays as it is.
 */
function Group({ bot, group: g }: { bot: BotSummary; group: GroupView }) {
  const { t } = useTranslation(["her", "common"]);
  const now = useNow(30_000);
  const go = useOpen();
  const [open, setOpen] = useState(g.stand !== "ended");
  const p = g.parent;
  const who = p.kind === "user" ? t("progress.you") : p.kind === "routine" ? t("progress.routine", { name: p.name }) : t("progress.earlier");
  const waits = g.stands.filter((s) => s === "waiting").length;
  const running = g.stands.filter((s) => s === "running" || s === "starting").length;
  return (
    <section className="pg-g" aria-label={p.kind === "user" ? p.text : who}>
      <div className="pg-gh">
        <button className="pg-gt" aria-expanded={open} onClick={() => setOpen(!open)}>
          <span className="pg-who" aria-hidden="true">
            <Icon name={p.kind === "user" ? "person" : "clock"} />
          </span>
          <span className="pg-gm">
            <span className="pg-when">{p.kind === "earlier" ? who : `${who} · ${messageTime(new Date(p.ts), now)}`}</span>
            {p.kind === "user" && <span className="pg-ask">{p.text}</span>}
            {g.said && (
              <span className="pg-said">
                <span className="pg-said-cat" aria-hidden="true"><Cat small /></span>
                <span title={bot.name}>{g.said.split("\n").find((l) => l.trim() !== "") ?? ""}</span>
              </span>
            )}
          </span>
          <span className="pg-marks">
            {waits > 0 && <span className="pg-wait"><i className="dot-w" aria-hidden="true" />{t("progress.waits")}</span>}
            {running > 0 && <span className="busy" role="img" aria-label={t("common:sidebar.running", { count: running })}><i aria-hidden="true" /><span aria-hidden="true">{running}</span></span>}
            {g.stand === "ended" && <Icon name="check" className="tm-ok" />}
            <span className="pg-n">{[t("progress.routes", { count: g.tasks.length }), g.tokens > 0 ? t("progress.tokens", { tokens: tokensText(g.tokens) }) : ""].filter(Boolean).join(" · ")}</span>
            <Icon name="chevD" className={`pg-chev ${open ? "open" : ""}`} />
          </span>
        </button>
        {p.kind === "user" && (
          <button className="tb" title={t("progress.inChat")} aria-label={t("progress.inChat")} onClick={go.chat}>
            <Icon name="ext" />
          </button>
        )}
      </div>
      {open && (
        <div className="pg-cards">
          {g.tasks.map((task, i) => (task.kind === "agent" ? <AgentCard key={task.key} launch={task.launch} title={g.titles.get(task.key) ?? ""} stand={g.stands[i]} /> : <SessionTaskCard key={task.key} started={task.started} />))}
        </div>
      )}
    </section>
  );
}

/** The mark at a card's top right: a ring while it works, a dot and a word when it waits or failed, a check when done. */
function StandMark({ stand }: { stand: Stand }) {
  const { t } = useTranslation("her");
  switch (stand) {
    case "starting":
    case "running":
      return <span className="busy" role="img" aria-label={t(`progress.state.${stand}`)}><i aria-hidden="true" /></span>;
    case "waiting":
      return <span className="pg-wait"><i className="dot-w" aria-hidden="true" />{t("progress.state.waiting")}</span>;
    case "done":
      return <Icon name="check" className="tm-ok" />;
    case "failed":
      return <span className="pg-fail"><i className="dot-f" aria-hidden="true" />{t("progress.state.failed")}</span>;
    case "stopped":
      return <span className="pg-off">{t("progress.state.stopped")}</span>;
    default:
      return null;
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

/** A sub-agent's card: who, how it stands, what the work is, what it does now or how it ended, how long and how many tokens. */
function AgentCard({ launch, title, stand }: { launch: AgentLaunch; title: string; stand: Stand }) {
  const { t } = useTranslation("her");
  const go = useOpen();
  const a = launch.agent;
  const line = agentLine(launch);
  const facts = useFacts(a);
  return (
    <button className="pg-c" disabled={!a} onClick={() => a && go.task({ agent: a.id })} aria-label={`${title} · ${t(`progress.state.${stand as AgentState}`)}`}>
      <span className="pg-ct">
        <span className="pg-kit" aria-hidden="true"><Icon name="kit" /></span>
        <span className="pg-doer">{t("progress.agent")}</span>
        <StandMark stand={stand} />
      </span>
      <b className="pg-title">{title}</b>
      <span className="pg-line">
        {line?.kind === "tool" ? (
          <>
            {toolVerb(line.tool)} {line.tool.category === "execute" ? <code>{toolObject(line.tool)}</code> : toolObject(line.tool)}
          </>
        ) : (
          (line?.text ?? (stand === "starting" ? t("progress.starting") : stand === "running" ? t("progress.thinking") : ""))
        )}
      </span>
      <span className="pg-facts">{facts}</span>
    </button>
  );
}

/** A coding session Her started, as its card: kept current from the sessions the window holds, as her card in the conversation is. */
function SessionTaskCard({ started }: { started: StartedSession }) {
  const { t } = useTranslation(["her", "common"]);
  const st = useStore();
  const go = useOpen();
  const s = st.sessions.find((x) => x.sessionId === started.session_id);
  const stand = sessionStand(s);
  const now = useNow(stand === "running" ? 1000 : 3_600_000);
  const agent = s?.agent ?? started.agent;
  const title = s ? displayTitle(s) : started.title;
  const d = s?.progress?.diffStat;
  const line =
    !s ? t("progress.sessionGone")
    : stand === "waiting" ? (pendingQuestion(s)?.items[0]?.text || t("common:team.waitingForAnswer"))
    : stand === "failed" ? (s.progress?.lastFailure?.message || t("common:team.lastTurnFailed"))
    : stand === "running" ? (s.progress?.currentTool ?? "")
    : d && d.filesChanged > 0 ? t("common:team.changed", { count: d.filesChanged, add: d.insertions, del: d.deletions })
    : t("common:team.done");
  const tokens = sessionTokens(s);
  const facts = [
    s && stand === "running" ? t("progress.ran", { time: clockText(elapsedSeconds(dateOf(s.progress?.turnStartedAt) ?? createdDate(s), now)) }) : "",
    tokens > 0 ? t("progress.tokens", { tokens: tokensText(tokens) }) : "",
  ].filter(Boolean).join(" · ");
  return (
    <button className="pg-c" disabled={!s} onClick={() => go.task({ session: started.session_id })} aria-label={`${title} · ${stand === "gone" ? t("progress.sessionGone") : t(`progress.state.${stand}`)}`}>
      <span className="pg-ct">
        <AgentLogo agent={agent} size={22} />
        <span className="pg-doer">{codingAgentShort(agent)} · {s ? machineName(st, s.machineId) : started.machine}</span>
        <StandMark stand={stand} />
      </span>
      <b className="pg-title">{title}</b>
      <span className="pg-line">{stand === "running" && line ? <code>{line}</code> : line}</span>
      <span className="pg-facts">{facts}</span>
    </button>
  );
}

// MARK: A card's page

/** A card opened: what set it off and its siblings at the top, then the record of that piece of work. */
function TaskPage({ group, task, conv, op }: { group: GroupView; task: ProgressTask; conv: Conv; op: SendOp }) {
  const { t } = useTranslation("her");
  const go = useOpen();
  const st = useStore();
  const now = useNow(30_000);
  const p = group.parent;
  const said = p.kind === "user" ? `${t("progress.you")} · ${messageTime(new Date(p.ts), now)}：${p.text}` : p.kind === "routine" ? t("progress.routine", { name: p.name }) : t("progress.earlier");
  const session = task.kind === "session" ? st.sessions.find((s) => s.sessionId === task.started.session_id) : undefined;
  return (
    <>
      <div className="ctool drag pt-bar">
        <button className="btn pt-back" onClick={go.board}>
          <Icon name="chevL" />
          {t("progress.back")}
        </button>
        <span className="pt-said" title={p.kind === "user" ? p.text : undefined}>{said}</span>
        {session && <button className="link" onClick={() => st.select(session.sessionId)}>{t("progress.openSession")}</button>}
        {p.kind === "user" && <button className="link" onClick={go.chat}>{t("progress.inChat")}</button>}
      </div>
      {group.tasks.length > 1 && (
        <div className="pt-sibs" role="group" aria-label={t("progress.siblings")}>
          {group.tasks.map((x, i) => {
            const ref = refOf(x);
            const title = x.kind === "agent" ? (group.titles.get(x.key) ?? "") : (st.sessions.find((s) => s.sessionId === x.started.session_id)?.title || x.started.title);
            return (
              <button key={x.key} className={`pt-sib ${x === task ? "sel" : ""}`} aria-pressed={x === task} disabled={!ref} onClick={() => ref && go.task(ref)}>
                <SibMark stand={group.stands[i]} />
                <span>{title}</span>
              </button>
            );
          })}
        </div>
      )}
      {task.kind === "agent" && task.launch.agent ? (
        <AgentRecord launch={task.launch} agent={task.launch.agent} title={group.titles.get(task.key) ?? ""} conv={conv} op={op} />
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
                  <span className="pg-kit" aria-hidden="true"><Icon name="kit" /></span>
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
