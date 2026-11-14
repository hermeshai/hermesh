import { useEffect, useState, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { Client, PairingDetails } from "@hermesh/proto/local";
import { PairingKind } from "@hermesh/proto/identity";
import { dateOf, displayTitle, pendingQuestion } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { opensActivityMonitor, problemBody, problemsOf, problemTitle, severe } from "../health";
import { Icon } from "../icons";
import { senderLine } from "../Sidebar";
import { useStore, type Store } from "../store";
import { QuickAnswer, useTeam } from "../team";

const day = 86_400_000;

/** Grants that run out within a week. */
export function expiringClients(st: Store, now: Date): Client[] {
  return st.clients.filter((c) => {
    const d = !c.revoked && dateOf(c.expiresAt);
    return d && d.getTime() - now.getTime() < 7 * day;
  });
}

/**
 * "Needs You": what waits on the owner. Machines kept from working first;
 * then the sessions that ask a question, answered here when one click can
 * (docs/her-team.md §14.4), and those whose turn failed; then pairing requests
 * and grants about to expire, and an older signer than the app's (Windows).
 * A machine only has only its own health here.
 */
export function Attention({ now }: { now: Date }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const owner = !st.status?.machineOnly;
  const expiring = owner ? expiringClients(st, now) : [];
  const sick = st.machines.flatMap((m) => problemsOf(m).map((p) => ({ m, p })));
  const team = useTeam();
  // The signer the app carries, newer than the one set up: it signs what the old one can't show.
  const [signer, setSigner] = useState<string>();
  useEffect(() => {
    if (owner) window.hermesh?.signerUpdate().then(setSigner, () => {});
  }, [owner]);
  const count = sick.length + team.waiting.length + team.failed.length + st.pairings.length + expiring.length + (signer ? 1 : 0);
  if (count === 0) return null;
  return (
    <section aria-label={t("attention.title")}>
      <div className="sec-h">{t("attention.title")}<span className="n">{count}</span></div>
      <div className="card attn" role="list">
        {sick.map(({ m, p }) => (
          <Row key={`${m.machineId}:${p.kind}`} icon="warn" tint={severe(p) ? "r" : "y"} title={problemTitle(m, p)} detail={problemBody(p)} wrap>
            {opensActivityMonitor(m, p) ? (
              <button className="btn" onClick={() => window.hermesh?.openActivityMonitor()}>{i18n.t("common:health.openActivityMonitor")}</button>
            ) : (
              <button className="btn" onClick={() => window.hermesh?.openSettings(`machines/${m.machineId}`)}>{i18n.t("common:health.view")}</button>
            )}
          </Row>
        ))}
        {team.waiting.map((s) => {
          const q = pendingQuestion(s);
          const words = q?.items.map((i) => i.text || i.header).filter(Boolean).join(" ");
          return (
            <Row key={s.sessionId} icon="q" tint="y" title={displayTitle(s)} detail={words ? `${senderLine(st, s)} · ${words}` : senderLine(st, s)} wrap>
              <QuickAnswer s={s} />
              <button className="btn" onClick={() => st.select(s.sessionId)}>{t("attention.open")}</button>
            </Row>
          );
        })}
        {team.failed.map((s) => (
          <Row key={s.sessionId} icon="alert" tint="r" title={displayTitle(s)} detail={`${senderLine(st, s)} · ${s.progress?.lastFailure?.message || i18n.t("common:team.lastTurnFailed")}`}>
            <button className="btn" onClick={() => st.select(s.sessionId)}>{t("attention.open")}</button>
          </Row>
        ))}
        {st.pairings.map((p) => (
          <Row
            key={p.code}
            icon="key"
            tint="a"
            title={pairingTitle(p)}
            detail={
              p.confirmed ? (
                p.platform || p.hostname
              ) : (
                <Trans
                  t={t}
                  i18nKey="attention.pairingDetail"
                  values={{ device: p.platform || p.hostname, code: p.verificationCode }}
                  components={{ code: <code className="mono" /> }}
                />
              )
            }
          >
            <button className="btn primary" onClick={() => st.set({ sheet: { kind: "pairing", code: p.code } })}>{t(p.confirmed ? "attention.review" : "attention.verify")}</button>
          </Row>
        ))}
        {expiring.map((c) => {
          const days = Math.max(0, Math.ceil(((dateOf(c.expiresAt)?.getTime() ?? 0) - now.getTime()) / day));
          return (
            <Row key={c.clientId} icon="clock" tint="y" title={t("attention.expires", { label: c.label, count: days })} detail={t("attention.expiresDetail")}>
              <button className="btn" onClick={() => st.set({ sheet: { kind: "scope", clientId: c.clientId, renew: true } })}>{t("attention.renew")}</button>
            </Row>
          );
        })}
        {signer && (
          <Row icon="shield" tint="a" title={t("attention.signer")} detail={t("attention.signerDetail", { version: signer })}>
            <button
              className="btn"
              onClick={() =>
                st.run(async () => {
                  await window.hermesh?.updateSigner();
                  setSigner(await window.hermesh?.signerUpdate());
                }, t("attention.signerUpdated"))
              }
            >
              {t("attention.updateSigner")}
            </button>
          </Row>
        )}
      </div>
    </section>
  );
}

function pairingTitle(p: PairingDetails): string {
  switch (p.kind) {
    case PairingKind.DEVICE:
      return i18n.t("overview:attention.pairingDevice", { label: p.label });
    case PairingKind.CLIENT:
      return i18n.t("overview:attention.pairingClient", { label: p.label });
    default:
      return i18n.t("overview:attention.pairingMachine", { label: p.label });
  }
}

function Row({ icon, tint, title, detail, wrap, children }: { icon: string; tint: string; title: string; detail: ReactNode; wrap?: boolean; children: ReactNode }) {
  return (
    <div className="arow" role="listitem">
      <span className={`ai ${tint}`}><Icon name={icon} /></span>
      <div className="at">
        <b>{title}</b>
        <span className={wrap ? "wrap" : undefined}>{detail}</span>
      </div>
      {children}
    </div>
  );
}
