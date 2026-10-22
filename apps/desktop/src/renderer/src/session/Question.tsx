import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Question, QuestionItem, Session } from "@hermesh/proto/session";
import { codingAgentTitle, dateOf, minutesLeft } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { local } from "../api";
import { Icon } from "../icons";
import { useNow, useStore } from "../store";

/**
 * The agent's question, answered as the owner: options by click or by number
 * key (the first item's), or in their own words; Return answers once every
 * item the agent requires has an answer. Don’t Answer tells the agent to decide by itself.
 */
export function QuestionPanel({ s, q }: { s: Session; q: Question }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const draftKey = `answer-draft:${s.sessionId}:${q.questionId}`;
  const [draft] = useState(() => {
    try { return JSON.parse(localStorage.getItem(draftKey) ?? "{}") as { chosen?: Record<string, string[]>; texts?: Record<string, string> }; }
    catch { return {}; }
  });
  const [chosen, setChosen] = useState<Record<string, string[]>>(draft.chosen ?? {});
  const [texts, setTexts] = useState<Record<string, string>>(draft.texts ?? {});
  useEffect(() => {
    const publicTexts = Object.fromEntries(q.items.filter((it) => !it.secret && texts[it.id]).map((it) => [it.id, texts[it.id]]));
    const publicChosen = Object.fromEntries(q.items.filter((it) => !it.secret && chosen[it.id]).map((it) => [it.id, chosen[it.id]]));
    try { localStorage.setItem(draftKey, JSON.stringify({ chosen: publicChosen, texts: publicTexts })); }
    catch { /* An unavailable profile does not prevent answering. */ }
  }, [chosen, texts, draftKey, q.items]);
  const [busy, setBusy] = useState(false);
  // The answer field at the bottom goes to the first item that takes free text.
  const free = q.items.find((it) => it.allowOther);
  const answered = (it: QuestionItem) => (chosen[it.id] ?? []).length > 0 || (texts[it.id] ?? "").trim() !== "";
  const complete = q.items.every((it) => it.optional || answered(it));

  const choose = (it: QuestionItem, label: string) =>
    setChosen((c) => {
      const now = c[it.id] ?? [];
      if (!it.multiSelect) return { ...c, [it.id]: [label] };
      return { ...c, [it.id]: now.includes(label) ? now.filter((l) => l !== label) : [...now, label] };
    });

  const send = async (decline: boolean) => {
    if (busy || (!decline && !complete)) return;
    setBusy(true);
    const answers = decline
      ? []
      : q.items.filter(answered).map((it) => ({
          itemId: it.id,
          selected: it.options.map((o) => o.label).filter((l) => (chosen[it.id] ?? []).includes(l)),
          text: (texts[it.id] ?? "").trim(),
        }));
    const ok = await st.run(
      () => local.answerQuestion({ sessionId: s.sessionId, questionId: q.questionId, answers, decline }),
      decline ? t("question.declined") : t("question.answered", { agent: codingAgentTitle(s.agent) }),
    );
    if (ok) {
      try { localStorage.removeItem(draftKey); } catch { /* The sent answer is already in the daemon. */ }
    }
    setBusy(false);
  };

  // Number keys pick the first item's options and Return answers, unless the person is typing or another layer is open.
  const keys = useRef({ choose, send, complete, blocked: false });
  keys.current = { choose, send, complete, blocked: st.sheet !== null || st.searching };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const k = keys.current;
      if (k.blocked || e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
      if (/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName ?? "")) return;
      const first = q.items[0];
      if (first && /^[1-9]$/.test(e.key) && Number(e.key) <= first.options.length) {
        e.preventDefault();
        k.choose(first, first.options[Number(e.key) - 1].label);
      } else if (e.key === "Enter" && k.complete) {
        e.preventDefault();
        k.send(false);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [q]);

  const field = (it: QuestionItem, id?: string) => (
    <input
      className="field"
      id={id}
      type={it.secret ? "password" : "text"}
      aria-label={it === free ? t("question.yourAnswer") : it.text}
      placeholder={it.secret ? t("question.secret") : it.options.length === 0 ? t("question.write") : t("question.orWrite")}
      autoComplete="off"
      value={texts[it.id] ?? ""}
      onChange={(e) => setTexts((x) => ({ ...x, [it.id]: e.target.value }))}
      onKeyDown={(e) => {
        if (e.key === "Enter" && !e.nativeEvent.isComposing) {
          e.preventDefault();
          send(false);
        }
      }}
    />
  );

  return (
    <div className="qx" role="group" aria-label={t("question.label")}>
      <div className="qx-h">
        <AgentLogo agent={s.agent} size={15} />
        <span>{t("question.asks", { agent: codingAgentTitle(s.agent) })}</span>
        {q.items[0]?.header && <span className="qx-tag">{q.items[0].header}</span>}
        <Countdown q={q} />
      </div>
      {q.items.map((it) => (
        <div className="qx-item" key={it.id}>
          <div className="qx-q sel-text">
            {it.text}
            {it.optional && <span className="faint" style={{ fontWeight: 400 }}>{t("question.optional")}</span>}
          </div>
          {it.options.length > 0 && (
            <div className="qx-opts" role={it.multiSelect ? "group" : "radiogroup"} aria-label={t("question.options")}>
              {it.options.map((o, i) => {
                const on = (chosen[it.id] ?? []).includes(o.label);
                return (
                  <button key={o.label} className={`qopt ${on ? "on" : ""}`} role={it.multiSelect ? "checkbox" : "radio"} aria-checked={on} onClick={() => choose(it, o.label)}>
                    <span className="qn">{i + 1}</span>
                    <span className="qb">
                      <b>{o.label}</b>
                      {o.description !== "" && <span>{o.description}</span>}
                    </span>
                    <span className="qc"><Icon name="check" /></span>
                  </button>
                );
              })}
            </div>
          )}
          {it.allowOther && it !== free && <div className="qx-own">{field(it)}</div>}
        </div>
      ))}
      <div className="qx-in">
        {free ? field(free, "qtext") : <span style={{ flex: 1 }} />}
        <button className="btn" title={t("question.declineTitle")} disabled={busy} onClick={() => send(true)}>{t("question.decline")}</button>
        <button className="btn primary" disabled={!complete || busy} onClick={() => send(false)}>
          {t("question.answer")}<span className="kret">↩</span>
        </button>
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
