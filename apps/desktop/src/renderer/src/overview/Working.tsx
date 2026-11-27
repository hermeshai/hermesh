import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { displayTitle, isActive, needsAttention } from "@hermesh/core";
import { agentName, grantColor, Mark, type GrantLook } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { grantFace } from "../components";
import { Icon } from "../icons";
import { machineName, senderLine } from "../Sidebar";
import { useStore, type Store } from "../store";
import { FinishedRow, RunningRow, useTeam } from "../team";
import { OWNER_ID, sessionKeys } from "./Map";

/**
 * "In Progress": a row per session at work (those that wait for the owner or
 * failed are under Needs You). pick, a node clicked on the map, turns it
 * into the sessions through that node, whatever their state: those that need
 * the owner, those at work, then the last three that finished; the heading
 * names the node.
 */
export function Working({ pick, onClear }: { pick: string; onClear: () => void }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const team = useTeam();
  const through = (list: Session[]) => list.filter((s) => sessionKeys(s).includes(pick));
  const rows = pick ? [...through(team.waiting), ...through(team.failed), ...through(team.running), ...through(team.finished).slice(0, 3)] : team.running;
  return (
    <section aria-label={i18n.t("common:team.inProgress")}>
      <div className="sec-h">
        {pick ? <Condition pick={pick} count={rows.length} onClear={onClear} /> : <>{i18n.t("common:team.inProgress")}{rows.length > 0 && <span className="n">{rows.length}</span>}</>}
        {/* A machine only has no grants to manage. */}
        {!st.status?.machineOnly && <button className="link" onClick={() => window.hermesh?.openSettings("grants")}>{t("working.manageAccess")}</button>}
      </div>
      {rows.length > 0 ? (
        <div className="card wk-list" role="list">
          {rows.map((s) => <div key={s.sessionId} role="listitem">{isActive(s) ? <RunningRow s={s} /> : needsAttention(s) ? <NeedRow s={s} /> : <FinishedRow s={s} />}</div>)}
        </div>
      ) : pick ? (
        <div className="card wnone">{t("working.noneThrough")}</div>
      ) : (
        <div className="card"><Idle /></div>
      )}
    </section>
  );
}

/** A session that waits for the owner or failed, among those through a node: which, from whom, and how it stands. */
function NeedRow({ s }: { s: Session }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const waits = s.status === SessionStatus.WAITING_INPUT;
  return (
    <button className="tm-row" onClick={() => st.select(s.sessionId)}>
      <i className={waits ? "dot-w" : "dot-f"} aria-hidden="true" />
      <span className="tm-main">
        <b>{displayTitle(s)}</b>
        <span>{senderLine(st, s)}</span>
      </span>
      <span className="tm-when">{waits ? t("working.waiting") : s.status === SessionStatus.CRASHED ? t("working.crashed") : t("working.failed")}</span>
    </button>
  );
}

/** What the rows are narrowed to, in words, with the way back. */
function Condition({ pick, count, onClear }: { pick: string; count: number; onClear: () => void }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const { icon, text } = describe(st, pick);
  return (
    <span className="wcond" role="status">
      {icon}
      <span>{text}</span>
      <span className="n">{t("working.matches", { count })}</span>
      <button onClick={onClear}>{t("working.showAll")}</button>
    </span>
  );
}

function describe(st: Store, pick: string): { icon: ReactNode; text: string } {
  const id = pick.slice(2);
  if (pick.startsWith("g:")) {
    if (id === OWNER_ID) {
      return {
        icon: (
          <span className="av you" style={{ width: 16, height: 16 }} aria-hidden="true">
            <Icon name="person" />
          </span>
        ),
        text: i18n.t("overview:working.byYou"),
      };
    }
    const c = st.clients.find((x) => x.clientId === id);
    const label = c ? agentName(c.label) : i18n.t("overview:working.disconnected");
    return { icon: <GrantIcon c={c ?? { label }} size={16} />, text: i18n.t("overview:working.byGrant", { label }) };
  }
  if (pick.startsWith("m:")) {
    const m = st.machines.find((x) => x.machineId === id);
    return { icon: <Icon name={m ? machineIcon(m) : "server"} className="sm" />, text: i18n.t("overview:working.onMachine", { machine: m?.local ? i18n.t("overview:thisMac") : machineName(st, id) }) };
  }
  return { icon: <Icon name="folder" className="sm" />, text: i18n.t("overview:working.inProject", { project: id.split("/").slice(1).join("/") }) };
}

function GrantIcon({ c, size }: { c: GrantLook; size: number }) {
  const label = c.label;
  const face = grantFace(c, size);
  if (face) return face;
  return (
    <span className="av" style={{ width: size, height: size, fontSize: size * 0.55, background: grantColor(label) }} aria-hidden="true">
      {label.slice(0, 1).toUpperCase()}
    </span>
  );
}

function machineIcon(m: Machine): string {
  if (m.local) return "laptop";
  const p = (m.status?.platform ?? "").toLowerCase();
  return p.includes("darwin") || p.includes("mac") ? "mini" : "server";
}

/** Nothing live: the mark at rest, and what will show here. */
function Idle() {
  const { t } = useTranslation("overview");
  return (
    <div className="wrest" role="status">
      <Mark size={34} className="wrest-mark" />
      <b>{t("working.nothingRuns")}</b>
      <p>{t("working.nothingRunsHint")}</p>
    </div>
  );
}
