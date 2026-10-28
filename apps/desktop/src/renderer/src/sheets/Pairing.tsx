import { useLayoutEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { PairingKind } from "@hermesh/proto/identity";
import type { PairingDetails } from "@hermesh/proto/local";
import { scopeComplete, scopeSpec } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { Icon } from "../icons";
import { useStore, type Store } from "../store";
import { ErrorLine, SignerWaiting, useAction, useWaitForPairings } from "./common";
import { newScope, ScopeRows } from "./Scope";

function pairingTitle(p: PairingDetails): string {
  switch (p.kind) {
    case PairingKind.CLIENT:
      return i18n.t("sheets:pairing.titleClient", { name: p.label });
    case PairingKind.DEVICE:
      return i18n.t("sheets:pairing.titleDevice", { name: p.label });
    default:
      return i18n.t("sheets:pairing.titleMachine", { name: p.label });
  }
}

/** The host of a custodian URL ("https://hermesh.io" → "hermesh.io"). */
export function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/**
 * Who asks, with what it self-reports; the verification code is compared in
 * the signer's window, unless the person already confirmed the request as
 * theirs (in the browser), or entered the code a machine or device shows,
 * which binds its key: then there is none to compare.
 */
export function SelfReport({ p }: { p: PairingDetails }) {
  const { t } = useTranslation("sheets");
  const who = [p.hostname, p.platform].filter(Boolean).join(" · ");
  if (p.confirmed && p.kind !== PairingKind.CLIENT) return <>{t("pairing.byCode", { who })}</>;
  if (p.confirmed) return <>{t("pairing.confirmed", { who })}</>;
  return (
    <>
      {t("pairing.reported", { who })} <span className="mono">{p.verificationCode}</span>
    </>
  );
}

/** A request from a notification or the menu bar: in its own sheet, unless "Connect Your Agent" shows and it is an agent's (that page shows it). */
export function openPairing(st: Store, code: string) {
  if (st.sheet?.kind === "newGrant" && st.pairings.some((p) => p.code === code && p.kind === PairingKind.CLIENT)) return;
  st.set({ sheet: { kind: "pairing", code } });
}

/**
 * A pairing request: a machine (named here), a personal agent (its scope)
 * or a trusted device. Approve blocks while the signer shows the request and
 * asks for Touch ID. Deny puts it off (it expires by itself); Not Now just
 * closes. Approved or put off, the next request of its kind waiting follows.
 */
export function PairingSheet({ code, details }: { code: string; details?: PairingDetails }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  // Opened again (a notification, a typed code): no longer put off.
  const { dismissed, set } = st;
  useLayoutEffect(() => {
    if (dismissed.includes(code)) set({ dismissed: dismissed.filter((c) => c !== code) });
  }, [code, dismissed, set]);
  const [held, setHeld] = useState<PairingDetails>();
  const p = held ?? st.pairings.find((x) => x.code === code) ?? details;
  const [name, setName] = useState<string>();
  const [scope, setScope] = useState(() => newScope(st));
  const act = useAction();
  useWaitForPairings();
  const close = () => st.set({ sheet: null });
  // The sheet for the next request of its kind waiting, if any.
  const next = (p: PairingDetails) => {
    const n = st.pairings.find((x) => x.kind === p.kind && x.code !== p.code);
    return n ? { kind: "pairing" as const, code: n.code } : null;
  };
  if (!p) {
    return (
      <>
        <h3 id="sheet-title">{t("pairing.goneTitle")}</h3>
        <p className="sub">{t("pairing.goneBody")}</p>
        <div className="sheet-foot"><button className="btn" onClick={close}>{t("close")}</button></div>
      </>
    );
  }
  const client = p.kind === PairingKind.CLIENT;
  const device = p.kind === PairingKind.DEVICE;
  const machineName = name?.trim() || p.label;
  // Agents granted all machines get this one too.
  const everywhere = st.clients.filter((c) => !c.revoked && c.spec?.machineIds.includes("*")).map((c) => t("pairing.quoted", { name: c.label }));

  const approve = async () => {
    setHeld(p);
    // A Mac joining as a machine only may ask for the two digits, shown in the signer's window.
    let digits = false;
    const ok = await act.go(async () => {
      if (device) return local.approveDevicePairing({ code: p.code });
      if (client) {
        const spec = scopeSpec(scope);
        spec.label = p.label;
        return local.approveClientPairing({ code: p.code, spec });
      }
      digits = (await local.approveMachinePairing({ code: p.code, machineName })).matchCode !== "";
    });
    if (!ok) return setHeld(undefined);
    st.set({ sheet: next(p) });
    st.show(
      device ? t("pairing.approvedDevice", { name: p.label })
      : client ? t("pairing.approvedClient", { name: p.label })
      : digits ? t("pairing.approvedMachineDigits", { name: machineName })
      : t("pairing.approvedMachine", { name: machineName }),
    );
    st.refresh();
  };
  const reject = () => {
    st.set({ sheet: next(p), dismissed: [...st.dismissed, p.code] });
    st.show(t("pairing.putOff"));
  };

  return (
    <>
      <h3 id="sheet-title">{pairingTitle(p)}</h3>
      <p className="sub"><SelfReport p={p} /></p>
      {client ? (
        <>
          <p className="sheet-q">{t("pairing.canUse", { name: p.label })}</p>
          <ScopeRows scope={scope} onChange={setScope} />
          <p className="sheet-note muted">{t("pairing.scopeNote")}</p>
          {p.custodian && <p className="sheet-note muted">{t("pairing.custodianNote", { name: p.label, server: hostOf(p.custodian) })}</p>}
        </>
      ) : device ? (
        <>
          <p className="sheet-q">{t("pairing.addDevice", { name: p.label })}</p>
          <p className="sheet-note muted">{t("pairing.deviceNote")}</p>
        </>
      ) : (
        <>
          <p className="sheet-q">{t("pairing.nameMachine")}</p>
          <div className="fgroup">
            <div className="frow">
              <label className="fl" htmlFor="pairing-name">{t("pairing.machineName")}</label>
              <span className="fv">
                <input id="pairing-name" className="field" style={{ width: 220 }} value={name ?? p.label} autoFocus onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && !act.busy && approve()} />
              </span>
            </div>
          </div>
          <p className="sheet-note muted">{t("pairing.machineNote")}</p>
          {everywhere.length > 0 && <p className="sheet-note muted">{t("pairing.everywhere", { names: everywhere.join(t("scope.separator")) })}</p>}
        </>
      )}
      {act.busy && <SignerWaiting detail={t(p.confirmed ? "pairing.signerConfirmed" : "pairing.signerDetail")} />}
      <ErrorLine text={act.error} />
      <div className="sheet-foot">
        <button className="btn left" disabled={act.busy} onClick={reject}>{t("deny")}</button>
        <button className="btn" onClick={close}>{t("pairing.later")}</button>
        <button className="btn primary" disabled={act.busy || (client && !scopeComplete(scope))} onClick={approve}>
          <Icon name="lock" />
          {device ? t("pairing.addTrusted") : t("approve")}
        </button>
      </div>
    </>
  );
}

/** For a machine or agent that paired without the connection address: the code its terminal shows. */
export function EnterCodeSheet() {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [code, setCode] = useState("");
  const act = useAction();
  const complete = code.replace(/[^0-9a-z]/gi, "").length >= 12;
  const find = async (e: FormEvent) => {
    e.preventDefault();
    let found: PairingDetails | undefined;
    if (await act.go(async () => (found = (await local.lookupPairing({ code: code.trim() })).details)) && found) st.set({ sheet: { kind: "pairing", code: found.code, details: found } });
  };
  return (
    <form onSubmit={find}>
      <h3 id="sheet-title">{t("enterCode.title")}</h3>
      <p className="sub">{t("enterCode.sub")}</p>
      <input className="field mono code-in" aria-label={t("enterCode.label")} placeholder="XXXX-XXXX-XXXX" autoFocus value={code} onChange={(e) => setCode(e.target.value)} />
      <ErrorLine text={act.error} />
      <div className="sheet-foot">
        <button type="button" className="btn" onClick={() => st.set({ sheet: null })}>{t("cancel")}</button>
        <button type="submit" className="btn primary" disabled={act.busy || !complete}>{act.busy ? t("enterCode.finding") : t("enterCode.find")}</button>
      </div>
    </form>
  );
}
