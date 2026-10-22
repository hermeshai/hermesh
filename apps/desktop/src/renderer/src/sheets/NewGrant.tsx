import { Fragment, useState } from "react";
import { useTranslation } from "react-i18next";
import type { PairingDetails } from "@hermesh/proto/local";
import { scopeComplete } from "@hermesh/core";
import { Mark, personalAgents } from "@hermesh/ui";
import { PersonalAgentIcon } from "../components";
import { useConnect, Waiting } from "../connect";
import { Icon } from "../icons";
import { kindName, signsInByLink } from "../personal";
import "../firstrun/firstrun.css";
import { useStore } from "../store";
import { ErrorLine, SignerWaiting } from "./common";
import { SelfReport } from "./Pairing";
import { ScopeRows } from "./Scope";

/**
 * Connect Agent…: the first run's "Connect Your Agent" page in a sheet. Pick the
 * app, send it the message, and once any agent's request arrives, choose
 * what it may use and approve (useConnect); the verification code is
 * compared in the signer's window.
 */
export function NewGrantSheet() {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const c = useConnect();
  const { request, act } = c;
  const [done, setDone] = useState<string>();

  const approve = async (p: PairingDetails) => {
    const rest = c.waiting.length;
    if (!(await c.approve(p))) return;
    if (rest) st.show(t("pairing.approvedClient", { name: p.label }));
    else setDone(p.label);
  };

  return (
    <>
      <h3 id="sheet-title">{t("newGrant.title")}</h3>
      {done ? (
        <div className="big-ok">
          <span className="okc"><Icon name="check" /></span>
          <b style={{ fontSize: 15 }}>{t("newGrant.connected", { name: done })}</b>
          <span className="muted" style={{ fontSize: 12.5 }}>{t("newGrant.tryIt")}</span>
        </div>
      ) : request ? (
        <Fragment key={request.code}>
          <p className="sub">{t(request.confirmed ? "newGrant.requestBodyConfirmed" : "newGrant.requestBody", { name: request.label })}</p>
          {!act.busy && <Waiting c={c} another />}
          <p className="sheet-q pa-who">
            <PersonalAgentIcon kind={c.requestKind!} size={28} />
            {t("pairing.canUse", { name: request.label })}
          </p>
          <ScopeRows scope={c.scope} onChange={c.setScope} />
          <p className="sheet-note muted">{t("pairing.scopeNote")}</p>
          <p className="sheet-note muted"><SelfReport p={request} /></p>
          <ErrorLine text={act.error} />
          {act.busy && (
            <SignerWaiting detail={request.confirmed ? t("pairing.signerConfirmed") : t("newGrant.signerDetail", { name: request.label })} />
          )}
        </Fragment>
      ) : (
        <>
          <p className="sub">{t("newGrant.messageBody")}</p>
          <Waiting c={c} />
          <div className="pa-pick" role="radiogroup" aria-label={t("newGrant.pick")}>
            {personalAgents.map((k) => (
              <button key={k.id} className={`pa ${k.id === c.kind.id ? "on" : ""}`} role="radio" aria-checked={k.id === c.kind.id} aria-label={kindName(k)} onClick={() => c.pick(k.id)}>
                <PersonalAgentIcon kind={k} size={40} />
                <b>{kindName(k)}</b>
              </button>
            ))}
          </div>
          {c.other && (
            <div className="pa-name">
              <label htmlFor="cf-name">{t("newGrant.name")}</label>
              <input id="cf-name" className="field" maxLength={64} value={c.otherName} onChange={(e) => c.rename(e.target.value)} />
            </div>
          )}
          <div className="pa-msg">
            <div className="msgbox sel-text">{c.message}</div>
            <button className={`btn sm ${c.copied ? "" : "primary"}`} onClick={c.copy}>
              <Icon name={c.copied ? "check" : "copy"} />
              {c.copied ? t("copied") : t("copy")}
            </button>
          </div>
          <div className="pa-wait">
            <Mark size={16} variant="seq" />
            {c.copied ? t(signsInByLink(st.status) ? "newGrant.waitingLink" : "newGrant.waiting", { name: c.label }) : t("newGrant.copyHint", { name: c.label })}
          </div>
        </>
      )}
      <div className="sheet-foot">
        {!done && request && !act.busy && <button className="btn left" onClick={() => c.deny(request, t("newGrant.denied"))}>{t("deny")}</button>}
        <button className="btn" onClick={() => st.set({ sheet: null })}>{done ? t("done") : t("cancel")}</button>
        {!done && request && !act.busy && <button className="btn primary" disabled={!scopeComplete(c.scope)} onClick={() => approve(request)}>
          <Icon name="lock" />
          {t("approve")}
        </button>}
      </div>
    </>
  );
}
