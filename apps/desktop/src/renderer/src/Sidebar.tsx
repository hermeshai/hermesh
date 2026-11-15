import { memo, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import {
  agoText, bucketTitle, clockText, elapsedSeconds, codingAgentShort, codingAgentTitle, createdDate, dateOf, displayTitle, isActive, isArchivable, isFailed, listTime, needsAttention,
  sessionSections, statusTitle, teamState, updatedDate, type SessionBucket, type SessionSection, type SessionView,
} from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../shared/i18n";
import { UpdateReminder } from "./update";
import { local } from "./api";
import { AppMenuButton } from "./MainWindow";
import { GrantAvatar, StatusDot } from "./components";
import { Icon } from "./icons";
import { listShown, setSidebar } from "./layout";
import { unreadCount } from "./her/environments";
import { expiringClients } from "./overview/Attention";
import { keys, ownKeys } from "./platform";
import { useNow, useStore, type ListFilter, type Store } from "./store";

export function grantLabel(st: Store, s: Session): string {
  return st.clients.find((c) => c.clientId === s.createdBy)?.label ?? i18n.t("common:sidebar.disconnectedGrant");
}

/** Her's own grants: the clients that carry her_machine_id. */
export function herClients(st: Store): string[] {
  return st.clients.filter((c) => c.herMachineId !== "").map((c) => c.clientId);
}

/** Who sent work, in a row (docs/her-team.md §4.7): Her by her name alone, the owner as "You", the others as their grants name them. */
export function senderName(st: Store, clientId: string): string {
  if (clientId === "owner") return i18n.t("common:sidebar.you");
  if (herClients(st).includes(clientId)) return "Her";
  return st.clients.find((c) => c.clientId === clientId)?.label ?? i18n.t("common:sidebar.disconnectedGrant");
}

/** A session's second line in lists: who sent the work to which coding agent, and the project. */
export function senderLine(st: Store, s: Session): string {
  return `${senderName(st, s.createdBy)} → ${codingAgentShort(s.agent)} · ${s.projectId}`;
}

/** Whether a session is one the team's map chose. */
export function inFilter(f: ListFilter | null, s: Session): boolean {
  if (!f) return true;
  if (f.kind === "client") return s.createdBy === f.client;
  return s.machineId === f.machine && (f.kind === "machine" || s.agent === f.agent);
}

export function machineName(st: Store, id: string): string {
  return st.machines.find((m) => m.machineId === id)?.machineName ?? id;
}

export function archive(st: Store, s: Session) {
  st.run(() => local.archiveSession({ sessionId: s.sessionId })).then((ok) => {
    if (ok) st.show(i18n.t("common:sidebar.archived", { title: displayTitle(s) }), () => unarchive(st, s));
  });
}

export function unarchive(st: Store, s: Session) {
  st.run(() => local.unarchiveSession({ sessionId: s.sessionId }));
}

/** Delete, once confirmed: what goes for good, what stays (the branch), and that a turn in progress is interrupted. */
export function confirmDelete(st: Store, s: Session) {
  const title = displayTitle(s);
  const message = [
    i18n.t(s.branch ? "common:sessionMenu.deleteMessage" : "common:sessionMenu.deleteMessageNoBranch", { branch: s.branch }),
    isActive(s) ? i18n.t("common:sessionMenu.deleteRunning") : "",
  ].filter(Boolean).join(" ");
  st.set({
    sheet: {
      kind: "confirm",
      title: i18n.t("common:sessionMenu.deleteTitle", { title }),
      message,
      button: i18n.t("common:sessionMenu.deleteButton"),
      run: () => local.deleteSession({ sessionId: s.sessionId }),
      done: i18n.t("common:sessionMenu.deleted", { title }),
    },
  });
}

/** The list as the sidebar shows it, in the owner's view; machines this Mac first, then by name. */
export function listSections(st: Store, now: Date): SessionSection[] {
  const machines = [...st.machines].sort((a, b) => Number(b.local) - Number(a.local) || a.machineName.localeCompare(b.machineName)).map((m) => m.machineId);
  return sessionSections(st.sessions.filter((s) => inFilter(st.listFilter, s)), st.view, { archived: st.archived, now, machines, her: herClients(st) });
}

export function sectionTitle(st: Store, sec: SessionSection): string {
  if (sec.grouping === "project") return sec.key;
  if (sec.grouping === "agent") return codingAgentTitle(sec.key);
  if (sec.grouping === "machine") return st.machines.find((m) => m.machineId === sec.key)?.local ? i18n.t("overview:thisMac") : machineName(st, sec.key);
  if (sec.grouping === "sender") return sec.key === "owner" ? i18n.t("common:sidebar.sentByYou") : i18n.t("common:sidebar.sentBy", { name: senderName(st, sec.key) });
  return bucketTitle(sec.key as SessionBucket);
}

export function Sidebar() {
  const st = useStore();
  const { t } = useTranslation("common");
  const now = useNow(30_000);
  return (
    <aside className="side" aria-label={t("sidebar.label")}>
      <div className="side-top drag">
        <AppMenuButton />
        <button className="tb" onClick={() => setSidebar(st, false)} title={`${t("window.hideSidebar")} ${keys(ownKeys.toggleSidebar)}`} aria-label={t("window.hideSidebar")}>
          <Icon name="sidebar" />
        </button>
      </div>
      {/* Navigation only (docs/her-team.md §14.2): the pages, then the team, whose rows open the session list. */}
      <nav className="side-nav">
        {!st.status?.machineOnly && <HerItem />}
        <WorkItem now={now} />
        <button className="nav-i" onClick={() => st.set({ searching: true })}>
          <Icon name="search" />
          <span>{t("sidebar.search")}</span>
          <span className="kbd">{keys("CmdOrCtrl+K")}</span>
        </button>
        {st.status?.machineOnly && <AllSessionsItem className="nav-i" />}
      </nav>
      <div className="side-list">{!st.status?.machineOnly && <TeamMap />}</div>
      <UpdateReminder />
      <AccountMenu />
    </aside>
  );
}

/** The list's column, showing what the filter chose; a session it shows stays chosen, another one is let go. */
export function openList(st: Store, f: ListFilter | null) {
  const sel = st.selection;
  const s = sel.kind === "session" ? st.sessions.find((x) => x.sessionId === sel.id) : undefined;
  st.set({ listFilter: f, selection: s && inFilter(f, s) ? sel : { kind: "list" } });
}

/** All Sessions: the list's column, unfiltered, with how many there are (archived ones aside); the team's first row. */
function AllSessionsItem({ className }: { className: string }) {
  const st = useStore();
  const { t } = useTranslation("common");
  const count = st.sessions.filter((s) => s.status !== SessionStatus.ARCHIVED).length;
  const sel = listShown(st.selection) && st.listFilter === null;
  return (
    <button className={`${className} ${sel ? "sel" : ""}`} aria-current={sel || undefined} onClick={() => openList(st, null)}>
      <Icon name="menu" />
      <span>{t("sidebar.allSessions")}</span>
      <span className="cnt">{count}</span>
    </button>
  );
}

/**
 * The Her entry (docs/her-server.md §9.1): what its assistants sent the owner and they have not read, over
 * every environment, as Watch brings each machine's Her; the page opens Her's connections itself. "Off" while
 * she runs nowhere (docs/her-team.md §14.2).
 */
function HerItem() {
  const st = useStore();
  const { t } = useTranslation("common");
  const unread = unreadCount(st.machines);
  const sel = st.selection.kind === "her" || st.selection.kind === "progress";
  const on = st.machines.some((m) => m.her?.enabled);
  return (
    <button className={`nav-i ${sel ? "sel" : ""}`} onClick={() => st.selection.kind !== "her" && st.set({ selection: { kind: "her" } })}>
      <Icon name="her" />
      <span>{t("sidebar.her")}</span>
      {!on ? <span className="cnt">{t("sidebar.herOff")}</span> : unread > 0 && <span className="cnt" aria-label={t("sidebar.herUnread", { count: unread })}>{unread}</span>}
    </button>
  );
}

/**
 * What Her sent out and how it stands (docs/her-team.md §15), for the Progress button on her page: her coding
 * sessions, as Watch brings them, and her sub-agents while a page of hers follows her conversation.
 */
export function progressMarks(st: Store) {
  const her = herClients(st);
  const mine = st.sessions.filter((s) => her.includes(s.createdBy) && s.status !== SessionStatus.ARCHIVED);
  return {
    waits: mine.filter((s) => s.status === SessionStatus.WAITING_INPUT).length + (st.herAgents?.waits ?? 0),
    running: mine.filter(isActive).length + (st.herAgents?.running ?? 0),
  };
}

/** Work (the overview, docs/her-team.md §14.2): an orange dot when something needs the owner, and what runs. */
export function workMarks(st: Store, now: Date) {
  const team = teamState(st.sessions, st.pairings.length);
  return { waits: team.waiting.length + st.pairings.length + expiringClients(st, now).length, running: team.running.length };
}

function WorkItem({ now }: { now: Date }) {
  const st = useStore();
  const { t } = useTranslation("common");
  const { waits, running } = workMarks(st, now);
  return (
    <button className={`nav-i ${st.selection.kind === "overview" ? "sel" : ""}`} onClick={() => st.set({ selection: { kind: "overview" } })}>
      <Icon name="grid" />
      <span>{t("sidebar.work")}</span>
      <span className="nav-marks">
        {waits > 0 && <span className="dot-w" role="img" aria-label={t("sidebar.attention", { count: waits })} />}
        {running > 0 && <Busy count={running} label={t("sidebar.running", { count: running })} />}
        {waits === 0 && running === 0 && <span className="kbd">{keys("CmdOrCtrl+1")}</span>}
      </span>
    </button>
  );
}

/** A small ring that turns, and how many run. */
function Busy({ count, label }: { count: number; label: string }) {
  return (
    <span className="busy" role="img" aria-label={label}>
      <i aria-hidden="true" />
      <span aria-hidden="true">{count}</span>
    </span>
  );
}

/** What a filter of the list is called, at the head of its column. */
export function filterName(st: Store, f: ListFilter): string {
  if (f.kind === "client") return senderName(st, f.client);
  const m = st.machines.find((x) => x.machineId === f.machine)?.local ? i18n.t("common:sidebar.thisMac") : machineName(st, f.machine);
  return f.kind === "machine" ? m : i18n.t("common:sidebar.agentOn", { agent: codingAgentShort(f.agent), machine: m });
}

/**
 * The team's map (docs/her-team.md §14.2): the machines, this Mac first, each with its coding agents; the agents
 * connected to the owner, Her apart; requests to connect that wait. A row narrows the list below to its sessions;
 * the heading folds the map.
 */
function TeamMap() {
  const st = useStore();
  const { t } = useTranslation("common");
  const now = useNow(60_000);
  const open = !st.collapsed.includes("team");
  const live = st.sessions.filter((s) => s.status !== SessionStatus.ARCHIVED);
  const machines = [...st.machines].sort((a, b) => Number(b.local) - Number(a.local) || a.machineName.localeCompare(b.machineName));
  const her = herClients(st);
  const clients = st.clients.filter((c) => !c.revoked && !her.includes(c.clientId));
  const same = (f: ListFilter) => listShown(st.selection) && JSON.stringify(st.listFilter) === JSON.stringify(f);
  const pick = (f: ListFilter) => openList(st, f);
  const marks = (mine: Session[]) => {
    const waiting = mine.some((s) => s.status === SessionStatus.WAITING_INPUT);
    const running = mine.filter(isActive).length;
    return (
      <span className="map-marks">
        {waiting && <span className="dot-w" role="img" aria-label={t("sidebar.waits")} />}
        {running > 0 && <Busy count={running} label={t("sidebar.running", { count: running })} />}
      </span>
    );
  };
  return (
    <div role="group" aria-label={t("sidebar.team")} className="team-map">
      <button className={`grp-h ${open ? "" : "shut"}`} aria-expanded={open} onClick={() => st.set({ collapsed: open ? [...st.collapsed, "team"] : st.collapsed.filter((c) => c !== "team") })}>
        {t("sidebar.team")}
        <Icon name="caret" className="sm" />
      </button>
      <AllSessionsItem className="map-row" />
      {open && (
        <>
          {machines.map((m) => {
            const f: ListFilter = { kind: "machine", machine: m.machineId };
            const seen = dateOf(m.lastSeen);
            const where = m.local ? t("sidebar.thisMac") : m.machineName;
            const sub = !(m.local || m.online) ? (seen ? t("sidebar.offlineSince", { ago: agoText(seen, now) }) : t("sidebar.offline")) : m.her?.enabled ? t("sidebar.herHere") : "";
            const agents = (m.status?.agents ?? []).filter((a) => a.available);
            return (
              <div key={m.machineId} className="map-m">
                <button className={`map-row map-mach ${m.local || m.online ? "" : "off"} ${same(f) ? "sel" : ""}`} aria-current={same(f) || undefined} onClick={() => pick(f)}>
                  <span className="map-n">{where}</span>
                  {sub && <span className="map-sub">{sub}</span>}
                  {marks(live.filter((s) => s.machineId === m.machineId && !agents.some((a) => a.name === s.agent)))}
                  <Icon name="chevR" className="sm map-go" />
                </button>
                {agents.map((a) => {
                  const fa: ListFilter = { kind: "agent", machine: m.machineId, agent: a.name };
                  return (
                    <button key={a.name} className={`map-row ${same(fa) ? "sel" : ""}`} aria-current={same(fa) || undefined} onClick={() => pick(fa)}>
                      <AgentLogo agent={a.name} size={16} />
                      <span className="map-n">{codingAgentTitle(a.name)}</span>
                      {marks(live.filter((s) => s.machineId === m.machineId && s.agent === a.name))}
                    </button>
                  );
                })}
              </div>
            );
          })}
          {(clients.length > 0 || st.pairings.length > 0) && <div className="map-h">{t("sidebar.connectedAgents")}</div>}
          {st.pairings.map((p) => (
            <button key={p.code} className="map-row" onClick={() => st.set({ sheet: { kind: "pairing", code: p.code } })}>
              <Icon name="link" />
              <span className="map-n">{p.label || t("sidebar.pairing")}</span>
              <span className="map-marks"><span className="dot-w" role="img" aria-label={t("sidebar.pairingWaits")} /></span>
            </button>
          ))}
          {clients.map((c) => {
            const f: ListFilter = { kind: "client", client: c.clientId };
            return (
              <button key={c.clientId} className={`map-row ${same(f) ? "sel" : ""}`} aria-current={same(f) || undefined} onClick={() => pick(f)}>
                <GrantAvatar c={c} size="sm" />
                <span className="map-n">{c.label}</span>
                {marks(live.filter((s) => s.createdBy === c.clientId))}
              </button>
            );
          })}
        </>
      )}
    </div>
  );
}

type AccountItem = { id: "settings" | "account" | "newGrant" | "addMachine" | "enterCode"; icon: string; shortcut?: string } | null;

/**
 * The account menu, from the avatar at the sidebar's foot: it opens upward, as wide as the foot, with what adds
 * something and the settings. compact: the rail's foot, the avatar alone; the menu then opens beside the rail.
 */
export function AccountMenu({ compact = false }: { compact?: boolean }) {
  const st = useStore();
  const { t } = useTranslation("common");
  // "key": opened from the keyboard, so the first item is chosen at once, as in a native menu.
  const [open, setOpen] = useState<false | "mouse" | "key">(false);
  const ref = useRef<HTMLDivElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const foot = useRef<HTMLButtonElement>(null);
  const online = st.machines.filter((m) => m.online).length;
  // A machine only adds nothing to the owner's (null: a separator).
  const items: AccountItem[] = st.status?.machineOnly
    ? [
        { id: "settings", icon: "gear", shortcut: keys("CmdOrCtrl+,") },
        { id: "account", icon: "user" },
      ]
    : [
        { id: "newGrant", icon: "key", shortcut: keys("Shift+CmdOrCtrl+N") },
        { id: "addMachine", icon: "laptop" },
        { id: "enterCode", icon: "link", shortcut: keys("Shift+CmdOrCtrl+P") },
        null,
        { id: "settings", icon: "gear", shortcut: keys("CmdOrCtrl+,") },
        { id: "account", icon: "user" },
      ];
  useEffect(() => {
    if (!open) return;
    (open === "key" ? menu.current?.querySelector<HTMLElement>("[role=menuitem]") : menu.current)?.focus();
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const blur = () => setOpen(false);
    document.addEventListener("mousedown", close);
    window.addEventListener("blur", blur);
    return () => {
      document.removeEventListener("mousedown", close);
      window.removeEventListener("blur", blur);
    };
  }, [open]);
  const choose = (id: NonNullable<AccountItem>["id"]) => {
    setOpen(false);
    if (id === "settings") window.hermesh?.openSettings();
    else if (id === "account") window.hermesh?.openSettings("account");
    else st.set({ sheet: { kind: id } });
  };
  // The keyboard, as in a native menu: arrows move, Esc closes back to the avatar.
  const onKeyDown = (e: ReactKeyboardEvent) => {
    const all = [...(menu.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
    const i = all.indexOf(document.activeElement as HTMLElement);
    const go = { ArrowDown: (i + 1) % all.length, ArrowUp: i <= 0 ? all.length - 1 : i - 1, Home: 0, End: all.length - 1 }[e.key];
    if (go !== undefined) {
      e.preventDefault();
      all[go]?.focus();
    } else if (e.key === "Escape" || e.key === "Tab") {
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      foot.current?.focus();
    }
  };
  return (
    <div className={`acct ${compact ? "compact" : ""}`} ref={ref}>
      {open && (
        <div className="acct-menu" role="menu" aria-label={t("sidebar.account")} tabIndex={-1} ref={menu} onKeyDown={onKeyDown} onMouseLeave={() => menu.current?.focus()}>
          {items.map((it, i) =>
            it ? (
              <button key={it.id} className="mi" role="menuitem" tabIndex={-1} onMouseEnter={(e) => e.currentTarget.focus()} onClick={() => choose(it.id)}>
                <Icon name={it.icon} />
                <span>{t(`accountMenu.${it.id}`)}</span>
                {it.shortcut && <span className="kbd">{it.shortcut}</span>}
              </button>
            ) : (
              <div key={i} className="msep" role="separator" />
            ),
          )}
        </div>
      )}
      <button className="side-foot" ref={foot} aria-label={t("sidebar.account")} title={compact ? t("sidebar.account") : undefined} aria-haspopup="menu" aria-expanded={!!open} onClick={(e) => setOpen(open ? false : e.detail ? "mouse" : "key")}>
        <span className="av" aria-hidden="true">
          {(window.hermesh?.userName ?? "H").slice(0, 1).toUpperCase()}
        </span>
        {/* The rail has room for the avatar alone; a dot on it when not connected. */}
        {compact ? (!st.status?.relayConnected && <i className="dotc" aria-hidden="true" />) : <>
        <span className="who">
          <b>{window.hermesh?.userName ?? "Hermesh"}</b>
          <span>
            {/* A dot only when it is worth noticing: not connected. */}
            {!st.status?.relayConnected && <i className="dotc" />}
            {st.status?.relayConnected ? t("sidebar.connected", { count: online }) : t("sidebar.notConnected")}
          </span>
        </span>
        <Icon name="chevD" className="sm chev" />
        </>}
      </button>
    </div>
  );
}

export const SessionRow = memo(function SessionRow({ s, sel, grant, meta, renaming, store }: {
  s: Session; sel: boolean; grant: string; meta: boolean; renaming: boolean; store: RefObject<Store>;
}) {
  const { t } = useTranslation("common");
  const archived = s.status === SessionStatus.ARCHIVED;
  // Where the ⋯ menu opens (under its button), while it is open.
  const [menu, setMenu] = useState<{ x: number; y: number; key: boolean } | null>(null);
  return (
    <div
      className={`srow has-act ${sel ? "sel" : ""} ${menu ? "menu-open" : ""} ${archived ? "arch" : ""}`}
      role="listitem"
      aria-current={sel || undefined}
      tabIndex={0}
      onClick={() => !renaming && store.current.select(s.sessionId)}
      onKeyDown={(e) => !renaming && e.key === "Enter" && store.current.select(s.sessionId)}
      onContextMenu={(e) => {
        e.preventDefault();
        sessionMenu(store.current, s);
      }}
    >
      <div className="smain">
        <div className="sl1">
          {/* A running session's title shimmers; waiting and failed ones have a dot instead, an idle or archived one neither. */}
          {renaming ? (
            <RenameField s={s} store={store} />
          ) : (
            <span className={`stitle ${isActive(s) && !needsAttention(s) ? "run" : ""}`} title={displayTitle(s)}>
              {displayTitle(s)}
              {isActive(s) && !needsAttention(s) && (
                <>
                  <span className="shine" data-t={displayTitle(s)} aria-hidden="true" />
                  <span className="shine band" data-t={displayTitle(s)} aria-hidden="true" />
                </>
              )}
            </span>
          )}
          <span className="smeta">
            {needsAttention(s) && <StatusDot s={s} />}
            <RowStatus s={s} />
          </span>
          <button
            className="rowact"
            title={t("sessionMenu.more")}
            aria-label={t("sessionMenu.more")}
            aria-haspopup="menu"
            aria-expanded={!!menu}
            onClick={(e) => {
              e.stopPropagation();
              const r = e.currentTarget.getBoundingClientRect();
              setMenu(menu ? null : { x: r.right, y: r.bottom + 4, key: e.detail === 0 });
            }}
          >
            <Icon name="more" className="sm" />
          </button>
          {menu && <RowMenu s={s} at={menu} store={store} onClose={() => setMenu(null)} />}
        </div>
        {meta && (
          <div className="sl2">
            <span className="sl2t">{grant} → {codingAgentShort(s.agent)} · {s.projectId}</span>
            <RowTime s={s} />
          </div>
        )}
      </div>
    </div>
  );
});

/** The session's context menu (native): open, rename, archive, copy its id, delete. */
async function sessionMenu(st: Store, s: Session) {
  const archived = s.status === SessionStatus.ARCHIVED;
  const id = await window.hermesh?.popupMenu([
    { id: "open", label: i18n.t("common:sessionMenu.open") },
    { separator: true },
    { id: "rename", label: i18n.t("common:sessionMenu.rename") },
    { id: archived ? "unarchive" : "archive", label: i18n.t(archived ? "common:sessionMenu.unarchive" : "common:sessionMenu.archive"), enabled: archived || isArchivable(s) },
    { separator: true },
    { id: "copy", label: i18n.t("common:sessionMenu.copyId") },
    { separator: true },
    { id: "delete", label: i18n.t("common:sessionMenu.delete") },
  ]);
  switch (id) {
    case "open":
      return st.select(s.sessionId);
    case "rename":
      return st.set({ renaming: s.sessionId });
    case "archive":
      return archive(st, s);
    case "unarchive":
      return unarchive(st, s);
    case "delete":
      return confirmDelete(st, s);
    case "copy":
      return navigator.clipboard.writeText(s.sessionId);
  }
}

/**
 * A row's ⋯ menu: rename, archive (or unarchive), and delete after a
 * separator. Drawn like the view menu, right-aligned under its button;
 * clicks stay in the menu, not the row.
 */
function RowMenu({ s, at, store, onClose }: { s: Session; at: { x: number; y: number; key: boolean }; store: RefObject<Store>; onClose: () => void }) {
  const { t } = useTranslation("common");
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: -9999, top: at.y });
  const archived = s.status === SessionStatus.ARCHIVED;
  const items: ({ id: string; label: string; run: () => void; disabled?: boolean; danger?: boolean } | null)[] = [
    { id: "rename", label: t("sessionMenu.rename"), run: () => store.current.set({ renaming: s.sessionId }) },
    archived
      ? { id: "unarchive", label: t("sessionMenu.unarchive"), run: () => unarchive(store.current, s) }
      : { id: "archive", label: t("sessionMenu.archive"), run: () => archive(store.current, s), disabled: !isArchivable(s) },
    null,
    { id: "delete", label: t("sessionMenu.delete"), run: () => confirmDelete(store.current, s), danger: true },
  ];
  useLayoutEffect(() => {
    const el = ref.current!;
    const top = at.y + el.offsetHeight > innerHeight - 8 ? at.y - el.offsetHeight - 30 : at.y;
    setPos({ left: Math.max(8, at.x - el.offsetWidth), top });
    (at.key ? el.querySelector<HTMLElement>("[role=menuitem]:not(:disabled)") : el)?.focus();
  }, [at]);
  useEffect(() => {
    // Its own button toggles it (on click), so a press there isn't outside.
    const outside = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && !(e.target as Element).closest?.(".rowact[aria-expanded=true]") && onClose();
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", onClose);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", onClose);
    };
  }, [onClose]);
  const onKeyDown = (e: ReactKeyboardEvent) => {
    e.stopPropagation();
    const all = [...(ref.current?.querySelectorAll<HTMLElement>("[role=menuitem]:not(:disabled)") ?? [])];
    const i = all.indexOf(document.activeElement as HTMLElement);
    const go = { ArrowDown: (i + 1) % all.length, ArrowUp: i <= 0 ? all.length - 1 : i - 1 }[e.key];
    if (go !== undefined) {
      e.preventDefault();
      all[go]?.focus();
    } else if (e.key === "Escape" || e.key === "Tab") {
      e.preventDefault();
      onClose();
    }
  };
  return (
    <div className="vmenu" role="menu" aria-label={t("sessionMenu.more")} tabIndex={-1} ref={ref} style={pos} onClick={(e) => e.stopPropagation()} onKeyDown={onKeyDown} onContextMenu={(e) => e.stopPropagation()}>
      {items.map((it, i) =>
        it ? (
          <button
            key={it.id}
            className={`mi ${it.danger ? "danger" : ""}`}
            role="menuitem"
            tabIndex={-1}
            disabled={it.disabled}
            onMouseMove={(e) => e.currentTarget.focus()}
            onClick={() => (onClose(), it.run())}
          >
            {it.label}
          </button>
        ) : (
          <div key={i} className="msep" role="separator" />
        ),
      )}
    </div>
  );
}

/** A row's title, edited in place: ↩ or leaving the field keeps it, Esc puts it back. */
function RenameField({ s, store }: { s: Session; store: RefObject<Store> }) {
  const { t } = useTranslation("common");
  const [value, setValue] = useState(displayTitle(s));
  const done = useRef(false);
  const finish = (keep: boolean) => {
    if (done.current) return;
    done.current = true;
    const st = store.current;
    st.set({ renaming: null });
    const title = value.trim();
    if (keep && title && title !== displayTitle(s)) st.run(() => local.renameSession({ sessionId: s.sessionId, title }));
  };
  return (
    <input
      className="srename"
      aria-label={t("sessionMenu.renameLabel")}
      autoFocus
      value={value}
      onFocus={(e) => e.currentTarget.select()}
      onChange={(e) => setValue(e.target.value)}
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.nativeEvent.isComposing) return;
        if (e.key === "Enter") finish(true);
        else if (e.key === "Escape") finish(false);
      }}
      onBlur={() => finish(true)}
    />
  );
}

/** The status in words, beside its dot: only what needs saying (a running session shimmers, an idle one says nothing). */
function RowStatus({ s }: { s: Session }) {
  const { t } = useTranslation("common");
  if (s.status !== SessionStatus.CRASHED && s.status !== SessionStatus.WAITING_INPUT && isFailed(s)) return <>{t("status.failed")}</>;
  switch (s.status) {
    case SessionStatus.WAITING_INPUT:
      return <>{t("status.waiting")}</>;
    case SessionStatus.CRASHED:
      return <>{t("status.crashed")}</>;
    case SessionStatus.PREPARING:
      return <>{t("status.preparing")}</>;
    case SessionStatus.QUEUED:
      return <>{t("status.queued")}</>;
    case SessionStatus.UNSPECIFIED: // its machine cannot be read now
      return <>{statusTitle(s.status)}</>;
    default:
      return null;
  }
}

/** The second line's right end, under the status: how long a running turn has run, else when the session last changed. */
function RowTime({ s }: { s: Session }) {
  const running = s.status === SessionStatus.RUNNING;
  const now = useNow(running ? 1000 : 30_000);
  const text = running ? clockText(elapsedSeconds(dateOf(s.progress?.turnStartedAt) ?? createdDate(s), now)) : listTime(updatedDate(s), now);
  return <span className="stime">{text}</span>;
}

type ViewPane = "group" | "sort" | "show";
type ViewChoice = { label: string; on: boolean; role: "menuitemradio" | "menuitemcheckbox"; patch: Partial<SessionView> } | null;

/**
 * The list's view menu, by "Sessions": grouping, sorting by last activity
 * and whether rows show their metadata, each a submenu. Drawn like the
 * account menu but opaque, over the list; the keyboard works as in a native
 * menu (→ opens a submenu, ← and Esc go back). Rows follow the mouse only as
 * it moves, so a menu opening under a resting pointer keeps the keyboard's choice.
 */
export function ListViewMenu() {
  const st = useStore();
  const { t } = useTranslation("common");
  const v = st.view;
  const [open, setOpen] = useState<false | "mouse" | "key">(false);
  const [pane, setPane] = useState<{ id: ViewPane; key: boolean } | null>(null);
  const [at, setAt] = useState({ x: 0, y: 0, sx: 0, sy: 0 });
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const sub = useRef<HTMLDivElement>(null);
  const close = (back: boolean) => {
    setOpen(false);
    setPane(null);
    if (back) btn.current?.focus();
  };
  useEffect(() => {
    if (!open) return;
    (open === "key" ? menu.current?.querySelector<HTMLElement>("[role=menuitem]") : menu.current)?.focus();
    const outside = (e: MouseEvent) => ![btn, menu, sub].some((r) => r.current?.contains(e.target as Node)) && close(false);
    const blur = () => close(false);
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", blur);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", blur);
    };
  }, [open]);
  // The submenu beside its row, on the left when there's no room on the right; from the keyboard its first item takes focus.
  useLayoutEffect(() => {
    if (!pane || !menu.current || !sub.current) return;
    const m = menu.current.getBoundingClientRect();
    const r = menu.current.querySelector(`[data-pane=${pane.id}]`)!.getBoundingClientRect();
    const w = sub.current.offsetWidth;
    setAt((a) => ({ ...a, sx: m.right + w + 4 > innerWidth ? m.left - w - 2 : m.right + 2, sy: r.top - 5 }));
    if (pane.key) sub.current.querySelector<HTMLElement>("[role^=menuitem]")?.focus();
  }, [pane]);
  const toggle = (e: { detail: number }) => {
    if (open) return close(false);
    const r = btn.current!.getBoundingClientRect();
    // Right-aligned to the button, so it opens leftward, over the sidebar.
    setAt({ x: innerWidth - r.right, y: r.bottom + 4, sx: 0, sy: 0 });
    setOpen(e.detail ? "mouse" : "key");
  };
  const choose = (patch: Partial<SessionView>) => {
    st.set({ view: { ...v, ...patch }, viewChosen: true });
    close(true);
  };
  const groups = { sender: t("listView.bySender"), status: t("listView.byStatus"), date: t("listView.byDate"), project: t("listView.byProject"), agent: t("listView.byAgent"), machine: t("listView.byMachine") };
  const panes: { id: ViewPane; label: string; value: string }[] = [
    { id: "group", label: t("listView.group"), value: groups[v.group] },
    { id: "sort", label: t("listView.sort"), value: t(v.newestFirst ? "listView.newestFirst" : "listView.oldestFirst") },
    { id: "show", label: t("listView.show"), value: t(v.metadata ? "listView.metadata" : "listView.titleOnly") },
  ];
  const choices: Record<ViewPane, ViewChoice[]> = {
    group: (Object.keys(groups) as SessionView["group"][]).map((g) => ({ label: groups[g], on: v.group === g, role: "menuitemradio", patch: { group: g } })),
    sort: [
      { label: t("listView.lastActive"), on: true, role: "menuitemradio", patch: {} },
      null,
      { label: t("listView.newestFirst"), on: v.newestFirst, role: "menuitemradio", patch: { newestFirst: true } },
      { label: t("listView.oldestFirst"), on: !v.newestFirst, role: "menuitemradio", patch: { newestFirst: false } },
    ],
    show: [{ label: t("listView.metadata"), on: v.metadata, role: "menuitemcheckbox", patch: { metadata: !v.metadata } }],
  };
  const move = (e: ReactKeyboardEvent, box: HTMLElement | null) => {
    const all = [...(box?.querySelectorAll<HTMLElement>("[role^=menuitem]") ?? [])];
    const i = all.indexOf(document.activeElement as HTMLElement);
    const go = { ArrowDown: (i + 1) % all.length, ArrowUp: i <= 0 ? all.length - 1 : i - 1, Home: 0, End: all.length - 1 }[e.key];
    if (go === undefined) return false;
    e.preventDefault();
    all[go]?.focus();
    return true;
  };
  const onMenuKey = (e: ReactKeyboardEvent) => {
    if (move(e, menu.current)) return setPane(null);
    const id = (document.activeElement as HTMLElement | null)?.dataset.pane as ViewPane | undefined;
    if (id && (e.key === "ArrowRight" || e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      setPane({ id, key: true });
    } else if (e.key === "Escape" || e.key === "Tab") {
      e.preventDefault();
      e.stopPropagation();
      close(true);
    }
  };
  const onSubKey = (e: ReactKeyboardEvent) => {
    e.stopPropagation();
    if (move(e, sub.current)) return;
    if (e.key === "ArrowLeft" || e.key === "Escape") {
      e.preventDefault();
      const id = pane?.id;
      setPane(null);
      menu.current?.querySelector<HTMLElement>(`[data-pane=${id}]`)?.focus();
    } else if (e.key === "Tab") {
      e.preventDefault();
      close(true);
    }
  };
  return (
    <>
      <button ref={btn} className={`tb ${open ? "on" : ""}`} title={t("listView.title")} aria-label={t("listView.title")} aria-haspopup="menu" aria-expanded={!!open} onClick={toggle}>
        <Icon name="filter" />
      </button>
      {open && (
        <div className="vmenu" role="menu" aria-label={t("listView.title")} tabIndex={-1} ref={menu} style={{ right: at.x, top: at.y }} onKeyDown={onMenuKey}>
          {panes.map((p) => (
            <button
              key={p.id}
              className={`mi ${pane?.id === p.id ? "open" : ""}`}
              role="menuitem"
              aria-haspopup="menu"
              aria-expanded={pane?.id === p.id}
              tabIndex={-1}
              data-pane={p.id}
              onMouseMove={(e) => document.activeElement !== e.currentTarget && (e.currentTarget.focus(), setPane({ id: p.id, key: false }))}
              onClick={() => setPane({ id: p.id, key: false })}
            >
              <span>{p.label}</span>
              <span className="val">{p.value}</span>
              <Icon name="chevR" className="sm" />
            </button>
          ))}
        </div>
      )}
      {open && pane && (
        <div className="vmenu" role="menu" aria-label={panes.find((p) => p.id === pane.id)!.label} tabIndex={-1} ref={sub} style={{ left: at.sx, top: at.sy }} onKeyDown={onSubKey}>
          {choices[pane.id].map((c, i) =>
            c ? (
              <button key={c.label} className="mi" role={c.role} aria-checked={c.on} tabIndex={-1} onMouseMove={(e) => e.currentTarget.focus()} onClick={() => choose(c.patch)}>
                <span className="ck">{c.on && <Icon name="check" className="sm" />}</span>
                <span>{c.label}</span>
              </button>
            ) : (
              <div key={i} className="msep" role="separator" />
            ),
          )}
        </div>
      )}
    </>
  );
}
