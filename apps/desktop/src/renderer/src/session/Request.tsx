import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { create } from "@bufbuild/protobuf";
import { ApprovalDecision, QuestionAnswerSchema, RuntimeRequestKind, type RuntimeRequest, type Session } from "@hermesh/proto/session";
import { codingAgentTitle, sessionQuestion } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { sessions as sessionApi } from "../api";
import { Preview } from "../her/Approval";
import { QuestionPanel } from "./Question";
import { errorText, useStore } from "../store";

type Answer = { decision?: ApprovalDecision; feedback?: string; approvePlan?: boolean; secretValue?: string };

/** The pending runtime request of session `s`: tool approval, plan review, secret input, or structured question. */
export function SessionRequest({ s, req, onResponded }: { s: Session; req: RuntimeRequest; onResponded?: () => void }) {
  const st = useStore();
  const respond = async (a: Answer) => {
    try {
      await sessionApi.respondRuntimeRequest({ sessionId: s.sessionId, requestId: req.requestId, ...a });
      onResponded?.();
      return true;
    } catch (e) {
      st.show(errorText(e));
      return false;
    }
  };
  switch (req.kind) {
    case RuntimeRequestKind.TOOL_APPROVAL:
      return <ToolApproval s={s} req={req} respond={respond} />;
    case RuntimeRequestKind.PLAN_REVIEW:
      return <PlanReview s={s} req={req} respond={respond} />;
    case RuntimeRequestKind.SECRET_INPUT:
      return <SecretInput s={s} req={req} respond={respond} />;
    case RuntimeRequestKind.USER_INPUT:
      return (
        <div className="appr th-ask" data-testid="pending-question-banner">
          <QuestionPanel
            s={s}
            q={sessionQuestion(req)}
            answer={async (answers, decline) => {
              const res = await sessionApi.respondRuntimeRequest({
                sessionId: s.sessionId,
                requestId: req.requestId,
                decline,
                decision: decline ? ApprovalDecision.DECLINE : ApprovalDecision.ACCEPT_ONCE,
                answers: decline
                  ? {}
                  : Object.fromEntries(answers.map((a) => [a.itemId, create(QuestionAnswerSchema, { selectedOptions: a.selected, customText: a.text })])),
              });
              onResponded?.();
              return res;
            }}
          />
        </div>
      );
    default:
      return null;
  }
}

type Respond = (a: Answer) => Promise<boolean>;

function Card({ s, title, hover, why, tag, testId, preview, deny, denyLabel, children }: {
  s: Session;
  title: string;
  hover?: string;
  why?: string;
  tag?: string;
  testId: string;
  preview?: ReactNode;
  deny: (feedback: string) => Promise<unknown>;
  denyLabel: string;
  children: (busy: boolean, run: (f: () => Promise<unknown>) => void) => ReactNode;
}) {
  const { t } = useTranslation("thread");
  const [denying, setDenying] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [busy, setBusy] = useState(false);
  const field = useRef<HTMLInputElement>(null);
  const run = (f: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    void f().finally(() => setBusy(false));
  };
  useEffect(() => {
    if (denying) field.current?.focus();
  }, [denying]);
  return (
    <div className="appr" role="region" aria-label={t("request.label")} data-testid={testId}>
      <div className="appr-h">
        <AgentLogo agent={s.agent} size={16} />
        <b title={hover || title}>{title}</b>
        {why && <span className="why" title={why}>{why}</span>}
        {tag && <span className="qx-tag">{tag}</span>}
      </div>
      {preview}
      <div className="appr-f">
        {denying ? (
          <>
            <input
              ref={field}
              className="field"
              placeholder={t("request.feedback", { agent: codingAgentTitle(s.agent) })}
              aria-label={t("request.feedback", { agent: codingAgentTitle(s.agent) })}
              value={feedback}
              data-testid="decline-feedback-input"
              onChange={(e) => setFeedback(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing) return;
                if (e.key === "Enter") (e.preventDefault(), run(() => deny(feedback.trim())));
                else if (e.key === "Escape") (e.preventDefault(), e.stopPropagation(), setDenying(false));
              }}
            />
            <button className="btn" onClick={() => setDenying(false)}>{t("request.cancel")}</button>
            <button className="btn danger" disabled={busy} data-testid="decline-btn" onClick={() => run(() => deny(feedback.trim()))}>
              {denyLabel}
              <span className="kret">↩</span>
            </button>
          </>
        ) : (
          <>
            <button className="btn" disabled={busy} data-testid="decline-open-btn" onClick={() => setDenying(true)}>{denyLabel}…</button>
            <span className="appr-sp" />
            {children(busy, run)}
          </>
        )}
      </div>
    </div>
  );
}

function useAllowKey(allow: () => void) {
  const st = useStore();
  const keys = useRef({ allow, blocked: false });
  keys.current = { allow, blocked: st.sheet !== null || st.searching };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (keys.current.blocked || e.defaultPrevented) return;
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.altKey) {
        e.preventDefault();
        keys.current.allow();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
}

function ToolApproval({ s, req, respond }: { s: Session; req: RuntimeRequest; respond: Respond }) {
  const { t } = useTranslation("thread");
  const a = req.approval;
  const command = a?.command ?? "";
  const target = (a?.targetPath ?? "").split(/[\\/]/).pop() ?? "";
  const title = command !== "" ? t("request.runCommand") : t("request.useTool", { tool: a?.toolName || t("request.tool"), target });
  const allow = () => respond({ decision: ApprovalDecision.ACCEPT_ONCE });
  useAllowKey(() => void allow());
  const body = command !== "" ? command : a?.diffPreview ?? "";
  return (
    <Card
      s={s}
      title={title.trim()}
      hover={a?.targetPath}
      why={a?.reason}
      tag={a?.outsideWorkspace ? t("request.outsideWorkspace") : undefined}
      testId="pending-approval-banner"
      preview={body !== "" && <Preview p={command !== "" ? { kind: "command", text: command } : { kind: "args", text: body }} />}
      deny={(feedback) => respond({ decision: ApprovalDecision.DECLINE, feedback })}
      denyLabel={t("request.deny")}
    >
      {(busy, run) => (
        <>
          <button className="btn" disabled={busy} data-testid="approve-thread-btn" onClick={() => run(() => respond({ decision: ApprovalDecision.ACCEPT_FOR_SESSION }))}>
            {t("request.approveThread")}
          </button>
          <button className="btn primary" disabled={busy} data-testid="approve-once-btn" onClick={() => run(allow)}>
            {t("request.approveOnce")}
            <span className="kret">⌘↩</span>
          </button>
        </>
      )}
    </Card>
  );
}

function PlanReview({ s, req, respond }: { s: Session; req: RuntimeRequest; respond: Respond }) {
  const { t } = useTranslation("thread");
  const approve = () => respond({ approvePlan: true });
  useAllowKey(() => void approve());
  return (
    <Card
      s={s}
      title={t("request.planTitle")}
      why={req.planReview?.title}
      testId="pending-plan-banner"
      deny={(feedback) => respond({ approvePlan: false, feedback })}
      denyLabel={t("request.revisePlan")}
    >
      {(busy, run) => (
        <button className="btn primary" disabled={busy} data-testid="approve-plan-btn" onClick={() => run(approve)}>
          {t("request.approvePlan")}
          <span className="kret">⌘↩</span>
        </button>
      )}
    </Card>
  );
}

function SecretInput({ s, req, respond }: { s: Session; req: RuntimeRequest; respond: Respond }) {
  const { t } = useTranslation("thread");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const p = req.secretInput;
  const send = async () => {
    if (busy || value === "") return;
    setBusy(true);
    if (await respond({ secretValue: value })) setValue("");
    setBusy(false);
  };
  return (
    <div className="appr" role="region" aria-label={t("request.label")} data-testid="pending-secret-banner">
      <div className="appr-h">
        <AgentLogo agent={s.agent} size={16} />
        <b>{t("request.secretTitle", { agent: codingAgentTitle(s.agent), label: p?.label ?? "" })}</b>
        {p?.reason && <span className="why" title={p.reason}>{p.reason}</span>}
      </div>
      <p className="knote-b">{t("request.secretNote")}</p>
      <div className="appr-f">
        <input
          className="field"
          type="password"
          autoComplete="off"
          value={value}
          placeholder={p?.placeholder || t("request.secretPlaceholder")}
          aria-label={p?.label}
          data-testid="secret-value-input"
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && !e.nativeEvent.isComposing && (e.preventDefault(), void send())}
        />
        <button className="btn primary" disabled={busy || value === ""} data-testid="submit-secret-btn" onClick={() => void send()}>
          {t("request.sendSecret")}
          <span className="kret">↩</span>
        </button>
      </div>
    </div>
  );
}
