import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { clockText, elapsedSeconds, createdDate, dateOf, displayTitle, failureTitle, isActive, isFailed } from "@hermesh/core";
import { grantColor, Mark, type GrantLook } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { grantFace, StatusDot } from "../components";
import { Icon } from "../icons";
import { grantLabel, machineName } from "../Sidebar";
import { useNow, useStore, type Store } from "../store";
import { liveOnMap, sessionKeys } from "./Map";

/** Questions first, then failures, then the turns in progress; oldest first within each. */
function order(s: Session): number {
  return s.status === SessionStatus.WAITING_INPUT ? 0 : isFailed(s) ? 1 : 2;
}

/**
 * "Working Now": a card per live session with what matters about it: which
 * session, its status, how long its turn has worked, who started it, and
 * where (machine / project). pick, a node clicked on the map, narrows the
 * cards to the sessions through it; the heading names it and each card marks
 * the part that matches.
 */
export function Working({ pick, onClear }: { pick: string; onClear: () => void }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const live = st.sessions.filter(liveOnMap);
  const running = live.filter(isActive).length;
  const cards = live
    .filter((s) => !pick || sessionKeys(s).includes(pick))
    .sort((a, b) => order(a) - order(b) || createdDate(a).getTime() - createdDate(b).getTime());
  return (
    <section aria-label={t("working.title")}>
      <div className="sec-h">
        {t("working.title")}
        {running > 0 && !pick && <span className="n">{t("runningCount", { count: running })}</span>}
        {pick && <Condition pick={pick} count={cards.length} onClear={onClear} />}
        {/* A machine only has no grants to manage. */}
        {!st.status?.machineOnly && <button className="link" onClick={() => window.hermesh?.openSettings("grants")}>{t("working.manageAccess")}</button>}
      </div>
      {live.length === 0 ? (
        <div className="card"><Idle /></div>
      ) : cards.length === 0 ? (
        <div className="card wnone">{t("working.noneThrough")}</div>
      ) : (
        <div className="wcards" role="list">
          {cards.map((s) => <Card key={s.sessionId} s={s} pick={pick} />)}
        </div>
      )}
    </section>
  );
}

/** What the cards are narrowed to, in words, with the way back. */
function Condition({ pick, count, onClear }: { pick: string; count: number; onClear: () => void }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const { icon, text } = describe(st, pick);
  return (
    <span className="wcond" role="status">
      {icon}
      <span>{text}</span>
      <span className="n">{t("working.matches", { count })}</span>
      <button onClick={onClear}>{t("working.showAll")}</button>
    </span>
  );
}

function describe(st: Store, pick: string): { icon: ReactNode; text: string } {
  const id = pick.slice(2);
  if (pick.startsWith("g:")) {
    const c = st.clients.find((x) => x.clientId === id);
    const label = c?.label ?? i18n.t("overview:working.disconnected");
    return { icon: <GrantIcon c={c ?? { label }} size={16} />, text: i18n.t("overview:working.byGrant", { label }) };
  }
  if (pick.startsWith("m:")) {
    const m = st.machines.find((x) => x.machineId === id);
    return { icon: <Icon name={m ? machineIcon(m) : "server"} className="sm" />, text: i18n.t("overview:working.onMachine", { machine: m?.local ? i18n.t("overview:thisMac") : machineName(st, id) }) };
  }
  return { icon: <Icon name="folder" className="sm" />, text: i18n.t("overview:working.inProject", { project: id.split("/").slice(1).join("/") }) };
}

function Card({ s, pick }: { s: Session; pick: string }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const [g, m, p] = sessionKeys(s);
  const grant = grantLabel(st, s);
  const local = st.machines.find((x) => x.machineId === s.machineId)?.local;
  const tone = s.status === SessionStatus.WAITING_INPUT ? "w" : isFailed(s) ? "r" : isActive(s) ? "run" : "";
  return (
    <button className={`wcard ${tone}`} role="listitem" onClick={() => st.select(s.sessionId)}>
      <span className="wc-top">
        <Status s={s} />
        <Worked s={s} />
      </span>
      <b className="wc-title" title={displayTitle(s)}>{displayTitle(s)}</b>
      <span className="wc-foot">
        <span className={`wc-by ${pick === g ? "hit" : ""}`}>
          <GrantIcon c={st.clients.find((c) => c.clientId === s.createdBy) ?? { label: grant }} size={16} />
          {grant}
        </span>
        <span className="arrow">→</span>
        <span className={`wc-at ${pick === m ? "hit" : ""}`}>{local ? t("thisMac") : machineName(st, s.machineId)}</span>
        <i>/</i>
        <b className={pick === p ? "hit" : ""}>{s.projectId}</b>
      </span>
    </button>
  );
}

/** The status in a word beside its dot. */
function Status({ s }: { s: Session }) {
  const { t } = useTranslation("overview");
  let word = t("working.running");
  let tone = "";
  if (s.status === SessionStatus.WAITING_INPUT) [word, tone] = [t("working.waiting"), "w"];
  else if (isFailed(s)) [word, tone] = [s.status === SessionStatus.CRASHED ? t("working.crashed") : t("working.failed"), "r"];
  else if (s.status === SessionStatus.PREPARING) word = t("working.preparing");
  else if (s.status === SessionStatus.QUEUED) word = t("working.queued");
  return (
    <span className={`wst ${tone}`}>
      <StatusDot s={s} />
      {word}
    </span>
  );
}

/** How long the turn in progress has worked (a ticking clock); for a failure, why. */
function Worked({ s }: { s: Session }) {
  const now = useNow(1000);
  if (isFailed(s)) return <span className="wc-why">{failureTitle(s.progress?.lastFailure?.kind ?? 0)}</span>;
  const from = dateOf(s.progress?.turnStartedAt) ?? createdDate(s);
  return <span className="wc-time">{clockText(elapsedSeconds(from, now))}</span>;
}

function GrantIcon({ c, size }: { c: GrantLook; size: number }) {
  const label = c.label;
  const face = grantFace(c, size);
  if (face) return face;
  return (
    <span className="av" style={{ width: size, height: size, fontSize: size * 0.55, background: grantColor(label) }} aria-hidden="true">
      {label.slice(0, 1).toUpperCase()}
    </span>
  );
}

function machineIcon(m: Machine): string {
  if (m.local) return "laptop";
  const p = (m.status?.platform ?? "").toLowerCase();
  return p.includes("darwin") || p.includes("mac") ? "mini" : "server";
}

/** Nothing live: the mark at rest, and what will show here. */
function Idle() {
  const { t } = useTranslation("overview");
  return (
    <div className="wrest" role="status">
      <Mark size={34} className="wrest-mark" />
      <b>{t("working.nothingRuns")}</b>
      <p>{t("working.nothingRunsHint")}</p>
    </div>
  );
}
