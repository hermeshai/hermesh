import { Trans, useTranslation } from "react-i18next";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { clockText, elapsedSeconds, codingAgentTitle, createdDate, currentTurn, dateOf, failureTitle, isActive, isFailed, minutesLeft, pendingQuestion, statusTitle, tokensText } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { GrantAvatar, StatusDot } from "../components";
import { Icon } from "../icons";
import { grantLabel, machineName } from "../Sidebar";
import { useNow, useStore } from "../store";

/**
 * A card floating over the conversation's top right: what a glance should
 * give. While a session is live or has failed, first its state (what it does
 * and for how long, or why it stopped; a finished one has nothing the list
 * doesn't say), then who started it, with which coding agent, where; then
 * what it has done so far. Shown or hidden with the toolbar's button (⌥⌘0).
 */
export function Inspector({ s }: { s: Session }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const now = useNow(1000);
  const grant = grantLabel(st, s);
  const m = st.machines.find((x) => x.machineId === s.machineId);
  const d = s.progress?.diffStat;
  const u = s.progress?.usage;
  const tokens = (u?.inputTokens ?? 0n) + (u?.outputTokens ?? 0n);
  const working = isActive(s) || s.status === SessionStatus.WAITING_INPUT;
  const from = dateOf(s.progress?.turnStartedAt) ?? createdDate(s);
  const tone = s.status === SessionStatus.WAITING_INPUT ? "w" : isFailed(s) ? "r" : "";
  return (
    <aside className="insp" aria-label={t("inspector.label")}>
      {(working || isFailed(s)) && (
        <div className="insp-state">
          <div className="insp-now">
            <span className={`insp-st ${tone}`}>
              <StatusDot s={s} />
              {stateWord(s)}
            </span>
            {working && <span className="insp-time">{clockText(elapsedSeconds(from, now))}</span>}
          </div>
          <div className={`insp-why ${tone}`}>{detail(s, now)}</div>
        </div>
      )}

      <ul className="insp-facts">
        <li>
          <GrantAvatar label={grant} />
          <span><Trans t={t} i18nKey="inspector.startedBy" values={{ grant }} components={{ b: <b /> }} /></span>
        </li>
        <li>
          <AgentLogo agent={s.agent} size={16} />
          <span>{codingAgentTitle(s.agent)}</span>
        </li>
        <li>
          <Icon name="laptop" className="sm" />
          <span>{m?.local ? t("thisMac") : machineName(st, s.machineId)} / <b>{s.projectId}</b></span>
        </li>
      </ul>

      <div className="insp-sum">
        <span><Trans t={t} i18nKey="inspector.turns" count={currentTurn(s)} components={{ b: <b /> }} /></span>
        <span>
          {d && d.filesChanged > 0 ? (
            <>
              <Trans t={t} i18nKey="inspector.files" count={d.filesChanged} components={{ b: <b /> }} /> <i className="add">+{d.insertions}</i> <i className="del">−{d.deletions}</i>
            </>
          ) : (
            t("inspector.noChanges")
          )}
        </span>
        {tokens > 0n && <span><b>{tokensText(tokens)}</b> tokens</span>}
      </div>
    </aside>
  );
}

function stateWord(s: Session): string {
  if (s.status === SessionStatus.WAITING_INPUT) return i18n.t("session:waiting");
  if (s.status === SessionStatus.CRASHED) return i18n.t("session:inspector.crashed");
  if (isFailed(s)) return i18n.t("session:inspector.failed");
  return statusTitle(s.status);
}

/** The line under the state: which turn runs, how long a question waits, or why it failed. */
function detail(s: Session, now: Date): string {
  if (s.status === SessionStatus.WAITING_INPUT) {
    const until = dateOf(pendingQuestion(s)?.expiresAt);
    return until
      ? i18n.t("session:inspector.turnLeft", { turn: currentTurn(s), left: minutesLeft(until, now) })
      : i18n.t("session:inspector.turn", { turn: currentTurn(s) });
  }
  if (isFailed(s)) return failureTitle(s.progress?.lastFailure?.kind ?? 0);
  return i18n.t("session:inspector.turn", { turn: currentTurn(s) });
}
