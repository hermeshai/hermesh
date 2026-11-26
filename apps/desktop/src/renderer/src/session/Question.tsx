import { useEffect, useRef, useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { create } from "@bufbuild/protobuf";
import { ApprovalDecision, QuestionAnswerSchema, type Question, type Session } from "@hermesh/proto/session";
import { codingAgentTitle, dateOf, minutesLeft } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { sessions } from "../api";
import { Icon } from "../icons";
import { useNow, useStore } from "../store";
import {
  applyChoice,
  applyCustomText,
  initialQuestionStep,
  isItemAnswered,
  isQuestionComplete,
  itemAnswerPreview,
  nextAutoStep,
  nextManualStep,
  parseOptionLabel,
} from "./question-state";

/** What the owner answered, item by item. */
export type QuestionAnswers = { itemId: string; selected: string[]; text: string }[];

const AUTO_ADVANCE_MS = 180;

/**
 * The agent's question, presented one item at a time: single-choice picks
 * advance automatically to the next unanswered item; the top pager and
 * segmented track let the owner revisit and revise any item before answering.
 */
export function QuestionPanel({ s, q, answer }: { s: Session; q: Question; answer?: (answers: QuestionAnswers, decline: boolean) => Promise<unknown> }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const draftKey = `answer-draft:${s.sessionId}:${q.questionId}`;
  const [draft] = useState(() => {
    try { return JSON.parse(localStorage.getItem(draftKey) ?? "{}") as { chosen?: Record<string, string[]>; texts?: Record<string, string> }; }
    catch { return {}; }
  });
  const [chosen, setChosen] = useState<Record<string, string[]>>(draft.chosen ?? {});
  const [texts, setTexts] = useState<Record<string, string>>(draft.texts ?? {});
  const [step, setStep] = useState(() => initialQuestionStep(q.items, draft.chosen ?? {}, draft.texts ?? {}));
  const [dir, setDir] = useState<1 | -1>(1);
  const advanceRef = useRef<number | null>(null);

  const clearAdvance = () => {
    if (advanceRef.current !== null) {
      window.clearTimeout(advanceRef.current);
      advanceRef.current = null;
    }
  };

  useEffect(() => () => clearAdvance(), []);

  useEffect(() => {
    const publicTexts = Object.fromEntries(q.items.filter((it) => !it.secret && texts[it.id]).map((it) => [it.id, texts[it.id]]));
    const publicChosen = Object.fromEntries(q.items.filter((it) => !it.secret && chosen[it.id]).map((it) => [it.id, chosen[it.id]]));
    try { localStorage.setItem(draftKey, JSON.stringify({ chosen: publicChosen, texts: publicTexts })); }
    catch { /* An unavailable profile does not prevent answering. */ }
  }, [chosen, texts, draftKey, q.items]);

  const [busy, setBusy] = useState(false);
  const count = q.items.length;
  const idx = count > 0 ? Math.min(step, count - 1) : 0;
  const it = q.items[idx];
  const multi = count > 1;
  const complete = isQuestionComplete(q.items, chosen, texts);
  const curAnswered = it ? isItemAnswered(it, chosen, texts) : false;
  const nextStep = it ? nextManualStep(q.items, chosen, texts, idx) : -1;

  const goStep = (target: number) => {
    if (target < 0 || target >= count || target === idx) return;
    clearAdvance();
    setDir(target > idx ? 1 : -1);
    setStep(target);
  };

  const choose = (label: string) => {
    if (!it || busy) return;
    clearAdvance();
    const next = applyChoice(it, label, chosen, texts);
    setChosen(next.chosen);
    if (next.texts !== texts) setTexts(next.texts);
    if (!it.multiSelect && multi) {
      const target = nextAutoStep(q.items, next.chosen, next.texts, idx);
      if (target !== -1) {
        advanceRef.current = window.setTimeout(() => {
          advanceRef.current = null;
          setDir(target > idx ? 1 : -1);
          setStep(target);
        }, AUTO_ADVANCE_MS);
      }
    }
  };

  const updateText = (value: string) => {
    if (!it) return;
    clearAdvance();
    const next = applyCustomText(it, value, chosen, texts);
    setTexts(next.texts);
    if (next.chosen !== chosen) setChosen(next.chosen);
  };

  const send = async (decline: boolean) => {
    clearAdvance();
    if (busy || (!decline && !complete)) return;
    setBusy(true);
    const answers: QuestionAnswers = decline
      ? []
      : q.items.filter((x) => isItemAnswered(x, chosen, texts)).map((x) => ({
          itemId: x.id,
          selected: x.options.map((o) => o.label).filter((l) => (chosen[x.id] ?? []).includes(l)),
          text: (texts[x.id] ?? "").trim(),
        }));
    const ok = await st.run(
      () =>
        answer
          ? answer(answers, decline)
          : sessions.respondRuntimeRequest({
              sessionId: s.sessionId,
              requestId: q.questionId,
              decline,
              decision: decline ? ApprovalDecision.DECLINE : ApprovalDecision.ACCEPT_ONCE,
              answers: decline
                ? {}
                : Object.fromEntries(answers.map((a) => [a.itemId, create(QuestionAnswerSchema, { selectedOptions: a.selected, customText: a.text })])),
            }),
      decline ? t("question.declined") : t("question.answered", { agent: codingAgentTitle(s.agent) }),
    );
    if (ok) {
      try { localStorage.removeItem(draftKey); } catch { /* The sent answer is already in the daemon. */ }
    }
    setBusy(false);
  };

  const advanceOrSend = () => {
    if (complete) void send(false);
    else if (multi && it && (curAnswered || it.optional) && nextStep !== -1) goStep(nextStep);
  };

  // Number keys pick the active item's options, Left/Right switch items, and Return advances or answers.
  const keys = useRef({ choose, goStep, advanceOrSend, idx, count, it, blocked: false });
  keys.current = { choose, goStep, advanceOrSend, idx, count, it, blocked: st.sheet !== null || st.searching };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const k = keys.current;
      if (k.blocked || e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
      if (/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName ?? "")) return;
      if (k.it && /^[1-9]$/.test(e.key) && Number(e.key) <= k.it.options.length) {
        e.preventDefault();
        k.choose(k.it.options[Number(e.key) - 1]!.label);
      } else if (e.key === "ArrowLeft" && k.idx > 0) {
        e.preventDefault();
        k.goStep(k.idx - 1);
      } else if (e.key === "ArrowRight" && k.idx < k.count - 1) {
        e.preventDefault();
        k.goStep(k.idx + 1);
      } else if (e.key === "Enter") {
        e.preventDefault();
        k.advanceOrSend();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [q]);

  if (!it) return null;
  const txtVal = texts[it.id] ?? "";
  const showHeader = it.header !== "" && it.header !== it.text;
  const showInput = it.allowOther || it.options.length === 0;

  return (
    <div className="qx" role="group" aria-label={t("question.label")}>
      <div className="qx-h">
        <AgentLogo agent={s.agent} size={15} />
        <span>{t("question.asks", { agent: codingAgentTitle(s.agent) })}</span>
        {showHeader && <span className="qx-tag">{it.header}</span>}
        {it.multiSelect && <span className="qx-tag kind">{t("question.multiSelect")}</span>}
        <span className="qx-r">
          <Countdown q={q} />
          {multi && (
            <span className="qx-pager" role="group">
              <button
                className="qx-pg"
                disabled={idx === 0 || busy}
                aria-label={t("question.previous")}
                title={`${t("question.previous")} (←)`}
                onClick={() => goStep(idx - 1)}
              >
                <Icon name="chevL" />
              </button>
              <span className="qx-pgnum" data-testid="question-step">{idx + 1} / {count}</span>
              <button
                className="qx-pg"
                disabled={idx === count - 1 || busy}
                aria-label={t("question.next")}
                title={`${t("question.next")} (→)`}
                onClick={() => goStep(idx + 1)}
              >
                <Icon name="chevR" />
              </button>
            </span>
          )}
        </span>
      </div>

      {multi && (
        <div className="qx-segs" style={{ "--qx-n": count } as CSSProperties}>
          {q.items.map((stepIt, sIdx) => {
            const done = isItemAnswered(stepIt, chosen, texts);
            const cur = sIdx === idx;
            const summary = done
              ? `${sIdx + 1}/${count} · ${t("question.answeredSummary", { answer: itemAnswerPreview(stepIt, chosen, texts) })}`
              : `${sIdx + 1}/${count} · ${stepIt.text}`;
            return (
              <button
                key={stepIt.id}
                className={`qx-seg ${cur ? "cur" : ""} ${done ? "done" : ""}`}
                title={summary}
                aria-label={summary}
                disabled={busy}
                onClick={() => goStep(sIdx)}
              >
                <i />
              </button>
            );
          })}
        </div>
      )}

      <div className="qx-body" key={it.id} style={{ "--qx-dir": `${dir * 10}px` } as CSSProperties}>
        <div className="qx-q sel-text">
          {it.text}
          {it.optional && <span className="faint" style={{ fontWeight: 400 }}>{t("question.optional")}</span>}
        </div>
        {it.options.length > 0 && (
          <div className="qx-opts" role={it.multiSelect ? "group" : "radiogroup"} aria-label={t("question.options")}>
            {it.options.map((o, i) => {
              const on = (chosen[it.id] ?? []).includes(o.label);
              const parsed = parseOptionLabel(o.label);
              return (
                <button
                  key={o.label}
                  className={`qopt ${on ? "on" : ""}`}
                  role={it.multiSelect ? "checkbox" : "radio"}
                  aria-checked={on}
                  disabled={busy}
                  onClick={() => choose(o.label)}
                >
                  <span className="qn">{i + 1}</span>
                  <span className="qb">
                    <b>
                      {parsed.recommended && <span className="qx-rec">{t("question.recommended")}</span>}
                      {parsed.text}
                    </b>
                    {o.description !== "" && <span>{o.description}</span>}
                  </span>
                  <span className="qc"><Icon name="check" /></span>
                </button>
              );
            })}
          </div>
        )}
        {showInput && (
          <div className={`qx-own ${txtVal.trim() !== "" ? "filled" : ""}`}>
            <Icon name={it.secret ? "lock" : "edit"} className="qx-own-ic" />
            <input
              id="qtext"
              className="qx-own-in"
              type={it.secret ? "password" : "text"}
              aria-label={t("question.yourAnswer")}
              placeholder={it.secret ? t("question.secret") : it.options.length === 0 ? t("question.write") : t("question.orWrite")}
              autoComplete="off"
              disabled={busy}
              value={txtVal}
              onChange={(e) => updateText(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing) return;
                if (e.key === "Enter") {
                  e.preventDefault();
                  if (!complete && multi && isItemAnswered(it, chosen, texts) && nextStep !== -1) {
                    goStep(nextStep);
                  } else if (complete) {
                    void send(false);
                  }
                } else if (e.key === "Escape") {
                  (e.currentTarget as HTMLInputElement).blur();
                }
              }}
            />
            {txtVal !== "" && !busy && (
              <button className="qx-own-clr" aria-label={t("question.clear")} title={t("question.clear")} onClick={() => updateText("")}>
                <Icon name="x" />
              </button>
            )}
          </div>
        )}
      </div>

      <div className="qx-in">
        <span style={{ flex: 1 }} />
        <button className="btn" title={t("question.declineTitle")} disabled={busy} onClick={() => void send(true)}>{t("question.decline")}</button>
        {complete ? (
          <button className="btn primary" disabled={busy} onClick={() => void send(false)}>
            {t("question.answer")}<span className="kret">↩</span>
          </button>
        ) : multi && curAnswered && nextStep !== -1 ? (
          <button className="btn primary" disabled={busy} onClick={() => goStep(nextStep)}>
            {t("question.continue")}<span className="kret">↩</span>
          </button>
        ) : multi && it.optional && nextStep !== -1 ? (
          <button className="btn" disabled={busy} onClick={() => goStep(nextStep)}>
            {t("question.skipItem")}<span className="kret">→</span>
          </button>
        ) : (
          <button className="btn primary" disabled>
            {t("question.answer")}<span className="kret">↩</span>
          </button>
        )}
      </div>
    </div>
  );
}

const ring = 2 * Math.PI * 6;

/** Time left before the agent decides by itself, as a ring and in minutes. */
function Countdown({ q }: { q: Question }) {
  const { t } = useTranslation("session");
  const now = useNow(15_000);
  const until = dateOf(q.expiresAt);
  if (!until) return null;
  const asked = dateOf(q.askedAt);
  const total = asked ? until.getTime() - asked.getTime() : 30 * 60_000;
  const left = Math.min(1, Math.max(0, (until.getTime() - now.getTime()) / total));
  return (
    <span className="qx-t" title={t("question.countdown")}>
      <svg className="ring" viewBox="0 0 16 16" aria-hidden="true">
        <circle className="trk" cx="8" cy="8" r="6" />
        <circle className="arc" cx="8" cy="8" r="6" style={{ strokeDasharray: ring, strokeDashoffset: ring * (1 - left) }} />
      </svg>
      <span>{minutesLeft(until, now)}</span>
    </span>
  );
}
