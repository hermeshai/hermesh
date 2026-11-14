import { useTranslation } from "react-i18next";
import type { StartedSession, TeamWork } from "@hermesh/her-client";
import { SessionStatus } from "@hermesh/proto/session";
import { clockText, codingAgentShort, createdDate, dateOf, displayTitle, elapsedSeconds, isActive, isFailed, tokensText } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { GrantAvatar } from "../components";
import { Icon } from "../icons";
import { machineName, senderName } from "../Sidebar";
import { useNow, useStore } from "../store";

// Her's cards in her conversation (docs/her-team.md §4.4, §6.3, §14.3), drawn as on the iPhone at the
// conversation's width.

/**
 * A coding session Her started, where she started it: the agent, the title, where it runs, and how it stands now,
 * kept current from the sessions the window holds; it opens the session. One the window holds no more shows as the
 * call named it, without a state. It answers no question: news of her own sessions goes to Her first.
 */
export function SessionCard({ started }: { started: StartedSession }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const now = useNow(1000);
  const s = st.sessions.find((x) => x.sessionId === started.session_id);
  const agent = s?.agent ?? started.agent;
  const title = s ? displayTitle(s) : started.title;
  const where = s ? machineName(st, s.machineId) : started.machine;
  const d = s?.progress?.diffStat;
  const state =
    !s ? null
    : s.status === SessionStatus.WAITING_INPUT ? <span className="hc-wait">{t("team.waitingForAnswer")}</span>
    : isFailed(s) ? <span className="hc-fail">{s.progress?.lastFailure?.message || t("team.lastTurnFailed")}</span>
    : isActive(s) ? (
        <span className="hc-run">
          {s.progress?.currentTool && <code>{s.progress.currentTool}</code>}
          <span>{clockText(elapsedSeconds(dateOf(s.progress?.turnStartedAt) ?? createdDate(s), now))}</span>
        </span>
      )
    : d && d.filesChanged > 0 ? <span>{t("team.changed", { count: d.filesChanged, add: d.insertions, del: d.deletions })}</span>
    : <span>{t("team.done")}</span>;
  const mark =
    !s ? null
    : s.status === SessionStatus.WAITING_INPUT ? <i className="dot-w" aria-hidden="true" />
    : isFailed(s) ? <i className="dot-f" aria-hidden="true" />
    : isActive(s) ? <span className="busy" aria-hidden="true"><i /></span>
    : <Icon name="check" className="tm-ok" />;
  return (
    <button className="her-card hc-session" disabled={!s} onClick={() => s && st.select(s.sessionId)} aria-label={title}>
      <AgentLogo agent={agent} size={30} />
      <span className="hc-main">
        <b>{title}</b>
        <span>{codingAgentShort(agent)} · {where}</span>
        {state && <span className="hc-state">{state}</span>}
      </span>
      {mark && <span className="hc-mark">{mark}</span>}
    </button>
  );
}

/** The team's work Her counted for the owner: the sessions, what came of them, a bar each hour or day, the agents and who sent the work. It opens Work. */
export function WorkCard({ work }: { work: TeamWork }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const top = Math.max(1, ...work.buckets);
  const client = (id: string) => st.clients.find((c) => c.clientId === id);
  return (
    <button className="her-card hc-work" onClick={() => st.set({ selection: { kind: "overview" } })} aria-label={t("team.openWork")}>
      <span className="hcw-top">
        <b>{work.sessions}</b>
        <span>{work.days === 1 ? t("team.sessionsToday") : t("team.sessionsInDays", { count: work.days })}</span>
        <Icon name="chevR" className="sm" />
      </span>
      <span className="hcw-sum">{t("team.workSummary", { done: work.done, turns: work.turns, tokens: tokensText(work.tokens) })}</span>
      {work.buckets.length > 0 && (
        <span className="hcw-bars" aria-hidden="true">
          {work.buckets.map((n, i) => <i key={i} className={n > 0 ? "on" : ""} style={{ height: Math.max(3, (n / top) * 30) }} />)}
        </span>
      )}
      <span className="hcw-groups">
        {work.agents.slice(0, 3).map((a) => (
          <span key={a.id}><AgentLogo agent={a.id} size={15} />{codingAgentShort(a.id)} {a.sessions}</span>
        ))}
      </span>
      <span className="hcw-groups">
        {work.senders.slice(0, 4).map((g) => {
          const c = client(g.id);
          return <span key={g.id}>{c && <GrantAvatar c={c} size="sm" />}{senderName(st, g.id)} {g.sessions}</span>;
        })}
      </span>
    </button>
  );
}
