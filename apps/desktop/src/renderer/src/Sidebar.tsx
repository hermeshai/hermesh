import { memo, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from "react";
import { Trans, useTranslation } from "react-i18next";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import {
  bucketCounted, bucketTitle, clockText, codingAgentShort, codingAgentTitle, createdDate, dateOf, displayTitle,
  filterAny, filterNarrowed, isArchivable, isFailed, listTime, sessionFilter, sessionSections, updatedDate, type SessionFilter,
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

export function Sidebar() {
  const st = useStore();
  const { t } = useTranslation("common");
  const now = useNow(30_000);
  const sections = sessionSections(st.sessions, st.filter, now);
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
      <FilterHeader />
      <div className="side-list" role="list" aria-label={t("sidebar.sessions")} ref={list}>
        {sections.map((sec) => (
          <div key={sec.bucket} role="group" aria-label={bucketTitle(sec.bucket)}>
            <div className="grp-h">
              {bucketTitle(sec.bucket)}
              {bucketCounted(sec.bucket) && <span className="n">{sec.sessions.length}</span>}
            </div>
            {sec.sessions.map((s) => (
              <SessionRow
                key={s.sessionId}
                s={s}
                sel={st.selection.kind === "session" && st.selection.id === s.sessionId}
                grant={grantLabel(st, s)}
                store={latest}
              />
            ))}
          </div>
        ))}
        {!st.sessionsLoaded ? null : st.moreSessions ? (
          <div className="endrow" ref={end}>{t("sidebar.loadingOlder")}</div>
        ) : total === 0 ? (
          <div className="endrow" style={{ paddingTop: 30 }}>
            {filterNarrowed(st.filter) ? t("sidebar.noMatch") : <Trans t={t} i18nKey="sidebar.empty" components={{ br: <br /> }} />}
          </div>
        ) : (
          <div className="endrow">
            {t("sidebar.noOlder")}
            {!st.filter.archived && (
              <>
                {" · "}
                <button className="link" onClick={() => st.set({ filter: { ...st.filter, archived: true } })}>{t("sidebar.showArchived")}</button>
              </>
            )}
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

const SessionRow = memo(function SessionRow({ s, sel, grant, store }: { s: Session; sel: boolean; grant: string; store: RefObject<Store> }) {
  const { t } = useTranslation("common");
  const archived = s.status === SessionStatus.ARCHIVED;
  const act = isArchivable(s) || archived;
  return (
    <div
      className={`srow ${sel ? "sel" : ""} ${act ? "has-act" : ""} ${archived ? "arch" : ""}`}
      role="listitem"
      aria-current={sel || undefined}
      tabIndex={0}
      onClick={() => store.current.select(s.sessionId)}
      onKeyDown={(e) => e.key === "Enter" && store.current.select(s.sessionId)}
      onContextMenu={(e) => {
        e.preventDefault();
        sessionMenu(store.current, s);
      }}
    >
      <div className="smain">
        <div className="sl1">
          <span className="stitle" title={displayTitle(s)}>{displayTitle(s)}</span>
          <span className="smeta">
            <StatusDot s={s} />
            <RowStatus s={s} />
          </span>
          {isArchivable(s) && (
            <button className="rowact" title={t("sessionMenu.archive")} aria-label={t("sessionMenu.archive")} onClick={(e) => (e.stopPropagation(), archive(store.current, s))}>
              <Icon name="archive" className="sm" />
            </button>
          )}
          {archived && (
            <button className="rowact" title={t("sessionMenu.unarchive")} aria-label={t("sessionMenu.unarchive")} onClick={(e) => (e.stopPropagation(), unarchive(store.current, s))}>
              <Icon name="unarchive" className="sm" />
            </button>
          )}
        </div>
        <div className="sl2">
          <span className="sl2t">{grant} · {codingAgentShort(s.agent)} · {s.projectId}</span>
          <RowTime s={s} />
        </div>
      </div>
    </div>
  );
});

/** The session's context menu (native): open, archive, narrow the list to its grant/project/machine, copy its id. */
async function sessionMenu(st: Store, s: Session) {
  const archived = s.status === SessionStatus.ARCHIVED;
  const id = await window.hermesh?.popupMenu([
    { id: "open", label: i18n.t("common:sessionMenu.open") },
    { separator: true },
    { id: archived ? "unarchive" : "archive", label: i18n.t(archived ? "common:sessionMenu.unarchive" : "common:sessionMenu.archive"), enabled: archived || isArchivable(s) },
    { separator: true },
    { id: "grant", label: i18n.t("common:sessionMenu.onlyGrant", { name: grantLabel(st, s) }) },
    { id: "project", label: i18n.t("common:sessionMenu.onlyProject", { name: s.projectId }) },
    { id: "machine", label: i18n.t("common:sessionMenu.onlyMachine", { name: machineName(st, s.machineId) }) },
    { separator: true },
    { id: "copy", label: i18n.t("common:sessionMenu.copyId") },
  ]);
  switch (id) {
    case "open":
      return st.select(s.sessionId);
    case "archive":
      return archive(st, s);
    case "unarchive":
      return unarchive(st, s);
    case "grant":
      return st.set({ filter: { ...st.filter, grant: s.createdBy } });
    case "project":
      return st.set({ filter: { ...st.filter, project: s.projectId } });
    case "machine":
      return st.set({ filter: { ...st.filter, machine: s.machineId } });
    case "copy":
      return navigator.clipboard.writeText(s.sessionId);
  }
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

function FilterHeader() {
  const st = useStore();
  const { t } = useTranslation("common");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const f = st.filter;
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);
  const setF = (patch: Partial<SessionFilter>) => st.set({ filter: { ...f, ...patch } });
  const names: Record<keyof Omit<SessionFilter, "archived">, (v: string) => string> = {
    grant: (v) => st.clients.find((c) => c.clientId === v)?.label ?? t("sidebar.disconnectedGrant"),
    project: (v) => v,
    agent: (v) => codingAgentTitle(v),
    machine: (v) => machineName(st, v),
  };
  const agents = [...new Set(st.sessions.map((s) => s.agent).concat(st.machines.flatMap((m) => (m.status?.agents ?? []).filter((a) => a.available && a.loggedIn).map((a) => a.name))))];
  const projects = [...new Set(st.projects.map((p) => p.projectId))];
  const select = (k: keyof typeof names, label: string, opts: [string, string][]) => (
    <label className="fp-r">
      <span>{label}</span>
      <select className={`psel ${f[k] ? "on" : ""}`} aria-label={label} value={f[k]} onChange={(e) => setF({ [k]: e.target.value })}>
        <option value="">{t("filter.all")}</option>
        {opts.map(([v, l]) => (
          <option key={v} value={v}>{l}</option>
        ))}
      </select>
    </label>
  );
  return (
    <div className="side-head" ref={ref}>
      <div className="lh">
        <span>{t("sidebar.sessions")}</span>
        <button className={`tb ${filterAny(f) ? "act" : ""} ${open ? "on" : ""}`} title={t("filter.title")} aria-label={t("filter.title")} aria-expanded={open} onClick={() => setOpen(!open)}>
          <Icon name="filter" />
        </button>
      </div>
      {filterAny(f) && (
        <div className="fchips">
          {(Object.keys(names) as (keyof typeof names)[]).filter((k) => f[k]).map((k) => (
            <span className="fchip" key={k}>
              <span>{names[k](f[k])}</span>
              <button aria-label={t("filter.remove")} onClick={() => setF({ [k]: "" })}><Icon name="x" /></button>
            </span>
          ))}
          {f.archived && (
            <span className="fchip">
              <span>{t("filter.withArchived")}</span>
              <button aria-label={t("filter.hideArchived")} onClick={() => setF({ archived: false })}><Icon name="x" /></button>
            </span>
          )}
        </div>
      )}
      {open && (
        <div className="fpop" role="dialog" aria-label={t("filter.title")}>
          <div className="fp-h">
            {t("filter.title")}
            <button className="link" disabled={!filterAny(f)} style={filterAny(f) ? undefined : { opacity: 0.4 }} onClick={() => st.set({ filter: sessionFilter() })}>{t("filter.clear")}</button>
          </div>
          {select("grant", t("filter.grant"), st.clients.map((c) => [c.clientId, c.label]))}
          {select("project", t("filter.project"), projects.map((p) => [p, p]))}
          {select("agent", t("filter.agent"), agents.map((a) => [a, codingAgentTitle(a)]))}
          {select("machine", t("filter.machine"), st.machines.map((m) => [m.machineId, m.machineName]))}
          <button className={`cb ${f.archived ? "on" : ""}`} role="checkbox" aria-checked={f.archived} onClick={() => setF({ archived: !f.archived })}>
            <span className="box"><Icon name="check" /></span>{t("filter.showArchived")}
          </button>
        </div>
      )}
    </div>
  );
}
