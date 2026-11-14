import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { Session } from "@hermesh/proto/session";
import {
  clockText, codingAgentTitle, createdDate, dateOf, displayTitle, elapsedSeconds, listTime, pendingQuestion, quickChoice, teamState, todayStats, updatedDate,
  type TeamState,
} from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { local } from "./api";
import { Icon } from "./icons";
import { senderLine } from "./Sidebar";
import { useNow, useStore, type Store } from "./store";
import "./team.css";

// The team (docs/her-team.md §4.2, §4.3, §14.3, §14.4): what Her's name tag says, and the lists the team panel and
// Work share: what needs the owner, answered in place where one click can; what runs; what just finished.

/** The team's state from the window's sessions and requests to connect. */
export function useTeam(): TeamState {
  const st = useStore();
  return teamState(st.sessions, st.pairings.length);
}

/** Beside Her's name: what the team is doing, the first that holds (§4.2). Nothing while it is quiet. */
export function TagWords({ team }: { team: TeamState }) {
  const { t } = useTranslation("common");
  const tag = team.tag;
  if (tag.kind === "quiet") return null;
  const waits = tag.kind === "waiting" || tag.kind === "needYou";
  return (
    <span className="tag-w">
      <span className="her-id-sep" aria-hidden="true">·</span>
      {waits && <i className="dot-w" aria-hidden="true" />}
      {tag.kind === "failed" && <i className="dot-f" aria-hidden="true" />}
      {tag.kind === "working" && <span className="tag-logos" aria-hidden="true">{team.runningAgents.slice(0, 3).map((a) => <AgentLogo key={a} agent={a} size={14} />)}</span>}
      <span className={waits ? "tag-wait" : ""}>
        {tag.kind === "waiting" ? t("team.waiting", { agent: codingAgentTitle(tag.agent) })
        : tag.kind === "needYou" ? t("team.needYou", { count: tag.count })
        : tag.kind === "failed" ? t("team.failed", { count: tag.count })
        : t("team.working", { count: tag.count })}
      </span>
      {/* While something waits, what runs follows the words as a ring and a number. */}
      {waits && team.running.length > 0 && (
        <span className="busy" aria-label={t("sidebar.running", { count: team.running.length })}>
          <i aria-hidden="true" />
          <span aria-hidden="true">{team.running.length}</span>
        </span>
      )}
    </span>
  );
}

/** The buttons that answer a question with one click, when it is one (§4.3); null otherwise. */
export function QuickAnswer({ s }: { s: Session }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const q = pendingQuestion(s);
  const item = q && quickChoice(q);
  if (!q || !item) return null;
  const answer = (label: string) =>
    st.run(
      () => local.answerQuestion({ sessionId: s.sessionId, questionId: q.questionId, answers: [{ itemId: item.id, selected: [label], text: "" }], decline: false }),
      t("team.answered", { answer: label }),
    );
  return (
    <span className="qa">
      {item.options.map((o) => (
        <button key={o.label} className="btn" onClick={() => answer(o.label)} title={o.description || undefined}>
          {o.label}
        </button>
      ))}
    </span>
  );
}

/** What needs the owner: each question (answered here when one click can), each request to connect, each failed session. */
export function NeedsYou({ team, onOpen }: { team: TeamState; onOpen?: () => void }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const open = (s: Session) => (onOpen?.(), st.select(s.sessionId));
  return (
    <>
      {team.waiting.map((s) => {
        const q = pendingQuestion(s);
        const words = q?.items.map((i) => i.text || i.header).filter(Boolean).join(" ") ?? "";
        return (
          <div key={s.sessionId} className="tm-need">
            <button className="tm-row" onClick={() => open(s)}>
              <i className="dot-w" aria-hidden="true" />
              <span className="tm-main">
                <b>{displayTitle(s)}</b>
                <span>{senderLine(st, s)}</span>
              </span>
            </button>
            {words && <div className="tm-q">{words}</div>}
            <QuickAnswer s={s} />
          </div>
        );
      })}
      {st.pairings.map((p) => (
        <button key={p.code} className="tm-row" onClick={() => (onOpen?.(), st.set({ sheet: { kind: "pairing", code: p.code } }))}>
          <i className="dot-w" aria-hidden="true" />
          <span className="tm-main">
            <b>{p.label || t("sidebar.pairing")}</b>
            <span>{t("team.wantsToConnect")}</span>
          </span>
        </button>
      ))}
      {team.failed.map((s) => (
        <button key={s.sessionId} className="tm-row" onClick={() => open(s)}>
          <i className="dot-f" aria-hidden="true" />
          <span className="tm-main">
            <b>{displayTitle(s)}</b>
            <span>{senderLine(st, s)} · {s.progress?.lastFailure?.message || t("team.lastTurnFailed")}</span>
          </span>
        </button>
      ))}
    </>
  );
}

/** A session at work: its agent, title, who sent it to whom, what it does now, and how long. */
export function RunningRow({ s, onOpen }: { s: Session; onOpen?: () => void }) {
  const st = useStore();
  const now = useNow(1000);
  const since = dateOf(s.progress?.turnStartedAt) ?? createdDate(s);
  const tool = s.progress?.currentTool ?? "";
  return (
    <button className="tm-row" onClick={() => (onOpen?.(), st.select(s.sessionId))}>
      <AgentLogo agent={s.agent} size={22} />
      <span className="tm-main">
        <b>{displayTitle(s)}</b>
        <span>{senderLine(st, s)}{tool && <> · <code>{tool}</code></>}</span>
      </span>
      <span className="tm-when">{clockText(elapsedSeconds(since, now))}</span>
    </button>
  );
}

/** A session done: what it changed, and when. */
export function FinishedRow({ s, onOpen }: { s: Session; onOpen?: () => void }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const now = useNow(30_000);
  const d = s.progress?.diffStat;
  return (
    <button className="tm-row" onClick={() => (onOpen?.(), st.select(s.sessionId))}>
      <Icon name="check" className="tm-ok" />
      <span className="tm-main">
        <b>{displayTitle(s)}</b>
        <span>
          {senderLine(st, s)} · {d && d.filesChanged > 0 ? t("team.changed", { count: d.filesChanged, add: d.insertions, del: d.deletions }) : t("team.noChanges")}
        </span>
      </span>
      <span className="tm-when">{listTime(updatedDate(s), now)}</span>
    </button>
  );
}

/** Today's sessions, for the panel's head (those the window holds). */
export function todayCount(st: Store, now: Date): number {
  return todayStats(st.sessions, now).sessions;
}

/**
 * The team panel (§4.3, §14.3): grown out of Her's name tag, over a dimmed conversation. Esc, a click outside or
 * opening a row closes it.
 */
export function TeamPanel({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const team = useTeam();
  const now = useNow(60_000);
  const box = useRef<HTMLDivElement>(null);
  const today = todayCount(st, now);
  useEffect(() => {
    box.current?.focus();
    const key = (e: KeyboardEvent) => e.key === "Escape" && (e.stopPropagation(), onClose());
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [onClose]);
  const needs = team.waiting.length + st.pairings.length + team.failed.length;
  return (
    <>
      <button className="tm-scrim" aria-label={t("team.close")} onClick={onClose} />
      <div className="tm-panel" role="dialog" aria-label={t("team.title")} tabIndex={-1} ref={box}>
        <div className="tm-head">
          <h2>{t("team.title")}</h2>
          <button className="link tm-today" onClick={() => (onClose(), st.set({ selection: { kind: "overview" } }))}>
            {t("team.today", { count: today })}
            <Icon name="chevR" className="sm" />
          </button>
        </div>
        {needs === 0 && team.running.length === 0 && <div className="tm-empty">{t("team.nothingRunning")}</div>}
        {needs > 0 && (
          <section aria-label={t("team.needsYou")}>
            <h3>{t("team.needsYou")}</h3>
            <NeedsYou team={team} onOpen={onClose} />
          </section>
        )}
        {team.running.length > 0 && (
          <section aria-label={t("team.inProgress")}>
            <h3>{t("team.inProgress")}</h3>
            {team.running.map((s) => <RunningRow key={s.sessionId} s={s} onOpen={onClose} />)}
          </section>
        )}
        {team.finished.length > 0 && (
          <section aria-label={t("team.justFinished")}>
            <h3>{t("team.justFinished")}</h3>
            {team.finished.slice(0, 2).map((s) => <FinishedRow key={s.sessionId} s={s} onOpen={onClose} />)}
          </section>
        )}
      </div>
    </>
  );
}
