import { useState, type FocusEvent, type MouseEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { agoText, codingAgentTitle, dateOf, isActive } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { GrantAvatar } from "./components";
import { Cat } from "./her/Cat";
import { unreadCount } from "./her/environments";
import { Icon } from "./icons";
import { listShown, setSidebar } from "./layout";
import { AppMenuButton } from "./MainWindow";
import { keys, ownKeys } from "./platform";
import { AccountMenu, herClients, openList, progressMarks, workMarks } from "./Sidebar";
import { useNow, useStore, type ListFilter } from "./store";

// The sidebar's rail (docs/her-team.md §14.9): the same places as the sidebar, as icons, 74 px wide. Her, Work,
// Search and All Sessions; the machines, each with its coding agents under it; the connected agents; the account.
// Each says on hover (or focus) what it is and how it stands; the list's column, beside, says where you are.

/** What a rail button says beside it while the pointer or the focus is on it. */
type Tip = { text: string; top: number } | null;

export function Rail() {
  const st = useStore();
  const { t } = useTranslation("common");
  const now = useNow(30_000);
  const [tip, setTip] = useState<Tip>(null);
  const show = (text: string) => ({
    onMouseEnter: (e: MouseEvent<HTMLElement>) => setTip({ text, top: e.currentTarget.getBoundingClientRect().top + e.currentTarget.offsetHeight / 2 }),
    onFocus: (e: FocusEvent<HTMLElement>) => setTip({ text, top: e.currentTarget.getBoundingClientRect().top + e.currentTarget.offsetHeight / 2 }),
    onMouseLeave: () => setTip(null),
    onBlur: () => setTip(null),
  });
  const sel = st.selection;
  const inList = listShown(sel);
  const same = (f: ListFilter | null) => inList && JSON.stringify(st.listFilter) === JSON.stringify(f);
  const live = st.sessions.filter((s) => s.status !== SessionStatus.ARCHIVED);
  const her = herClients(st);
  const owner = !st.status?.machineOnly;
  const unread = unreadCount(st.machines);
  const herOn = st.machines.some((m) => m.her?.enabled);
  const work = workMarks(st, now);
  const progress = progressMarks(st);
  const progressLabel = [t("sidebar.progress"), progress.waits ? t("rail.waiting", { count: progress.waits }) : "", progress.running ? t("sidebar.running", { count: progress.running }) : ""].filter(Boolean).join(" · ");
  const machines = [...st.machines].sort((a, b) => Number(b.local) - Number(a.local) || a.machineName.localeCompare(b.machineName));
  const clients = st.clients.filter((c) => !c.revoked && !her.includes(c.clientId));
  const state = (mine: Session[]) => {
    const waiting = mine.filter((s) => s.status === SessionStatus.WAITING_INPUT).length;
    const running = mine.filter(isActive).length;
    return { waiting, running, words: [waiting ? t("rail.waiting", { count: waiting }) : "", running ? t("sidebar.running", { count: running }) : ""].filter(Boolean) };
  };
  const button = (o: { label: string; current: boolean; onClick: () => void; small?: boolean; children: ReactNode; marks?: ReactNode }) => (
    <button className={`rb ${o.small ? "small" : ""} ${o.current ? "sel" : ""}`} aria-label={o.label} aria-current={o.current || undefined} onClick={o.onClick} {...show(o.label)}>
      {o.children}
      {o.marks}
    </button>
  );
  const marks = (m: { waiting: number; running: number }) => (
    <>
      {m.waiting > 0 && <i className="rb-dot" aria-hidden="true" />}
      {m.running > 0 && <span className="rb-busy" aria-hidden="true"><i /></span>}
    </>
  );
  const herLabel = !herOn ? t("rail.herOff") : unread > 0 ? t("sidebar.herUnread", { count: unread }) : "Her";
  const workLabel = [t("sidebar.work"), work.waits ? t("sidebar.attention", { count: work.waits }) : "", work.running ? t("sidebar.running", { count: work.running }) : ""].filter(Boolean).join(" · ");
  const allLabel = `${t("sidebar.allSessions")} · ${live.length}`;
  return (
    <aside className="rail" aria-label={t("rail.label")}>
      <div className="rail-top drag">
        <AppMenuButton />
      </div>
      <nav className="rail-nav" onScroll={() => setTip(null)}>
        {owner &&
          button({
            label: herLabel,
            current: sel.kind === "her",
            onClick: () => sel.kind !== "her" && st.set({ selection: { kind: "her" } }),
            children: <span className="rb-cat" aria-hidden="true"><Cat small /></span>,
            marks: herOn ? unread > 0 && <span className="rb-badge" aria-hidden="true">{unread}</span> : <span className="rb-off" aria-hidden="true" />,
          })}
        {owner &&
          herOn &&
          button({
            label: progressLabel,
            current: sel.kind === "progress",
            onClick: () => st.set({ selection: { kind: "progress" } }),
            children: <Icon name="pulse" />,
            marks: (
              <>
                {progress.waits > 0 && <i className="rb-dot" aria-hidden="true" />}
                {progress.running > 0 && <span className="rb-busy" aria-hidden="true"><i />{progress.running}</span>}
              </>
            ),
          })}
        {button({
          label: workLabel,
          current: sel.kind === "overview",
          onClick: () => st.set({ selection: { kind: "overview" } }),
          children: <Icon name="grid" />,
          marks: (
            <>
              {work.waits > 0 && <i className="rb-dot" aria-hidden="true" />}
              {work.running > 0 && <span className="rb-busy" aria-hidden="true"><i />{work.running}</span>}
            </>
          ),
        })}
        {button({ label: `${t("sidebar.search")} ${keys("CmdOrCtrl+K")}`, current: false, onClick: () => st.set({ searching: true }), children: <Icon name="search" /> })}
        {button({ label: allLabel, current: same(null), onClick: () => openList(st, null), children: <Icon name="menu" /> })}
        {owner && (
          <>
            <div className="rail-sep" role="separator" aria-label={t("sidebar.team")} />
            {machines.map((m) => {
              const f: ListFilter = { kind: "machine", machine: m.machineId };
              const seen = dateOf(m.lastSeen);
              const name = m.local ? t("sidebar.thisMac") : m.machineName;
              const where = !(m.local || m.online) ? (seen ? t("sidebar.offlineSince", { ago: agoText(seen, now) }) : t("sidebar.offline")) : m.her?.enabled ? t("sidebar.herHere") : t("rail.online");
              const agents = (m.status?.agents ?? []).filter((a) => a.available);
              return (
                <div key={m.machineId} className="rail-m">
                  {button({
                    label: `${name} · ${where}`,
                    current: same(f),
                    onClick: () => openList(st, f),
                    children: (
                      <span className="rb-tile">
                        <Icon name={m.local ? "laptop" : "server"} />
                        <i className={`dotc ${m.local || m.online ? "on" : ""}`} aria-hidden="true" />
                      </span>
                    ),
                  })}
                  {agents.map((a) => {
                    const fa: ListFilter = { kind: "agent", machine: m.machineId, agent: a.name };
                    const w = state(live.filter((s) => s.machineId === m.machineId && s.agent === a.name));
                    return (
                      <div key={a.name}>
                        {button({
                          label: [t("sidebar.agentOn", { agent: codingAgentTitle(a.name), machine: name }), ...w.words].join(" · "),
                          current: same(fa),
                          small: true,
                          onClick: () => openList(st, fa),
                          children: <AgentLogo agent={a.name} size={22} />,
                          marks: marks(w),
                        })}
                      </div>
                    );
                  })}
                </div>
              );
            })}
            {(clients.length > 0 || st.pairings.length > 0) && <div className="rail-sep" role="separator" aria-label={t("sidebar.connectedAgents")} />}
            {st.pairings.map((p) =>
              <div key={p.code}>
                {button({
                  label: `${p.label || t("sidebar.pairing")} · ${t("sidebar.pairingWaits")}`,
                  current: false,
                  onClick: () => st.set({ sheet: { kind: "pairing", code: p.code } }),
                  children: <Icon name="link" />,
                  marks: <i className="rb-dot" aria-hidden="true" />,
                })}
              </div>,
            )}
            {clients.map((c) => {
              const f: ListFilter = { kind: "client", client: c.clientId };
              const w = state(live.filter((s) => s.createdBy === c.clientId));
              return (
                <div key={c.clientId}>
                  {button({
                    label: [c.label, ...w.words].join(" · "),
                    current: same(f),
                    onClick: () => openList(st, f),
                    children: <GrantAvatar c={c} size="md" />,
                    marks: marks(w),
                  })}
                </div>
              );
            })}
          </>
        )}
      </nav>
      <div className="rail-foot">
        <button className="rb" aria-label={t("rail.expand")} onClick={() => setSidebar(st, true)} {...show(`${t("rail.expand")} ${keys(ownKeys.toggleSidebar)}`)}>
          <Icon name="sidebar" />
        </button>
        <AccountMenu compact />
      </div>
      {tip && (
        <div className="rail-tip" role="tooltip" style={{ top: tip.top }}>
          {tip.text}
        </div>
      )}
    </aside>
  );
}
