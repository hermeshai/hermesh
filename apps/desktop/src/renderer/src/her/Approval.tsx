import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { approvalPreview, argsOf, callTarget, describe, pendingApproval, toolObject, type ApprovalPreview, type SessionView } from "@hermesh/her-client";
import type { ApprovalDecision, Op, SiteGrant } from "@hermesh/her-protocol";
import { Mark } from "@hermesh/ui";
import { Code } from "../session/Code";
import { useStore } from "../store";
import { meshProject, summaryParts } from "./approvalWords";
import { useHer } from "./context";
import { toolDo } from "./format";

/** How a page reaches its assistant's conversation: session/op, true when it went. */
export type SendOp = (op: Op) => Promise<boolean>;

/** A website's grants are a site's or every site's; an app's start with this (a Mac Her Server's desktop tools). */
const APP_ORIGIN = "app://";

/**
 * A tool call waiting for the owner, the assistant's own or a sub-agent's, which
 * says who asks: right above the composer, in its shape. What Her wants to do and why it asks, the
 * command or the change itself, and the answers as buttons. Deny opens a line for why, which goes
 * back to the model. Keys: ⌘↩ allows once from anywhere; outside a field ↩ allows, 2 allows for the
 * session, Esc (or 3) denies. Several waiting are answered in order. A website asks one question, the
 * site by name: once, always this site, or every site, kept in the environment.
 */
export function Approval({ view, op }: { view: SessionView; op: SendOp }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const { approval: a, agent } = pendingApproval(view)!;
  const live = (agent?.view ?? view).live!;
  const target = callTarget(a.name, a.args);
  const tool = live.tools.find((x) => x.call_id === a.call_id) ?? { ...describe(a.name, argsOf(a.args), view.info.cwd), name: a.name };
  const preview = approvalPreview(target.name, target.args, a.summary);
  // A summary of several lines (a session on the owner's computers, docs/her-mesh.md §5): its first is the question.
  const said = preview.kind === "args" ? summaryParts(a.summary) : null;
  const [denying, setDenying] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [busy, setBusy] = useState(false);
  const [remember, setRemember] = useState(false);
  const field = useRef<HTMLInputElement>(null);

  const give = async (decision: ApprovalDecision) => {
    if (busy) return;
    setBusy(true);
    await op(agent ? { type: "agent_approval", agent: agent.id, call_id: a.call_id, decision } : { type: "approval", call_id: a.call_id, decision });
    setBusy(false);
  };
  const deny = () => give({ type: "deny", feedback: feedback.trim() || undefined });

  const grants = a.proposed_grants ?? [];
  const script = grants.find((g) => g.authority === "script");
  const site = grants.length > 0 && !script && !grants.some((g) => g.origin.startsWith(APP_ORIGIN));
  const keys = useRef({ give, denying, blocked: false, grants: false, site: false });
  keys.current = { give, denying, blocked: st.sheet !== null || st.searching || her.sheet !== null, grants: grants.length > 0, site };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const k = keys.current;
      if (k.blocked || e.defaultPrevented) return;
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.altKey && !k.denying) {
        e.preventDefault();
        return void k.give({ type: "approve" });
      }
      if (k.denying || e.metaKey || e.ctrlKey || e.altKey) return;
      if (/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName ?? "")) return;
      if (e.key === "Enter" || e.key === "1") void k.give({ type: "approve" });
      else if (e.key === "2") void k.give({ type: "approve_for_session", grant: k.grants ? 0 : undefined, persist: k.site || undefined });
      else if (e.key === "Escape" || e.key === "3") setDenying(true);
      else return;
      e.preventDefault();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
  useEffect(() => {
    if (denying) field.current?.focus();
  }, [denying]);

  const question = script ? t("approval.scriptQuestion", { site: hostOf(script.origin) }) : said ? said.title : tool.category === "execute" ? t("approval.runCommand") : t("approval.useTool", { verb: toolDo(tool.category), object: toolObject(tool) });
  const rule = a.proposed_rule?.prefix.join(" ");
  const project = meshProject(a.proposed_rule);
  const denyField = (
    <>
      <input
        ref={field}
        className="field"
        placeholder={t("approval.feedback")}
        aria-label={t("approval.feedback")}
        value={feedback}
        onChange={(e) => setFeedback(e.target.value)}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing) return;
          if (e.key === "Enter") (e.preventDefault(), void deny());
          else if (e.key === "Escape") (e.preventDefault(), e.stopPropagation(), setDenying(false));
        }}
      />
      <button className="btn" onClick={() => setDenying(false)}>{t("approval.cancel")}</button>
      <button className="btn danger" disabled={busy} onClick={() => void deny()}>
        {t("approval.deny")}
        <span className="kret">↩</span>
      </button>
    </>
  );
  const queue = live.approvals.length > 1 && <span className="qx-tag">{t("approval.queue", { n: 1, total: live.approvals.length })}</span>;
  const who = agent && <span className="appr-who" title={agent.name}>{t("approval.agent", { name: agent.description || agent.name })}</span>;
  if (site) {
    const origin = grants[0].origin;
    const every = grants.findIndex((g) => g.origin === "*");
    const url = typeof target.args.url === "string" ? (target.args.url as string) : a.summary;
    return (
      <div className="appr" role="region" aria-label={t("approval.label")}>
        <div className="appr-h">
          <Mark size={16} />
          <b title={origin}>{t("approval.siteQuestion", { site: hostOf(origin) })}</b>
          {who}
          {queue}
        </div>
        <div className="appr-site">
          <code className="sel-text" title={url}>{url}</code>
        </div>
        <div className="appr-f">
          {denying ? (
            denyField
          ) : (
            <>
              <button className="btn" disabled={busy} onClick={() => setDenying(true)}>{t("approval.denyWhy")}</button>
              <span className="appr-sp" />
              {every >= 0 && (
                <button className="btn" disabled={busy} title={t("approval.siteEveryDesc")} onClick={() => void give({ type: "approve_for_session", grant: every, persist: true })}>
                  {t("approval.siteEvery")}
                </button>
              )}
              <button className="btn" disabled={busy} title={t("approval.siteAlwaysDesc")} onClick={() => void give({ type: "approve_for_session", grant: 0, persist: true })}>
                {t("approval.siteAlways")}
              </button>
              <button className="btn primary" disabled={busy} onClick={() => void give({ type: "approve" })}>
                {t("approval.siteOnce")}
                <span className="kret">⌘↩</span>
              </button>
            </>
          )}
        </div>
      </div>
    );
  }
  return (
    <div className="appr" role="region" aria-label={t("approval.label")}>
      <div className="appr-h">
        <Mark size={16} />
        <b title={said?.title}>{question}</b>
        {a.reason && <span className="why" title={a.reason}>{a.reason}</span>}
        {who}
        {queue}
      </div>
      <Preview p={said ? { kind: "args", text: said.body } : preview} />
      <div className="appr-f">
        {denying ? (
          denyField
        ) : (
          <>
            <button className="btn" disabled={busy} onClick={() => setDenying(true)}>{t("approval.denyWhy")}</button>
            <span className="appr-sp" />
            {grants.length > 0 && !script && (
              <label className="appr-keep" title={t("approval.rememberDesc")}>
                <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
                {t("approval.remember")}
              </label>
            )}
            {grants.length > 0
              ? grants.map((g, i) => (
                  <button key={i} className="btn" disabled={busy} title={script ? t("approval.grantScriptDesc", { site: hostOf(g.origin) }) : t("approval.grantAppDesc")} onClick={() => void give({ type: "approve_for_session", grant: i, persist: (remember && !script) || undefined })}>
                    {grantLabel(g, t)}
                  </button>
                ))
              : (rule || a.proposed_mode) && (
                  <button className="btn" disabled={busy} title={rule && !project ? t("approval.sessionRule", { rule }) : t("approval.sessionDesc")} onClick={() => void give({ type: "approve_for_session" })}>
                    {project ? t("approval.project", { project }) : t("approval.session")}
                  </button>
                )}
            <button className="btn primary" disabled={busy} onClick={() => void give({ type: "approve" })}>
              {t("approval.once")}
              <span className="kret">⌘↩</span>
            </button>
          </>
        )}
      </div>
    </div>
  );
}

type T = ReturnType<typeof useTranslation<"her">>["t"];

/** A grant as a button: a site's scripts, or an app on the machine by its bundle id. */
function grantLabel(g: SiteGrant, t: T): string {
  if (g.authority === "script") return t("approval.grantScript");
  return t("approval.grantApp", { app: g.origin.slice(APP_ORIGIN.length) });
}

/** A site as people read it: the host, with the scheme only when it is not https. */
function hostOf(origin: string): string {
  return origin.replace(/^https:\/\//, "");
}

/** The call itself: a command coloured as shell, a change as removed and added lines, or what it writes. */
function Preview({ p }: { p: ApprovalPreview }) {
  const { t } = useTranslation("her");
  switch (p.kind) {
    case "command":
      return (
        <pre className="appr-pre sel-text">
          <Code text={p.text} lang="bash" />
        </pre>
      );
    case "diff":
      return (
        <pre className="appr-pre diff sel-text">
          {p.lines.map((l, i) => (
            <span key={i} className={l.sign === "+" ? "add" : "del"}>
              {l.sign} {l.text}
            </span>
          ))}
          {p.more > 0 && <span className="more">{t("approval.more", { count: p.more })}</span>}
        </pre>
      );
    case "content":
      return (
        <pre className="appr-pre sel-text">
          {p.lines.join("\n")}
          {p.more > 0 && <span className="more">{t("approval.more", { count: p.more })}</span>}
        </pre>
      );
    case "args":
      return <pre className="appr-pre sel-text">{p.text}</pre>;
  }
}
