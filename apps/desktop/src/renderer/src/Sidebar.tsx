import { memo, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from "react";
import { Trans, useTranslation } from "react-i18next";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import {
  bucketTitle, clockText, codingAgentShort, codingAgentTitle, createdDate, dateOf, displayTitle, isActive, isArchivable, isFailed, listTime,
  sectionCounted, sessionSections, updatedDate, type SessionBucket, type SessionSection, type SessionView,
} from "@hermesh/core";
import { i18n } from "../../shared/i18n";
import { UpdateReminder } from "./update";
import { local } from "./api";
import { StatusDot } from "./components";
import { Icon } from "./icons";
import { expiringClients } from "./overview/Attention";
import { useNow, useStore, type Store } from "./store";

export function grantLabel(st: Store, s: Session): string {
  return st.clients.find((c) => c.clientId === s.createdBy)?.label ?? i18n.t("common:sidebar.disconnectedGrant");
}

export function machineName(st: Store, id: string): string {
  return st.machines.find((m) => m.machineId === id)?.machineName ?? id;
}

/** What the overview's "Needs You" lists: pairing requests and grants about to expire (sessions count in the list). */
export function attentionCount(st: Store, now: Date): number {
  return st.pairings.length + expiringClients(st, now).length;
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
  return sessionSections(st.sessions, st.view, { archived: st.archived, now, machines });
}

function sectionTitle(st: Store, sec: SessionSection): string {
  if (sec.grouping === "agent") return codingAgentTitle(sec.key);
  if (sec.grouping === "machine") return st.machines.find((m) => m.machineId === sec.key)?.local ? i18n.t("overview:thisMac") : machineName(st, sec.key);
  return bucketTitle(sec.key as SessionBucket);
}

export function Sidebar() {
  const st = useStore();
  const { t } = useTranslation("common");
  const now = useNow(30_000);
  const sections = listSections(st, now);
  const count = attentionCount(st, now);
  const total = sections.reduce((n, x) => n + x.sessions.length, 0);
  const list = useRef<HTMLDivElement>(null);
  const end = useRef<HTMLDivElement>(null);
  // The rows get the store as of a click, so a change elsewhere doesn't render them again.
  const latest = useRef(st);
  latest.current = st;
  // Older sessions load as the end of the list comes into view (again
  // after each page, until the list is full or there are no more).
  const { moreSessions, loadOlderSessions } = st;
  useEffect(() => {
    if (!moreSessions || !end.current) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && loadOlderSessions(), { root: list.current, rootMargin: "200px" });
    io.observe(end.current);
    return () => io.disconnect();
  }, [moreSessions, loadOlderSessions, st.sessions]);
  return (
    <aside className="side" aria-label={t("sidebar.label")}>
      <div className="side-top drag">
        <button className="tb" onClick={() => st.set({ sidebar: false })} title={`${t("window.hideSidebar")} ⌃⌘S`} aria-label={t("window.hideSidebar")}>
          <Icon name="sidebar" />
        </button>
      </div>
      <nav className="side-nav">
        <button className={`nav-i ${st.selection.kind === "overview" ? "sel" : ""}`} onClick={() => st.set({ selection: { kind: "overview" } })}>
          <Icon name="grid" />
          <span>{t("sidebar.overview")}</span>
          {count ? <span className="cnt" aria-label={t("sidebar.attention", { count })}>{count}</span> : <span className="kbd">⌘1</span>}
        </button>
        <button className="nav-i" onClick={() => st.set({ searching: true })}>
          <Icon name="search" />
          <span>{t("sidebar.search")}</span>
          <span className="kbd">⌘K</span>
        </button>
      </nav>
      <div className="side-head">
        <div className="lh">
          <span>{t("sidebar.sessions")}</span>
          <ListViewMenu />
        </div>
      </div>
      <div className="side-list" role="list" aria-label={t("sidebar.sessions")} ref={list}>
        {sections.map((sec) => {
          const id = `${sec.grouping}:${sec.key}`;
          const open = !st.collapsed.includes(id);
          return (
          <div key={id} role="group" aria-label={sectionTitle(st, sec)}>
            <button
              className={`grp-h ${open ? "" : "shut"}`}
              aria-expanded={open}
              onClick={() => st.set({ collapsed: open ? [...st.collapsed, id] : st.collapsed.filter((c) => c !== id) })}
            >
              {sectionTitle(st, sec)}
              {sectionCounted(sec) && <span className="n">{sec.sessions.length}</span>}
              <Icon name="chevD" className="sm" />
            </button>
            {open && sec.sessions.map((s) => (
              <SessionRow
                key={s.sessionId}
                s={s}
                sel={st.selection.kind === "session" && st.selection.id === s.sessionId}
                grant={grantLabel(st, s)}
                meta={st.view.metadata}
                renaming={st.renaming === s.sessionId}
                store={latest}
              />
            ))}
          </div>
          );
        })}
        {!st.sessionsLoaded ? null : st.moreSessions ? (
          <div className="endrow" ref={end}>{t("sidebar.loadingOlder")}</div>
        ) : total === 0 ? (
          <div className="endrow" style={{ paddingTop: 30 }}>
            <Trans t={t} i18nKey="sidebar.empty" components={{ br: <br /> }} />
          </div>
        ) : (
          <div className="endrow">
            {t("sidebar.noOlder")}
            {" · "}
            <button className="link" onClick={() => st.set({ archived: !st.archived })}>{t(st.archived ? "sidebar.hideArchived" : "sidebar.showArchived")}</button>
          </div>
        )}
      </div>
      <UpdateReminder />
      <AccountMenu />
    </aside>
  );
}

type AccountItem = { id: "settings" | "account" | "newGrant" | "addMachine" | "enterCode"; icon: string; shortcut?: string } | null;

/** The account menu, from the avatar at the sidebar's foot: it opens upward, as wide as the foot, with what adds something and the settings. */
function AccountMenu() {
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
    ? [{ id: "settings", icon: "gear", shortcut: "⌘," }]
    : [
        { id: "newGrant", icon: "key", shortcut: "⇧⌘N" },
        { id: "addMachine", icon: "laptop" },
        { id: "enterCode", icon: "link", shortcut: "⇧⌘P" },
        null,
        { id: "settings", icon: "gear", shortcut: "⌘," },
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
    <div className="acct" ref={ref}>
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
      <button className="side-foot" ref={foot} aria-label={t("sidebar.account")} aria-haspopup="menu" aria-expanded={!!open} onClick={(e) => setOpen(open ? false : e.detail ? "mouse" : "key")}>
        <span className="av" style={{ background: "linear-gradient(135deg,var(--aurora-1),var(--aurora-2))" }} aria-hidden="true">
          {(window.hermesh?.userName ?? "H").slice(0, 1).toUpperCase()}
        </span>
        <span className="who">
          <b>{window.hermesh?.userName ?? "Hermesh"}</b>
          <span>
            <i className={`dotc ${st.status?.relayConnected ? "on" : ""}`} />
            {st.status?.relayConnected ? t("sidebar.connected", { count: online }) : t("sidebar.notConnected")}
          </span>
        </span>
        <Icon name="chevD" className="sm chev" />
      </button>
    </div>
  );
}

const SessionRow = memo(function SessionRow({ s, sel, grant, meta, renaming, store }: {
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
          {renaming ? <RenameField s={s} store={store} /> : <span className="stitle" title={displayTitle(s)}>{displayTitle(s)}</span>}
          <span className="smeta">
            <StatusDot s={s} />
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
            <span className="sl2t">{grant} · {codingAgentShort(s.agent)} · {s.projectId}</span>
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

/** The status in words, beside its dot: only what needs saying (a running or idle session is its dot). */
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
    default:
      return null;
  }
}

/** The second line's right end, under the status: how long a running turn has run, else when the session last changed. */
function RowTime({ s }: { s: Session }) {
  const running = s.status === SessionStatus.RUNNING;
  const now = useNow(running ? 1000 : 30_000);
  const text = running ? clockText((now.getTime() - (dateOf(s.progress?.turnStartedAt) ?? createdDate(s)).getTime()) / 1000) : listTime(updatedDate(s), now);
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
function ListViewMenu() {
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
    st.set({ view: { ...v, ...patch } });
    close(true);
  };
  const groups = { status: t("listView.byStatus"), date: t("listView.byDate"), agent: t("listView.byAgent"), machine: t("listView.byMachine") };
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
