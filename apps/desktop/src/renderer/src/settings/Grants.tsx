import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Client } from "@hermesh/proto/local";
import { agoText, dateOf, monthDay, sameDay, updatedDate } from "@hermesh/core";
import { agentName } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { AgentName, GrantAvatar } from "../components";
import { hostOf } from "../sheets/Pairing";
import { machineName } from "../Sidebar";
import { useNow, useStore, type Store } from "../store";
import { GoRow, More, Row } from "./common";
import { LookSheet } from "./LookSheet";
import { Wakes } from "./Wakes";

/** Whole days until the grant ends (0 once it has). */
function daysLeft(c: Client, now: Date): number {
  const d = dateOf(c.expiresAt);
  return d ? Math.max(0, Math.ceil((d.getTime() - now.getTime()) / 86_400_000)) : 0;
}

/** "30 days left", or, in the last week, "Expires in 4 days" in orange. */
function Left({ days }: { days: number }) {
  const { t } = useTranslation("settings");
  return days <= 7 ? <span className="warn">{t("grants.expiresIn", { count: days })}</span> : <span>{t("grants.daysLeft", { count: days })}</span>;
}

/** Names as a list ("a, b"). */
function list(names: string[]): string {
  return names.join(i18n.t("settings:listSeparator"));
}

/**
 * Who used a grant last, and when, which tells grants apart: "hermesh 在用 ·
 * 2 分钟前", "3 天前用过". The names are those a hosted app's bots go by; an
 * agent on a computer names its conversations instead.
 */
function usedText(st: Store, c: Client, now: Date): string {
  const mine = st.sessions.filter((s) => s.createdBy === c.clientId).sort((a, b) => (dateOf(b.createdAt)?.getTime() ?? 0) - (dateOf(a.createdAt)?.getTime() ?? 0));
  const seen = dateOf(c.lastSeen);
  const started = dateOf(mine[0]?.createdAt);
  const last = !seen || (started && started > seen) ? started : seen;
  if (!last) return i18n.t("settings:grants.notUsed");
  const names = c.custodian ? [...new Set(mine.map((s) => s.createdAs).filter((n) => n))].slice(0, 2) : [];
  const ago = agoText(last, now);
  return names.length ? i18n.t("settings:grants.usedBy", { names: list(names), ago }) : i18n.t("settings:grants.usedAgo", { ago });
}

/** Access: the grants, each opening what it may use and changing or ending it. */
export function Grants({ open, select }: { open: string; select: (id: string) => void }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(60_000);
  const clients = st.clients.filter((c) => !c.revoked);
  const c = clients.find((x) => x.grantId === open);
  const connect = () => st.set({ sheet: { kind: "newGrant" } });
  if (c) return <GrantDetail c={c} />;
  return (
    <div className="form">
      {clients.length > 0 ? (
        <div className="fgroup" role="list" aria-label={t("pages.grants")}>
          {clients.map((x) => (
            <GoRow key={x.grantId} icon={<GrantAvatar c={x} size="md" />} title={agentName(x.label)} sub={usedText(st, x, now)} onClick={() => select(x.grantId)}>
              <span className="fv-side">
                <Left days={daysLeft(x, now)} />
              </span>
            </GoRow>
          ))}
        </div>
      ) : (
        <div className="fgroup">
          <div className="frow muted">{t("grants.none")}</div>
        </div>
      )}
      <div className="set-acts">
        <button className="btn" onClick={connect}>{t("grants.connect")}</button>
      </div>
      <p className="set-note">{t("grants.note")}</p>
    </div>
  );
}

function GrantDetail({ c }: { c: Client }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(30_000);
  const all = st.sessions.filter((s) => s.createdBy === c.clientId);
  const today = all.filter((s) => sameDay(updatedDate(s), now)).length;
  const expires = dateOf(c.expiresAt);
  const seen = dateOf(c.lastSeen);
  const agents = (c.spec?.agents ?? []).filter((a) => a !== "*");
  const projects = (c.spec?.projectIds ?? []).filter((p) => p !== "*");
  const machines = (c.spec?.machineIds ?? []).filter((m) => m !== "*");
  const name = agentName(c.label);
  const [editing, setEditing] = useState(false);
  const scope = (renew: boolean) => st.set({ sheet: { kind: "scope", clientId: c.clientId, renew } });
  const rename = () => setEditing(true);
  const revoke = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("grants.disconnectTitle", { name }),
        message: t("grants.disconnectMessage"),
        button: t("grants.disconnectButton"),
        run: async () => {
          const r = await local.revokeClient({ grantId: c.grantId });
          const offline = r.acks.filter((a) => !a.ok).map((a) => a.machineName);
          st.show(offline.length ? t("grants.disconnectedOffline", { name, machines: list(offline) }) : t("grants.disconnected", { name }));
        },
      },
    });
  return (
    <div className="form">
      <div className="dhero">
        <button className="dhero-av" aria-label={t("grants.renameTitle")} onClick={rename}>
          <GrantAvatar c={c} size="xl" />
        </button>
        <div>
          <b>{name}</b>
          <span>
            {t("grants.validUntil", { date: expires ? monthDay(expires) : "—" })} · <Left days={daysLeft(c, now)} />
          </span>
        </div>
      </div>
      <div>
        <div className="fhead">{t("grants.canUse")}</div>
        <div className="fgroup">
          <Row k={t("grants.projects")}>{projects.length ? list(projects) : t("grants.allProjectsLater")}</Row>
          <Row k={t("grants.machines")}>{machines.length ? list(machines.map((id) => machineName(st, id))) : t("grants.allMachinesLater")}</Row>
          <Row k={t("grants.codingAgents")}>{agents.length ? agents.map((a) => <AgentName key={a} agent={a} full />) : t("grants.allCodingAgents")}</Row>
          {c.custodian && <Row k={t("grants.through")}>{t("grants.throughValue", { server: hostOf(c.custodian) })}</Row>}
        </div>
      </div>
      <div className="set-acts">
        <button className="btn" onClick={rename}>{t("grants.rename")}</button>
        <button className="btn" onClick={() => scope(false)}>{t("grants.changeScope")}</button>
        <button className="btn" onClick={() => scope(true)}>{t("grants.renew")}</button>
        <button className="btn danger push" onClick={revoke}>{t("grants.disconnect")}</button>
      </div>
      <Wakes c={c} />
      {editing && <LookSheet c={c} st={st} close={() => setEditing(false)} />}
      <More>
        <div className="fgroup">
          <div className="frow">
            <span className="fl">{t("grants.sessions")}</span>
            <span className="fv">
              {t("grants.sessionCount", { today, total: all.length })}
              <button className="btn sm" disabled={all.length === 0} onClick={() => (window.hermesh?.showInMain({ grant: c.clientId }), window.close())}>
                {t("showInMain")}
              </button>
            </span>
          </div>
          {(c.online || seen) && <Row k={t("grants.lastOnline")}>{c.online ? t("grants.now") : agoText(seen!, now)}</Row>}
          <Row k={t("grants.fingerprint")}>
            <span className="mono sel-text">{c.fingerprint}</span>
          </Row>
        </div>
      </More>
    </div>
  );
}
