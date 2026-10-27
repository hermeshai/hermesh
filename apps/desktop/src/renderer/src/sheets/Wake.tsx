import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Wake, WakeCheck } from "@hermesh/proto/local";
import { agoText, dateOf } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { Icon } from "../icons";
import { useNow, useStore, type Sheet } from "../store";
import { ErrorLine, useAction } from "./common";

// An agent's doorbell (docs/agent-names.md §7): the webhook Hermesh calls
// when messages wait for the agent or one of its bots. Setting one up is
// four steps, one per screen: the bot's name, the routine's instruction to
// copy into Grok, the webhook's URL and key, and a check. The key goes from
// here to Hermesh's vault; neither the bot nor its model ever sees it.

type Step = "name" | "routine" | "webhook" | "check" | "manage";

const order: Step[] = ["name", "routine", "webhook", "check"];

/** What the bot's routine is told: the same for every bot but its name. */
export function routineInstruction(name: string): string {
  return name ? i18n.t("sheets:wake.instruction", { name }) : i18n.t("sheets:wake.instructionSelf");
}

/** Why a webhook didn't take a call, from its answer: the reason Hermesh gave, in the app's words. */
export function wakeReason(status: number, reason: string): string {
  if (status === 400) return i18n.t("sheets:wake.reason400");
  if (status === 401 || status === 403) return i18n.t("sheets:wake.reasonKey", { status });
  if (status === 404 || status === 410) return i18n.t("sheets:wake.reasonURL", { status });
  if (status === 429) return i18n.t("sheets:wake.reasonBusy");
  if (status >= 500) return i18n.t("sheets:wake.reasonFailed", { status });
  if (status === 0 && reason) return i18n.t("sheets:wake.reasonUnreachable");
  return reason;
}

/** The Authorization header a key goes in: Grok's routines take a bearer key. */
function bearer(key: string): string {
  const k = key.trim();
  return !k || /^bearer\s/i.test(k) ? k : `Bearer ${k}`;
}

export function WakeSheet({ sheet }: { sheet: Extract<Sheet, { kind: "wake" }> }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const now = useNow(30_000);
  const [step, setStep] = useState<Step>(sheet.wake ? "manage" : "name");
  const [wake, setWake] = useState(sheet.wake);
  const [name, setName] = useState(sheet.wake?.name ?? "");
  const [url, setURL] = useState("");
  const [key, setKey] = useState("");
  const [copied, setCopied] = useState(false);
  const [check, setCheck] = useState<WakeCheck>();
  const [saved, setSaved] = useState(false);
  const [secret, setSecret] = useState("");
  const act = useAction();
  const close = () => st.set({ sheet: null });
  const who = name.trim() || sheet.agent;
  const replacing = !!sheet.wake;

  const save = async (keep: boolean) => {
    setStep("check");
    setCheck(undefined);
    await act.go(async () => {
      const res = await local.setWake({ clientId: sheet.clientId, name: name.trim(), url: url.trim(), authorization: bearer(key), keep });
      setCheck(res.check);
      setSaved(!!res.wake);
      setSecret(res.signingSecret);
    });
  };
  const checkNow = () =>
    act.go(async () => {
      const res = await local.checkWake({ clientId: sheet.clientId, name: name });
      setWake(res.wake);
      setCheck(res.check);
    });
  const remove = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("wake.removeTitle", { name: who }),
        message: t("wake.removeMessage"),
        button: t("wake.removeButton"),
        done: t("wake.removed", { name: who }),
        run: () => local.clearWake({ clientId: sheet.clientId, name }),
      },
    });
  const copy = () => {
    navigator.clipboard.writeText(routineInstruction(name.trim())).catch(() => {});
    setCopied(true);
  };

  const n = order.indexOf(step);
  const kicker = n >= 0 && (
    <>
      <div className="wk-dots" aria-hidden="true">
        {order.map((s, i) => <i key={s} className={i <= n ? "on" : ""} />)}
      </div>
      <div className="wk-kicker">{t("wake.step", { n: n + 1 })}</div>
    </>
  );

  if (step === "manage" && wake) {
    const rang = dateOf(wake.lastRingAt);
    return (
      <>
        <h3 id="sheet-title">{t("wake.manageTitle", { name: who })}</h3>
        <div className="wk-kv" role="list">
          <div role="listitem"><span>{t("wake.state")}</span><b className={wake.state === "on" ? "wk-state" : "wk-state bad"}><i />{wake.state === "on" ? t("wake.on") : t("wake.broken")}</b></div>
          <div role="listitem"><span>{t("wake.webhook")}</span><b className="mono sel-text">{wake.url}</b></div>
          <div role="listitem"><span>{t("wake.lastRang")}</span><b>{rang ? t("wake.answered", { ago: agoText(rang, now), status: wake.lastStatus }) : t("wake.never")}</b></div>
          <div role="listitem"><span>{t("wake.waiting")}</span><b>{wake.waiting}</b></div>
        </div>
        {wake.state !== "on" && <p className="sheet-err">{wakeReason(wake.lastStatus, wake.reason)}</p>}
        {check?.ok && <p className="sheet-note muted">{t("wake.checkedOK")}</p>}
        <ErrorLine text={act.error} />
        <div className="sheet-foot">
          <button className="btn left" onClick={remove}>{t("wake.remove")}</button>
          <button className="btn" disabled={act.busy} onClick={checkNow}>{t("wake.checkNow")}</button>
          <button className="btn" onClick={() => setStep("webhook")}>{t("wake.replace")}</button>
          <button className="btn primary" onClick={close}>{t("done")}</button>
        </div>
      </>
    );
  }

  return (
    <>
      {kicker}
      {step === "name" && (
        <>
          <h3 id="sheet-title">{t("wake.nameTitle")}</h3>
          <p className="sub">{t("wake.nameSub", { agent: sheet.agent })}</p>
          <input className="field" aria-label={t("wake.nameLabel")} placeholder={t("wake.namePlaceholder")} value={name} maxLength={60} autoFocus
            onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && setStep("routine")} />
          <div className="sheet-foot">
            <button className="btn" onClick={close}>{t("cancel")}</button>
            <button className="btn primary" onClick={() => setStep("routine")}>{t("wake.next")}</button>
          </div>
        </>
      )}
      {step === "routine" && (
        <>
          <h3 id="sheet-title">{t("wake.routineTitle", { name: who })}</h3>
          <p className="sub">{t("wake.routineSub")}</p>
          <button className={`btn wk-big ${copied ? "" : "primary"}`} onClick={copy}>
            <Icon name={copied ? "check" : "copy"} />
            {copied ? t("wake.copyAgain") : t("wake.copyInstruction")}
          </button>
          <details className="wk-how">
            <summary>{t("wake.how")}</summary>
            <p>{t("wake.howSteps")}</p>
            <div className="msgbox">{routineInstruction(name.trim())}</div>
          </details>
          <div className="sheet-foot">
            <button className="btn left" onClick={() => setStep("name")}>{t("wake.back")}</button>
            <button className={`btn ${copied ? "primary" : ""}`} onClick={() => setStep("webhook")}>{t("wake.made")}</button>
          </div>
        </>
      )}
      {step === "webhook" && (
        <>
          <h3 id="sheet-title">{t("wake.webhookTitle")}</h3>
          <p className="sub">{t("wake.webhookSub")}</p>
          <label className="wk-lab" htmlFor="wk-url">{t("wake.url")}</label>
          <input id="wk-url" className="field mono" placeholder="https://…" value={url} autoFocus={replacing} onChange={(e) => setURL(e.target.value)} />
          <label className="wk-lab" htmlFor="wk-key">{t("wake.key")}</label>
          <input id="wk-key" className="field" type="password" placeholder={t("wake.keyPlaceholder")} value={key} onChange={(e) => setKey(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && url.trim() && save(false)} />
          <div className="sheet-foot">
            <button className="btn left" onClick={() => setStep(replacing ? "manage" : "routine")}>{t("wake.back")}</button>
            <button className="btn primary" disabled={!url.trim()} onClick={() => save(false)}>{t("wake.check")}</button>
          </div>
        </>
      )}
      {step === "check" && (
        <>
          {act.busy ? (
            <div className="big-ok" role="status">
              <span className="wk-spin" />
              <b style={{ fontSize: 15 }} id="sheet-title">{t("wake.checking")}</b>
              <span className="muted" style={{ fontSize: 12.5 }}>{t("wake.checkingSub")}</span>
            </div>
          ) : check?.ok ? (
            <div className="big-ok" role="status">
              <span className="okc"><Icon name="check" /></span>
              <b style={{ fontSize: 15 }} id="sheet-title">{t("wake.ok", { name: who })}</b>
              <span className="muted" style={{ fontSize: 12.5 }}>{t("wake.okSub")}</span>
            </div>
          ) : (
            <div className="big-ok" role="status">
              <span className="okc bad">!</span>
              <b style={{ fontSize: 15 }} id="sheet-title">{saved ? t("wake.savedBroken") : t("wake.failed")}</b>
              <span className="muted" style={{ fontSize: 12.5 }}>{check ? wakeReason(check.status, check.reason) : act.error}</span>
            </div>
          )}
          {saved && secret && (
            <details className="wk-how">
              <summary>{t("wake.secret")}</summary>
              <div className="msgbox">{secret}</div>
            </details>
          )}
          {!act.busy && (
            <div className="sheet-foot">
              {saved ? (
                <button className="btn primary" onClick={close}>{t("done")}</button>
              ) : (
                <>
                  {check && <button className="btn left" onClick={() => save(true)}>{t("wake.saveAnyway")}</button>}
                  <button className="btn primary" onClick={() => setStep("webhook")}>{t("wake.goBack")}</button>
                </>
              )}
            </div>
          )}
        </>
      )}
    </>
  );
}
