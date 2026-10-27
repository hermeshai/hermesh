import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { WakeCheck, WakeRing } from "@hermesh/proto/local";
import { agoText, dateOf } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { Icon } from "../icons";
import { useNow, useStore, type Sheet } from "../store";
import { ErrorLine, useAction } from "./common";

// An agent's doorbell (docs/agent-names.md §7): the webhook Hermesh calls
// when messages wait for the agent, or one of its bots. Setting one up is
// three steps, one per screen: the message that has the bot make its routine
// (Grok Bot's routines are made by asking the bot), the routine's URL and key,
// and a first ring. A doorbell for one bot of several asks for its name
// first. The key goes from here to Hermesh's vault; neither the bot nor its
// model ever sees it.

type Step = "name" | "routine" | "webhook" | "check" | "manage";

/** What the bot is sent to make its routine: the same for every bot, as the ring says whose messages wait. */
export function routineMessage(): string {
  return i18n.t("sheets:wake.message");
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

/**
 * A routine's URL and key from what was pasted: one of them, or both at once
 * (Grok's Webhook section copied whole), its labels left out.
 */
export function splitWebhook(text: string): { url?: string; key?: string } {
  const url = /https?:\/\/[^\s"'<>]+/.exec(text)?.[0];
  if (!url) return { key: text };
  const rest = text
    .replace(url, " ")
    .replace(/\b(POST to|key|header|authorization:?|bearer)\b/gi, " ")
    .split(/\s+/)
    .filter((w) => w && !/^[:：-]+$/.test(w));
  return { url, key: rest.length ? rest[rest.length - 1] : undefined };
}

/** Grok's routines don't check signatures: their owners never need the signing secret. */
function isGrok(url: string): boolean {
  try {
    return new URL(url).hostname.endsWith("cursor.sh");
  } catch {
    return false;
  }
}

/** What a past ring was, and what came of it. */
function ringText(r: WakeRing, doorbellName: string): string {
  const why = r.check ? i18n.t("sheets:wake.ringTry") : r.as && r.as !== doorbellName ? i18n.t("sheets:wake.ringNewFor", { name: r.as }) : i18n.t("sheets:wake.ringNew");
  const what = r.status >= 200 && r.status < 300 ? i18n.t("sheets:wake.ringOK") : wakeReason(r.status, "unreachable");
  return `${why} · ${what}`;
}

export function WakeSheet({ sheet }: { sheet: Extract<Sheet, { kind: "wake" }> }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const now = useNow(30_000);
  const order: Step[] = sheet.named ? ["name", "routine", "webhook", "check"] : ["routine", "webhook", "check"];
  const [step, setStep] = useState<Step>(sheet.wake ? "manage" : order[0]);
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
  // As it is now: it may have rung since the list was read.
  useEffect(() => {
    if (!sheet.wake) return;
    const doorbell = sheet.wake.name;
    local.listWakes({ clientId: sheet.clientId }).then((r) => {
      const w = r.wakes.find((x) => x.name === doorbell);
      if (w) setWake(w);
    }, () => {});
  }, [sheet.clientId, sheet.wake]);

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
    navigator.clipboard.writeText(routineMessage()).catch(() => {});
    setCopied(true);
  };
  // Either box takes both at once.
  const paste = (text: string, into: "url" | "key") => {
    const p = splitWebhook(text);
    if (p.url && (into === "key" || p.key)) {
      setURL(p.url);
      setKey(p.key ?? "");
    } else if (into === "url") setURL(text);
    else setKey(text);
  };

  const n = order.indexOf(step);
  const kicker = n >= 0 && (
    <>
      <div className="wk-dots" aria-hidden="true">
        {order.map((s, i) => <i key={s} className={i <= n ? "on" : ""} />)}
      </div>
      <div className="wk-kicker">{t("wake.step", { n: n + 1, total: order.length })}</div>
    </>
  );

  if (step === "manage" && wake) {
    return (
      <>
        <h3 id="sheet-title">{t("wake.manageTitle", { name: who })}</h3>
        <p className="sub">{t("wake.manageSub")}</p>
        <div className="wk-kv" role="list">
          <div role="listitem"><span>{t("wake.state")}</span><b className={wake.state === "on" ? "wk-state" : "wk-state bad"}><i />{wake.state === "on" ? t("wake.on") : t("wake.broken")}</b></div>
          <div role="listitem"><span>{t("wake.webhook")}</span><b className="mono sel-text">{wake.url}</b></div>
          <div role="listitem"><span>{t("wake.waiting")}</span><b>{wake.waiting}</b></div>
        </div>
        <div className="wk-lab">{t("wake.recent")}</div>
        <div className="wk-kv" role="list" aria-label={t("wake.recent")}>
          {wake.recent.length ? (
            wake.recent.map((r, i) => (
              <div role="listitem" key={i}><span>{agoText(dateOf(r.at)!, now)}</span><b>{ringText(r, wake.name)}</b></div>
            ))
          ) : (
            <div role="listitem"><span>{t("wake.never")}</span></div>
          )}
        </div>
        {wake.state !== "on" && <p className="sheet-err">{wakeReason(wake.lastStatus, wake.reason)}</p>}
        {check?.ok && <p className="sheet-note muted">{t("wake.checkedOK")}</p>}
        <ErrorLine text={act.error} />
        <div className="sheet-foot">
          <button className="btn left" onClick={remove}>{t("wake.remove")}</button>
          <button className="btn" disabled={act.busy} onClick={checkNow}>{t("wake.tryNow")}</button>
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
          <p className="sub">{t("wake.nameSub")}</p>
          <input className="field" aria-label={t("wake.nameLabel")} placeholder={t("wake.namePlaceholder")} value={name} maxLength={60} autoFocus
            onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && name.trim() && setStep("routine")} />
          <div className="sheet-foot">
            <button className="btn" onClick={close}>{t("cancel")}</button>
            <button className="btn primary" disabled={!name.trim()} onClick={() => setStep("routine")}>{t("wake.next")}</button>
          </div>
        </>
      )}
      {step === "routine" && (
        <>
          <h3 id="sheet-title">{name.trim() ? t("wake.routineTitleNamed", { name: name.trim() }) : t("wake.routineTitle")}</h3>
          <p className="sub">{t("wake.routineSub")}</p>
          <button className={`btn wk-big ${copied ? "" : "primary"}`} onClick={copy}>
            <Icon name={copied ? "check" : "copy"} />
            {copied ? t("wake.copyAgain") : t("wake.copyMessage")}
          </button>
          <div className="msgbox wk-msg">{routineMessage()}</div>
          <details className="wk-how">
            <summary>{t("wake.how")}</summary>
            <p>{t("wake.howSteps")}</p>
          </details>
          <div className="sheet-foot">
            {sheet.named ? <button className="btn left" onClick={() => setStep("name")}>{t("wake.back")}</button> : <button className="btn left" onClick={close}>{t("cancel")}</button>}
            <button className={`btn ${copied ? "primary" : ""}`} onClick={() => setStep("webhook")}>{t("wake.made")}</button>
          </div>
        </>
      )}
      {step === "webhook" && (
        <>
          <h3 id="sheet-title">{t("wake.webhookTitle")}</h3>
          <p className="sub">{t("wake.webhookSub")}</p>
          <label className="wk-lab" htmlFor="wk-url">{t("wake.url")}</label>
          <input id="wk-url" className="field mono" placeholder="https://…" value={url} autoFocus onChange={(e) => paste(e.target.value, "url")} />
          <label className="wk-lab" htmlFor="wk-key">{t("wake.key")}</label>
          <input id="wk-key" className="field" type="password" placeholder={t("wake.keyPlaceholder")} value={key} onChange={(e) => paste(e.target.value, "key")}
            onKeyDown={(e) => e.key === "Enter" && url.trim() && save(false)} />
          <p className="sheet-note muted">{t("wake.pasteNote")}</p>
          <div className="sheet-foot">
            <button className="btn left" onClick={() => setStep(replacing ? "manage" : "routine")}>{t("wake.back")}</button>
            <button className="btn primary" disabled={!url.trim()} onClick={() => save(false)}>{t("wake.tryNow")}</button>
          </div>
        </>
      )}
      {step === "check" && (
        <>
          {act.busy ? (
            <div className="big-ok" role="status">
              <span className="wk-spin" />
              <b style={{ fontSize: 15 }} id="sheet-title">{t("wake.trying")}</b>
              <span className="muted" style={{ fontSize: 12.5 }}>{t("wake.tryingSub")}</span>
            </div>
          ) : check?.ok ? (
            <div className="big-ok" role="status">
              <span className="okc"><Icon name="check" /></span>
              <b style={{ fontSize: 15 }} id="sheet-title">{t("wake.ok")}</b>
              <span className="muted" style={{ fontSize: 12.5 }}>{t("wake.okSub")}</span>
              <span className="muted" style={{ fontSize: 11.5 }}>{t("wake.okCost")}</span>
            </div>
          ) : (
            <div className="big-ok" role="status">
              <span className="okc bad">!</span>
              <b style={{ fontSize: 15 }} id="sheet-title">{saved ? t("wake.savedBroken") : t("wake.failed")}</b>
              <span className="muted" style={{ fontSize: 12.5 }}>{check ? wakeReason(check.status, check.reason) : act.error}</span>
            </div>
          )}
          {saved && secret && !isGrok(url) && (
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
