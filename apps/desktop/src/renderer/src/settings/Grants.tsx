import { useTranslation } from "react-i18next";
import type { Client } from "@hermesh/proto/local";
import { agoText, dateOf, monthDay, sameDay, updatedDate } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { AgentName, GrantAvatar } from "../components";
import { hostOf } from "../sheets/Pairing";
import { machineName } from "../Sidebar";
import { useNow, useStore, type Store } from "../store";
import { GoRow, More, Row } from "./common";

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

function projectsText(c: Client): string {
  const p = (c.spec?.projectIds ?? []).filter((x) => x !== "*");
  return p.length ? list(p) : i18n.t("settings:grants.allProjects");
}

function machinesText(st: Store, c: Client): string {
  const m = (c.spec?.machineIds ?? []).filter((x) => x !== "*");
  return m.length ? list(m.map((id) => machineName(st, id))) : i18n.t("settings:grants.allMachines");
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
            <GoRow key={x.grantId} icon={<GrantAvatar label={x.label} size="md" />} title={x.label} sub={`${projectsText(x)} · ${machinesText(st, x)}`} onClick={() => select(x.grantId)}>
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
  const scope = (renew: boolean) => st.set({ sheet: { kind: "scope", clientId: c.clientId, renew } });
  const revoke = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("grants.disconnectTitle", { name: c.label }),
        message: t("grants.disconnectMessage"),
        button: t("grants.disconnectButton"),
        run: async () => {
          const r = await local.revokeClient({ grantId: c.grantId });
          const offline = r.acks.filter((a) => !a.ok).map((a) => a.machineName);
          st.show(offline.length ? t("grants.disconnectedOffline", { name: c.label, machines: list(offline) }) : t("grants.disconnected", { name: c.label }));
        },
      },
    });
  return (
    <div className="form">
      <div className="dhero">
        <GrantAvatar label={c.label} size="xl" />
        <div>
          <b>{c.label}</b>
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
        <button className="btn" onClick={() => scope(false)}>{t("grants.changeScope")}</button>
        <button className="btn" onClick={() => scope(true)}>{t("grants.renew")}</button>
        <button className="btn danger push" onClick={revoke}>{t("grants.disconnect")}</button>
      </div>
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
