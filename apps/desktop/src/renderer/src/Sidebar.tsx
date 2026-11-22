import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import {
  bucketTitle, clockText, elapsedSeconds, codingAgentShort, codingAgentTitle, createdDate, dateOf, displayTitle, isActive, isArchivable, isFailed, listTime,
  sessionSections, statusTitle, teamState, updatedDate, type SessionBucket, type SessionSection, type SessionView,
  SCRATCH_PROJECT, isThreadId, threadAsSession,
} from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../shared/i18n";
import { UpdateReminder } from "./update";
import { local, threads as threadApi } from "./api";
import { AppMenuButton } from "./MainWindow";
import { Icon } from "./icons";
import { setSidebar } from "./layout";
import { unreadCount } from "./her/environments";
import { expiringClients } from "./overview/Attention";
import { keys, ownKeys } from "./platform";
import { useNow, useStore, type Store } from "./store";

export function grantLabel(st: Store, s: Session): string {
  return st.clients.find((c) => c.clientId === s.createdBy)?.label ?? i18n.t("common:sidebar.disconnectedGrant");
}

/** Her's own grants: the clients that carry her_machine_id. */
export function herClients(st: Pick<Store, "clients">): string[] {
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

export function machineName(st: Store, id: string): string {
  return st.machines.find((m) => m.machineId === id)?.machineName ?? id;
}

/** Archives or unarchives a v2 thread (UpdateThreadConfig). */
const setThreadArchived = (id: string, archived: boolean) => threadApi.updateThreadConfig({ threadId: id, hasArchived: true, archived });

export function archive(st: Store, s: Session) {
  st.run(() => (isThreadId(s.sessionId) ? setThreadArchived(s.sessionId, true) : local.archiveSession({ sessionId: s.sessionId }))).then((ok) => {
    if (ok) st.show(i18n.t("common:sidebar.archived", { title: displayTitle(s) }), () => unarchive(st, s));
  });
}

export function unarchive(st: Store, s: Session) {
  st.run(() => (isThreadId(s.sessionId) ? setThreadArchived(s.sessionId, false) : local.unarchiveSession({ sessionId: s.sessionId })));
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
      run: () => (isThreadId(s.sessionId) ? threadApi.deleteThread({ threadId: s.sessionId }) : local.deleteSession({ sessionId: s.sessionId })),
      done: i18n.t("common:sessionMenu.deleted", { title }),
    },
  });
}

/**
 * The list as the sidebar shows it, in the owner's view; machines this Mac first, then by name. By status, the
 * running group stays while nothing runs, empty, so it is always there to look at (docs/her-team.md §16).
 */
export function listSections(st: Pick<Store, "sessions" | "threads" | "view" | "archived" | "machines" | "clients">, now: Date): SessionSection[] {
  const machines = [...st.machines].sort((a, b) => Number(b.local) - Number(a.local) || a.machineName.localeCompare(b.machineName)).map((m) => m.machineId);
  const rows = st.threads.length ? [...st.sessions, ...st.threads.map(threadAsSession)] : st.sessions;
  return sessionSections(rows, st.view, { archived: st.archived, now, machines, her: herClients(st), alwaysActive: true });
}

export function sectionTitle(st: Store, sec: SessionSection): string {
  if (sec.grouping === "project") return sec.key === SCRATCH_PROJECT ? i18n.t("thread:launch.scratch") : sec.key;
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
        <button className="tb side-search" onClick={() => st.set({ searching: true })} title={`${t("sidebar.search")} ${keys("CmdOrCtrl+K")}`} aria-label={t("sidebar.search")}>
          <Icon name="search" />
        </button>
      </div>
      {/* Her and the overview, then every session, as the view lays them out (docs/her-team.md §16). */}
      <nav className="side-nav">
        <NewSessionItem />
        {!st.status?.machineOnly && <HerItem />}
        <OverviewItem now={now} />
      </nav>
      <div className="side-h">
        <span>{t("sidebar.sessions")}</span>
        <ListViewMenu />
      </div>
      <Sessions now={now} />
      <UpdateReminder />
      <AccountMenu />
    </aside>
  );
}

/**
 * The Her entry (docs/her-server.md §9.1): a dot while what its assistants sent the owner is unread, over every
 * environment, as Watch brings each machine's Her; "Off" while she runs nowhere. The page opens Her's connections itself.
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
      {!on ? <span className="cnt">{t("sidebar.herOff")}</span> : unread > 0 && <span className="nav-marks"><span className="dot-u" role="img" aria-label={t("sidebar.herUnread", { count: unread })} /></span>}
    </button>
  );
}

/** What the overview's marks say: something needs the owner, and how many sessions run. */
export function overviewMarks(st: Store, now: Date) {
  const team = teamState(st.sessions, st.pairings.length);
  return { waits: team.waiting.length + st.pairings.length + expiringClients(st, now).length, running: team.running.length };
}

/** The overview (⌘1): an orange dot when something needs the owner, a ring while anything runs. */
function OverviewItem({ now }: { now: Date }) {
  const st = useStore();
  const { t } = useTranslation("common");
  const { waits, running } = overviewMarks(st, now);
  return (
    <button className={`nav-i ${st.selection.kind === "overview" ? "sel" : ""}`} onClick={() => st.set({ selection: { kind: "overview" } })}>
      <Icon name="grid" />
      <span>{t("sidebar.overview")}</span>
      <span className="nav-marks">
        {waits > 0 && <span className="dot-w" role="img" aria-label={t("sidebar.attention", { count: waits })} />}
        {running > 0 && <Ring label={t("sidebar.running", { count: running })} />}
        {waits === 0 && running === 0 && <span className="kbd">{keys("CmdOrCtrl+1")}</span>}
      </span>
    </button>
  );
}

/** New Session (⌘N), over Her: the composer, on the machine and project last used (docs/thread-composer.md §4.6). */
function NewSessionItem() {
  const st = useStore();
  const { t } = useTranslation("thread");
  return (
    <button className={`nav-i ${st.selection.kind === "newThread" ? "sel" : ""}`} data-testid="nav-new-thread" onClick={() => st.set({ selection: { kind: "newThread" } })}>
      <Icon name="edit" />
      <span>{t("nav.newThread")}</span>
      <span className="kbd">{keys("CmdOrCtrl+N")}</span>
    </button>
  );
}

/**
 * ＋ on a project's group: a new session in that project, on the machine of
 * its latest session there (projects of one name may be on several machines;
 * the composer can still change it).
 */
function NewInProject({ sec }: { sec: SessionSection }) {
  const st = useStore();
  const { t } = useTranslation("thread");
  const latest = sec.sessions.reduce<Session | undefined>((a, b) => (!a || updatedDate(b) > updatedDate(a) ? b : a), undefined);
  const name = sectionTitle(st, sec);
  return (
    <button
      className="tb grp-add"
      title={t("nav.newIn", { project: name })}
      aria-label={t("nav.newIn", { project: name })}
      onClick={() => st.set({ selection: { kind: "newThread", machineId: latest?.machineId, projectId: sec.key } })}
    >
      <Icon name="plus" />
    </button>
  );
}

/** A small ring that turns: something is at work. */
export function Ring({ label }: { label: string }) {
  return (
    <span className="busy" role="img" aria-label={label}>
      <i aria-hidden="true" />
    </span>
  );
}

/**
 * Every session, as the view lays them out (docs/her-team.md §16): grouped, each group collapsible, older ones
 * loaded as the end comes into view; archived ones on request.
 */
function Sessions({ now }: { now: Date }) {
  const st = useStore();
  const { t } = useTranslation("common");
  const { sessions, threads, view, archived, machines, clients } = st;
  const sections = useMemo(() => listSections({ sessions, threads, view, archived, machines, clients }, now), [sessions, threads, view, archived, machines, clients, now]);
  const total = sections.reduce((n, x) => n + x.sessions.length, 0);
  const list = useRef<HTMLDivElement>(null);
  const end = useRef<HTMLDivElement>(null);
  // The rows get the store as of a click, so a change elsewhere doesn't render them again.
  const latest = useRef(st);
  latest.current = st;
  // Older sessions load as the end of the list comes into view (again after each page, until the list is full or there are no more).
  const { moreSessions, loadOlderSessions } = st;
  useEffect(() => {
    if (!moreSessions || !end.current) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && loadOlderSessions(), { root: list.current, rootMargin: "200px" });
    io.observe(end.current);
    return () => io.disconnect();
  }, [moreSessions, loadOlderSessions, sessions]);
  const sel = st.selection;
  return (
    <div className="side-list" ref={list}>
      <div className="side-sessions" role="list" aria-label={t("sidebar.sessions")}>
        {sections.map((sec) => {
          const id = `${sec.grouping}:${sec.key}`;
          const open = !st.collapsed.includes(id);
          return (
            <div key={id} role="group" aria-label={sectionTitle(st, sec)}>
              <div className="grp-hrow">
                <button
                  className={`grp-h ${open ? "" : "shut"}`}
                  aria-expanded={open}
                  onClick={() => st.set({ collapsed: open ? [...st.collapsed, id] : st.collapsed.filter((c) => c !== id) })}
                >
                  {sectionTitle(st, sec)}
                  <Icon name="caret" className="sm" />
                </button>
                {sec.grouping === "project" && <NewInProject sec={sec} />}
              </div>
              {open && sec.sessions.length === 0 && <div className="side-none">{t("sidebar.noneNow")}</div>}
              {open && sec.sessions.map((s) => (
                <SessionRow
                  key={s.sessionId}
                  s={s}
                  sel={(sel.kind === "session" || sel.kind === "thread") && sel.id === s.sessionId}
                  sender={senderName(st, s.createdBy)}
                  meta={view.metadata}
                  renaming={st.renaming === s.sessionId}
                  store={latest}
                />
              ))}
            </div>
          );
        })}
        {!st.sessionsLoaded ? null : moreSessions ? (
          <div className="endrow" ref={end}>{t("sidebar.loadingOlder")}</div>
        ) : total === 0 ? (
          <div className="side-empty">
            <b>{t("sidebar.emptyTitle")}</b>
            {t(st.status?.machineOnly ? "sidebar.emptyMachine" : "sidebar.emptyBody")}
          </div>
        ) : (
          <div className="endrow">
            {t("sidebar.noOlder")}
            {" · "}
            <button className="link" onClick={() => st.set({ archived: !st.archived })}>{t(st.archived ? "sidebar.hideArchived" : "sidebar.showArchived")}</button>
          </div>
        )}
      </div>
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
            {st.status?.relayConnected ? t("sidebar.connected") : t("sidebar.notConnected")}
          </span>
        </span>
        <Icon name="chevD" className="sm chev" />
        </>}
      </button>
    </div>
  );
}

/**
 * A session in the sidebar (docs/her-team.md §16): the coding agent's logo, the title, and one mark (an orange dot
 * while it waits for the owner, a red one when it failed, a ring while it runs); with metadata, who sent the work
 * to which agent, the project and the time. ⋯ in the mark's place on hover; right-click for the native menu.
 */
export const SessionRow = memo(function SessionRow({ s, sel, sender, meta, renaming, store }: {
  s: Session; sel: boolean; sender: string; meta: boolean; renaming: boolean; store: RefObject<Store>;
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
      <span className="slogo"><AgentLogo agent={s.agent} size={16} /></span>
      <div className="smain">
        <div className="sl1">
          {renaming ? <RenameField s={s} store={store} /> : <span className="stitle" title={displayTitle(s)}>{displayTitle(s)}</span>}
          <span className="smeta"><RowMark s={s} /></span>
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
            <span className="sl2t">{sender} → {codingAgentShort(s.agent)} · {s.projectId}</span>
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
    if (keep && title && title !== displayTitle(s))
      st.run(() => (isThreadId(s.sessionId) ? threadApi.updateThreadConfig({ threadId: s.sessionId, title }) : local.renameSession({ sessionId: s.sessionId, title })));
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

/** A row's one mark, its state in words for screen readers; an idle or archived session has none. */
export function RowMark({ s }: { s: Session }) {
  const { t } = useTranslation("common");
  if (s.status === SessionStatus.WAITING_INPUT) return <span className="dot-w" role="img" aria-label={t("status.waiting")} />;
  if (isFailed(s)) return <span className="dot-r" role="img" aria-label={t(s.status === SessionStatus.CRASHED ? "status.crashed" : "status.failed")} />;
  if (isActive(s)) return <Ring label={t(s.status === SessionStatus.QUEUED ? "status.queued" : s.status === SessionStatus.PREPARING ? "status.preparing" : "status.running")} />;
  // Its machine cannot be read now: say so, there is no mark for it.
  if (s.status === SessionStatus.UNSPECIFIED) return <>{statusTitle(s.status)}</>;
  return null;
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
        <Icon name="sort" />
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
