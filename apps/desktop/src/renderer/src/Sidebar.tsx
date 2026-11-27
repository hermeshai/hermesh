import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { GitState, SessionStatus, type Session } from "@hermesh/proto/session";
import {
  bucketTitle, childrenOf, clockText, HER_SECTION, herChildren, isHerConversation, isTreeArchivable, sliceChildren, treeMark, treeStatus, elapsedSeconds, codingAgentShort, codingAgentTitle, createdDate, dateOf, diffBarBlocks, displayTitle, hasGitDeliveryBox, isActive, isArchivable, isFailed, listTime, naturalSectionKey, needsAttention, needsGitRefresh,
  SECTION_EXPAND_STEP, SECTION_INITIAL_LIMIT, sessionGitMark, sessionSections, sessionsNeedingGitRefresh, sliceSectionSessions, statusTitle, teamState, updatedDate, type SessionBucket, type SessionSection, type SessionView,
  SCRATCH_PROJECT,
} from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../shared/i18n";
import { UpdateReminder } from "./update";
import { sessions as sessionApi } from "./api";
import { AppMenuButton } from "./MainWindow";
import { Icon } from "./icons";
import { setSidebar } from "./layout";
import { TitleShine } from "./runningMotion";
import { unreadCount } from "./her/environments";
import { expiringClients } from "./overview/Attention";
import { keys, ownKeys } from "./platform";
import { useNow, useStore, type Store } from "./store";
import { forgetSessionDraft } from "./session/draftMemory";

export function grantLabel(st: Store, s: Session): string {
  // A session the owner started here (a v2 thread's): "You".
  if (s.createdBy === "owner") return i18n.t("common:sidebar.you");
  return st.clients.find((c) => c.clientId === s.createdBy)?.label ?? i18n.t("common:sidebar.disconnectedGrant");
}

/** Her's own grants: the clients that carry her_machine_id. */
export function herClients(st: Pick<Store, "clients">): string[] {
  return st.clients.filter((c) => c.herMachineId !== "").map((c) => c.clientId);
}

/** Who sent work, in a row (docs/her-team.md §4.7): Her by her name alone, the owner as "You", the others as their grants name them. */
export function senderName(st: Pick<Store, "clients">, clientId: string): string {
  if (clientId === "owner") return i18n.t("common:sidebar.you");
  if (herClients(st).includes(clientId)) return "Her";
  return st.clients.find((c) => c.clientId === clientId)?.label ?? i18n.t("common:sidebar.disconnectedGrant");
}

/** A session's second line in lists: who sent the work to which coding agent, and the project. */
export function senderLine(st: Store, s: Session): string {
  if (isHerConversation(s)) return i18n.t("common:sidebar.sentToHer");
  return `${senderName(st, s.createdBy)} → ${codingAgentShort(s.agent)} · ${s.projectId}`;
}

export function machineName(st: Store, id: string): string {
  return st.machines.find((m) => m.machineId === id)?.machineName ?? id;
}

const ARCHIVE_UNDO_GRACE_SECONDS = 6;
const setSessionArchived = (id: string, archived: boolean, graceSeconds = 0) =>
  sessionApi.updateSession({ sessionId: id, hasArchived: true, archived, archiveGraceSeconds: archived ? graceSeconds : 0 });
const setSessionPinned = (id: string, pinned: boolean) => sessionApi.updateSession({ sessionId: id, hasPinned: true, pinned });

let archiveChain: Promise<void> = Promise.resolve();
const archiveOps = new Map<string, number>();

function insertAt(otherIds: readonly string[], movedId: string, slot: number): string[] {
  const next = otherIds.filter((x) => x !== movedId);
  next.splice(Math.max(0, Math.min(next.length, slot)), 0, movedId);
  return next;
}

export function togglePin(st: Store, s: Session, slot?: number) {
  const next = !s.pinned;
  const curPinned = listSections(st, new Date()).find((x) => x.grouping === "pinned")?.sessions.map((x) => x.sessionId) ?? [];
  const pinnedOrder = next ? insertAt(curPinned, s.sessionId, slot ?? 0) : st.pinnedOrder.filter((id) => id !== s.sessionId);
  st.set({ pinnedOrder });
  void st.run(() => setSessionPinned(s.sessionId, next));
}

/** The work a conversation with Her sent, nested in the list or not: it is archived with it (docs/her-conversations.md D14). */
function sentFrom(st: Pick<Store, "sessions">, s: Session): Session[] {
  return isHerConversation(s) ? (herChildren(st.sessions).get(s.sessionId) ?? []) : [];
}

/** Whether a session can be archived now: a conversation with Her only once all it sent has stopped too. */
export function canArchive(st: Pick<Store, "sessions">, s: Session): boolean {
  return isTreeArchivable(s, sentFrom(st, s));
}

function runTreeArchive(st: Store, tree: Session[], archived: boolean, toastText?: string, undo?: () => void) {
  if (tree.length === 0) return Promise.resolve(true);
  const ids = tree.map((x) => x.sessionId);
  const ops = new Map<string, number>();
  for (const id of ids) {
    const next = (archiveOps.get(id) ?? 0) + 1;
    archiveOps.set(id, next);
    ops.set(id, next);
  }
  const root = tree[0]!;
  const title = displayTitle(root);
  st.setPendingArchived(ids, archived);
  st.noteArchiveCleanup(root.sessionId, archived ? title : null);
  if (toastText) st.show(toastText, undo);

  const grace = archived ? ARCHIVE_UNDO_GRACE_SECONDS : 0;
  const runNext = archiveChain.then(async () => {
    let kept = false;
    const ok = await st.run(async () => {
      const res = await Promise.all(tree.map((x) => setSessionArchived(x.sessionId, archived, grace)));
      kept = res[0]?.transitionApplied === "workspace_kept";
    });
    if (!ok) {
      const rollbackIds = ids.filter((id) => archiveOps.get(id) === ops.get(id));
      st.setPendingArchived(rollbackIds, null);
      if (archiveOps.get(root.sessionId) === ops.get(root.sessionId)) {
        st.noteArchiveCleanup(root.sessionId, null);
      }
      return false;
    }
    if (archived && kept && archiveOps.get(root.sessionId) === ops.get(root.sessionId)) {
      st.noteArchiveCleanup(root.sessionId, null);
      st.show(i18n.t("common:sidebar.archivedKept", { title }), undo);
    }
    return true;
  });
  archiveChain = runNext.then(() => undefined, () => undefined);
  return runNext;
}

export function archive(st: Store, s: Session) {
  const tree = [s, ...sentFrom(st, s)];
  const title = displayTitle(s);
  void runTreeArchive(st, tree, true, i18n.t("common:sidebar.archived", { title }), () => {
    void unarchiveAll(st, tree);
  });
}

function unarchiveAll(st: Store, tree: Session[]) {
  return runTreeArchive(st, tree, false);
}

export function unarchive(st: Store, s: Session, done?: string) {
  return runTreeArchive(st, [s], false, done);
}

/** Delete, once confirmed: what goes for good, what stays (the branch), and that a turn in progress is interrupted. */
export function confirmDelete(st: Store, s: Session) {
  const title = displayTitle(s);
  const message = [
    i18n.t(s.branch ? "common:sessionMenu.deleteMessage" : "common:sessionMenu.deleteMessageNoBranch", { branch: s.branch }),
    isActive(s) ? i18n.t("common:sessionMenu.deleteRunning") : "",
    sentFrom(st, s).length > 0 ? i18n.t("common:sessionMenu.deleteKeepsChildren") : "",
  ].filter(Boolean).join(" ");
  st.set({
    sheet: {
      kind: "confirm",
      title: i18n.t("common:sessionMenu.deleteTitle", { title }),
      message,
      button: i18n.t("common:sessionMenu.deleteButton"),
      run: () => sessionApi.deleteSession({ sessionId: s.sessionId }).then(() => {
        forgetSessionDraft(s.sessionId);
        if (st.selection.kind === "session" && st.selection.id === s.sessionId) {
          st.set({ selection: { kind: "overview" } });
        }
      }),
      done: i18n.t("common:sessionMenu.deleted", { title }),
    },
  });
}

/**
 * The list as the sidebar shows it, in the owner's view; machines this Mac first, then by name. By status, the
 * running group stays while nothing runs, empty, so it is always there to look at (docs/her-team.md §16).
 */
export function listSections(
  st: Pick<Store, "sessions" | "view" | "archived" | "machines" | "clients"> & Partial<Pick<Store, "pinnedOrder" | "activeOrder" | "sectionOrder">>,
  now: Date,
): SessionSection[] {
  const machines = [...st.machines].sort((a, b) => Number(b.local) - Number(a.local) || a.machineName.localeCompare(b.machineName)).map((m) => m.machineId);
  return sessionSections(st.sessions, st.view, {
    archived: st.archived,
    now,
    machines,
    her: herClients(st),
    alwaysActive: true,
    pinnedOrder: st.pinnedOrder,
    activeOrder: st.activeOrder,
    sectionOrder: st.sectionOrder,
  });
}

export function sectionTitle(st: Store, sec: SessionSection): string {
  if (sec.grouping === "project" && sec.key === HER_SECTION) return i18n.t("common:sidebar.herSection");
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

/** What the overview's mark says: how many things need the owner. */
function overviewMarks(st: Store, now: Date) {
  const team = teamState(st.sessions, st.pairings.length);
  return { waits: team.waiting.length + st.pairings.length + expiringClients(st, now).length };
}

/** The overview (⌘1): an orange dot when something needs the owner; nothing while sessions run (each row shows its own). */
function OverviewItem({ now }: { now: Date }) {
  const st = useStore();
  const { t } = useTranslation("common");
  const { waits } = overviewMarks(st, now);
  return (
    <button className={`nav-i ${st.selection.kind === "overview" ? "sel" : ""}`} onClick={() => st.set({ selection: { kind: "overview" } })}>
      <Icon name="grid" />
      <span>{t("sidebar.overview")}</span>
      <span className="nav-marks">
        {waits > 0 && <span className="dot-w" role="img" aria-label={t("sidebar.attention", { count: waits })} />}
        {waits === 0 && <span className="kbd">{keys("CmdOrCtrl+1")}</span>}
      </span>
    </button>
  );
}

/** New Session (⌘N), over Her: the composer, on the machine and project last used (docs/thread-composer.md §4.6). */
function NewSessionItem() {
  const st = useStore();
  const { t } = useTranslation("thread");
  return (
    <button className={`nav-i ${st.selection.kind === "newSession" ? "sel" : ""}`} data-testid="nav-new-thread" onClick={() => st.set({ selection: { kind: "newSession" } })}>
      <Icon name="plus" />
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
      onClick={() => st.set({ selection: { kind: "newSession", machineId: latest?.machineId, projectId: sec.key } })}
    >
      <Icon name="plus" />
    </button>
  );
}

/**
 * Every session, as the view lays them out (docs/her-team.md §16): grouped, each group collapsible, older ones
 * loaded as the end comes into view; archived ones on request.
 */
function Sessions({ now }: { now: Date }) {
  const st = useStore();
  const { t } = useTranslation("common");
  const { sessions, view, archived, machines, clients, pinnedOrder, activeOrder, sectionOrder } = st;
  const sections = useMemo(
    () => listSections({ sessions, view, archived, machines, clients, pinnedOrder, activeOrder, sectionOrder }, now),
    [sessions, view, archived, machines, clients, pinnedOrder, activeOrder, sectionOrder, now],
  );
  const total = sections.reduce((n, x) => n + x.sessions.length, 0);
  const [expanded, setExpanded] = useState<{ group: SessionView["group"]; limits: Record<string, number> }>({ group: view.group, limits: {} });
  const limits = expanded.group === view.group ? expanded.limits : {};
  const sel = st.selection;
  const selectedId = sel.kind === "session" ? sel.id : null;
  // All non-archived work sent from each conversation with Her, and the subset nested in the list.
  const allTrees = useMemo(() => herChildren(archived ? sessions : sessions.filter((x) => x.status !== SessionStatus.ARCHIVED)), [sessions, archived]);
  const trees = useMemo(() => (view.nest ? allTrees : new Map<string, Session[]>()), [allTrees, view.nest]);
  const rendered = useMemo(
    () =>
      sections.map((sec) => {
        const id = `${sec.grouping}:${sec.key}`;
        const open = !st.collapsed.includes(id);
        const limit = limits[id] ?? SECTION_INITIAL_LIMIT;
        const slice = sliceSectionSessions(sec.sessions, sec.grouping, limit, selectedId, allTrees);
        return { sec, id, open, limit, slice };
      }),
    [sections, st.collapsed, limits, selectedId, allTrees],
  );
  const anyTruncated = rendered.some((r) => r.slice.hiddenCount > 0);
  const [allKids, setAllKids] = useState<ReadonlySet<string>>(() => new Set());
  const toggleTree = (id: string) => {
    const key = `tree:${id}`;
    const cur = latest.current;
    cur.set({ collapsed: cur.collapsed.includes(key) ? cur.collapsed.filter((c) => c !== key) : [...cur.collapsed, key] });
  };
  const row = (s: Session, _sec: SessionSection, tree?: { kids: Session[]; shut: boolean }, child?: boolean) => {
    const kids = child ? undefined : allTrees.get(s.sessionId);
    return (
      <SessionRow
        key={s.sessionId}
        s={s}
        sel={sel.kind === "session" && sel.id === s.sessionId}
        sender={senderName(st, s.createdBy)}
        meta={view.metadata}
        renaming={st.renaming === s.sessionId}
        store={latest}
        hover={hoverCtrl}
        child={child}
        twist={tree ? (tree.shut ? "shut" : "open") : undefined}
        treeRunning={kids?.length ? treeStatus(s, kids) === "active" : undefined}
        treeMark={(!tree || tree.shut) && kids?.length ? treeMark(s, kids) : undefined}
        onTwist={tree ? toggleTree : undefined}
      />
    );
  };
  const withTree = (s: Session, sec: SessionSection) => {
    const kids = trees.get(s.sessionId);
    if (!kids?.length) return row(s, sec);
    const shut = st.collapsed.includes(`tree:${s.sessionId}`);
    const shown = sliceChildren(kids, allKids.has(s.sessionId), selectedId);
    return (
      <div key={s.sessionId} className="stree">
        {row(s, sec, { kids, shut })}
        {!shut && (
          <div className="kids" role="group" aria-label={displayTitle(s)}>
            {shown.visible.map((k) => row(k, sec, undefined, true))}
            {shown.hiddenCount > 0 && (
              <button className="grp-more" onClick={() => setAllKids((m) => new Set(m).add(s.sessionId))}>
                {t("sidebar.showMore", { count: shown.hiddenCount })}
              </button>
            )}
          </div>
        )}
      </div>
    );
  };
  const list = useRef<HTMLDivElement>(null);
  const end = useRef<HTMLDivElement>(null);
  // The rows get the store as of a click, so a change elsewhere doesn't render them again.
  const latest = useRef(st);
  latest.current = st;

  const [hovered, setHovered] = useState<{ sessionId: string; top: number; left: number } | null>(null);
  const hoveredRef = useRef(hovered);
  hoveredRef.current = hovered;
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const hoverCtrl = useRef<RowHoverController>({
    enter(sessionId, rowEl) {
      if (hoverTimer.current) clearTimeout(hoverTimer.current);
      const delay = hoveredRef.current ? 30 : 180;
      hoverTimer.current = setTimeout(() => {
        const cur = latest.current;
        if (cur.renaming || cur.searching || cur.sheet || document.body.classList.contains("is-dragging")) return;
        const sideRect = rowEl.closest(".side")?.getBoundingClientRect();
        const rowRect = rowEl.getBoundingClientRect();
        if (rowRect.width === 0 && rowRect.height === 0) return;
        setHovered({
          sessionId,
          top: rowRect.top,
          left: (sideRect ? sideRect.right : rowRect.right) + 8,
        });
      }, delay);
    },
    leave(sessionId) {
      if (hoverTimer.current) {
        clearTimeout(hoverTimer.current);
        hoverTimer.current = null;
      }
      setHovered((cur) => (cur?.sessionId === sessionId ? null : cur));
    },
    dismiss() {
      if (hoverTimer.current) {
        clearTimeout(hoverTimer.current);
        hoverTimer.current = null;
      }
      setHovered(null);
    },
  });
  useEffect(() => {
    if (st.renaming || st.searching || st.sheet) hoverCtrl.current.dismiss();
  }, [st.renaming, st.searching, st.sheet]);
  useEffect(() => {
    const onBlur = () => hoverCtrl.current.dismiss();
    window.addEventListener("blur", onBlur);
    return () => {
      window.removeEventListener("blur", onBlur);
      if (hoverTimer.current) clearTimeout(hoverTimer.current);
    };
  }, []);

  // Refresh Git delivery state on demand when inspecting a specific unsettled code
  // session (hovering its card or selecting it), plus a small local check on focus.
  const gitRefreshBusy = useRef(false);
  const gitRefreshLast = useRef(0);
  const gitRefreshedPerSession = useRef(new Map<string, number>());
  const refreshSingleSessionGit = (sessionId: string | null | undefined) => {
    if (!sessionId) return;
    const s = latest.current.sessions.find((x) => x.sessionId === sessionId);
    if (!s || !needsGitRefresh(s)) return;
    const nowMs = Date.now();
    if (nowMs - (gitRefreshedPerSession.current.get(sessionId) ?? 0) < 5000) return;
    gitRefreshedPerSession.current.set(sessionId, nowMs);
    sessionApi.refreshGitStatus({ sessionIds: [sessionId] }).catch(() => {});
  };
  useEffect(() => {
    if (!st.sessionsLoaded) return;
    refreshSingleSessionGit(selectedId);
  }, [st.sessionsLoaded, selectedId]);
  useEffect(() => {
    if (!st.sessionsLoaded) return;
    refreshSingleSessionGit(hovered?.sessionId);
  }, [st.sessionsLoaded, hovered?.sessionId]);
  useEffect(() => {
    if (!st.sessionsLoaded) return;
    const triggerRefresh = () => {
      const nowMs = Date.now();
      if (gitRefreshBusy.current || nowMs - gitRefreshLast.current < 1500) return;
      const cur = latest.current;
      const selId = cur.selection.kind === "session" ? cur.selection.id : null;
      const sessionIds = sessionsNeedingGitRefresh(cur.sessions, selId, 6);
      if (sessionIds.length === 0) return;
      gitRefreshBusy.current = true;
      gitRefreshLast.current = nowMs;
      sessionApi.refreshGitStatus({ sessionIds }).catch(() => {}).finally(() => {
        gitRefreshBusy.current = false;
      });
    };
    const offIpc = window.hermesh?.onWindowFocus((focused) => {
      if (focused) triggerRefresh();
    });
    const onFocus = () => triggerRefresh();
    const onVis = () => {
      if (document.visibilityState === "visible") triggerRefresh();
    };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVis);
    return () => {
      offIpc?.();
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [st.sessionsLoaded]);

  // Older sessions load as the end of the list comes into view (again after each page, until the list is full or there are no more).
  // While an open group hides rows behind "Show more", ask on demand instead of pulling page after page into a folded tail.
  const { moreSessions, loadOlderSessions } = st;
  useEffect(() => {
    if (!moreSessions || anyTruncated || !end.current) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && loadOlderSessions(), { root: list.current, rootMargin: "200px" });
    io.observe(end.current);
    return () => io.disconnect();
  }, [moreSessions, anyTruncated, loadOlderSessions, sessions]);
  const toggleGroup = (id: string, open: boolean) => {
    if (open && limits[id] !== undefined) {
      const next = { ...limits };
      delete next[id];
      setExpanded({ group: view.group, limits: next });
    }
    st.set({ collapsed: open ? [...st.collapsed, id] : st.collapsed.filter((c) => c !== id) });
  };
  const showMore = (id: string, curLimit: number, hiddenCount: number) => {
    setExpanded({ group: view.group, limits: { ...limits, [id]: curLimit + SECTION_EXPAND_STEP } });
    if (hiddenCount <= SECTION_EXPAND_STEP && moreSessions) loadOlderSessions();
  };
  const hoveredSession = hovered ? sessions.find((s) => s.sessionId === hovered.sessionId) : undefined;
  const onRowPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 || (e.target as Element).closest("button, input, [role=menu]")) return;
    const rowEl = (e.target as Element).closest<HTMLElement>(".srow[data-session-id]");
    const listEl = list.current;
    const container = rowEl?.closest<HTMLElement>(".side-sessions");
    if (!rowEl || !listEl || !container || rowEl.classList.contains("arch") || rowEl.classList.contains("renaming") || rowEl.dataset.child) return;
    // A conversation with Her is dragged with the work under it (docs/her-conversations.md §3.7).
    const dragEl = rowEl.parentElement?.classList.contains("stree") ? rowEl.parentElement : rowEl;
    const id = rowEl.dataset.sessionId!;
    const sObj = latest.current.sessions.find((x) => x.sessionId === id);
    if (!sObj) return;

    const startX = e.clientX;
    const startY = e.clientY;
    const rect = dragEl.getBoundingClientRect();
    let lastClientY = startY;
    let started = false;
    let scrollRaf = 0;
    let ghost: HTMLElement | null = null;
    let dropLine: HTMLElement | null = null;
    let pinDropEl: HTMLElement | null = null;
    type DropTarget =
      | { kind: "pin-dropzone" }
      | { kind: "unpin" }
      | { kind: "section"; secId: string; slot: number }
      | null;
    let target: DropTarget = null;

    const hideIndicators = () => {
      if (dropLine) dropLine.style.display = "none";
      pinDropEl?.classList.remove("over");
      target = null;
    };

    const showLineAt = (viewportY: number, listRect: DOMRect) => {
      if (!dropLine) return;
      dropLine.style.top = `${viewportY - listRect.top + listEl.scrollTop}px`;
      dropLine.style.display = "block";
    };

    const updateTarget = (clientY: number) => {
      if (!started || !ghost || !dropLine) return;
      ghost.style.top = `${rect.top + (clientY - startY)}px`;

      const listRect = listEl.getBoundingClientRect();
      if (pinDropEl) {
        const pr = pinDropEl.getBoundingClientRect();
        if (clientY >= listRect.top - 12 && clientY <= pr.bottom) {
          pinDropEl.classList.add("over");
          dropLine.style.display = "none";
          target = { kind: "pin-dropzone" };
          return;
        }
        pinDropEl.classList.remove("over");
      }

      const blockOf = (r: HTMLElement) => (r.parentElement?.classList.contains("stree") ? r.parentElement : r);
      const placeInSection = (groupEl: HTMLElement, secId: string) => {
        const rows = [...groupEl.querySelectorAll<HTMLElement>(".srow[data-session-id]:not([data-child])")];
        const origIdx = rows.findIndex((r) => r.dataset.sessionId === id);
        if (rows.length === 0) {
          if (origIdx !== -1) return hideIndicators();
          const hRect = (groupEl.querySelector(".grp-hrow") ?? groupEl).getBoundingClientRect();
          showLineAt(hRect.bottom + 2, listRect);
          target = { kind: "section", secId, slot: 0 };
          return;
        }
        if (origIdx !== -1 && rows.length <= 1) return hideIndicators();

        const otherRows = origIdx === -1 ? rows : rows.filter((_, i) => i !== origIdx);
        let slot = otherRows.length;
        for (let i = 0; i < otherRows.length; i++) {
          const cr = blockOf(otherRows[i]).getBoundingClientRect();
          // Require crossing 35% into an adjacent row when reordering within the same section; use 50% when entering from another section.
          const ratio = origIdx === -1 ? 0.5 : i < origIdx ? 0.65 : 0.35;
          if (clientY < cr.top + cr.height * ratio) {
            slot = i;
            break;
          }
        }
        if (origIdx !== -1 && slot === origIdx) return hideIndicators();

        const lineViewportY =
          slot === 0
            ? blockOf(otherRows[0]).getBoundingClientRect().top - 1
            : slot === otherRows.length
              ? blockOf(otherRows[otherRows.length - 1]).getBoundingClientRect().bottom + 1
              : (blockOf(otherRows[slot - 1]).getBoundingClientRect().bottom + blockOf(otherRows[slot]).getBoundingClientRect().top) / 2;
        showLineAt(lineViewportY, listRect);
        target = { kind: "section", secId, slot };
      };

      const pinnedGroup = container.querySelector<HTMLElement>('[data-sec-id="pinned:pinned"]');
      if (pinnedGroup) {
        const pRect = pinnedGroup.getBoundingClientRect();
        if (clientY <= pRect.bottom + 8) {
          placeInSection(pinnedGroup, "pinned:pinned");
          return;
        }
      }

      const curKids = sentFrom(latest.current, sObj);
      const natSecId = naturalSectionKey(latest.current.view, sObj, new Date(), curKids);
      const natGroup = container.querySelector<HTMLElement>(`[data-sec-id="${CSS.escape(natSecId)}"]`);
      if (natGroup && !natGroup.querySelector(".grp-h.shut")) {
        const gRect = natGroup.getBoundingClientRect();
        if (clientY >= gRect.top - 6 && clientY <= gRect.bottom + 32) {
          placeInSection(natGroup, natSecId);
          return;
        }
      }

      if (sObj.pinned && pinnedGroup) {
        showLineAt(pinnedGroup.getBoundingClientRect().bottom + 6, listRect);
        target = { kind: "unpin" };
        return;
      }

      hideIndicators();
    };

    const tickAutoScroll = () => {
      scrollRaf = 0;
      if (!started) return;
      const lr = listEl.getBoundingClientRect();
      const edge = 34;
      let delta = 0;
      if (lastClientY < lr.top + edge && listEl.scrollTop > 0) {
        delta = -Math.ceil(((lr.top + edge - lastClientY) / edge) * 9);
      } else if (lastClientY > lr.bottom - edge && listEl.scrollTop < listEl.scrollHeight - listEl.clientHeight) {
        delta = Math.ceil(((lastClientY - (lr.bottom - edge)) / edge) * 9);
      }
      if (delta !== 0) {
        const prev = listEl.scrollTop;
        listEl.scrollTop += delta;
        if (listEl.scrollTop !== prev) updateTarget(lastClientY);
        scrollRaf = requestAnimationFrame(tickAutoScroll);
      }
    };

    const onMove = (ev: PointerEvent) => {
      lastClientY = ev.clientY;
      if (!started) {
        if (Math.hypot(ev.clientX - startX, ev.clientY - startY) < 5) return;
        started = true;
        hoverCtrl.current.dismiss();
        document.body.classList.add("is-dragging");
        dragEl.classList.add("dragging-src");
        if (!sObj.pinned && !container.querySelector('[data-sec-id="pinned:pinned"]')) {
          pinDropEl = document.createElement("div");
          pinDropEl.className = "pin-dropzone";
          pinDropEl.textContent = i18n.t("common:sidebar.pinDrop");
          listEl.appendChild(pinDropEl);
        }
        ghost = dragEl.cloneNode(true) as HTMLElement;
        ghost.classList.remove("dragging-src", "menu-open", "sel");
        ghost.querySelectorAll(".rowacts").forEach((el) => el.remove());
        ghost.classList.add("drag-ghost");
        ghost.style.width = `${rect.width}px`;
        ghost.style.left = `${rect.left}px`;
        ghost.style.top = `${rect.top}px`;
        document.body.appendChild(ghost);

        dropLine = document.createElement("div");
        dropLine.className = "drop-line";
        dropLine.style.display = "none";
        listEl.appendChild(dropLine);
      }

      updateTarget(ev.clientY);
      if (!scrollRaf) scrollRaf = requestAnimationFrame(tickAutoScroll);
    };

    const cleanup = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", finish);
      window.removeEventListener("pointercancel", cancel);
      window.removeEventListener("keydown", onKeyDown, true);
      if (scrollRaf) cancelAnimationFrame(scrollRaf);
      scrollRaf = 0;
      if (!started) return;
      document.body.classList.remove("is-dragging");
      dragEl.classList.remove("dragging-src");
      ghost?.remove();
      dropLine?.remove();
      pinDropEl?.remove();
    };

    const cancel = () => {
      target = null;
      cleanup();
    };

    const onKeyDown = (ke: KeyboardEvent) => {
      if (ke.key === "Escape" && started) {
        ke.preventDefault();
        ke.stopPropagation();
        cancel();
      }
    };

    const finish = () => {
      const wasStarted = started;
      const chosen = target;
      cleanup();
      if (!wasStarted) return;
      const swallowClick = (ce: MouseEvent) => {
        ce.stopPropagation();
        ce.preventDefault();
      };
      window.addEventListener("click", swallowClick, true);
      setTimeout(() => window.removeEventListener("click", swallowClick, true), 0);

      const curSt = latest.current;
      const curSess = curSt.sessions.find((x) => x.sessionId === id);
      if (!curSess || !chosen) return;

      const curSections = listSections(curSt, new Date());
      if (chosen.kind === "pin-dropzone") {
        if (!curSess.pinned) togglePin(curSt, curSess, 0);
      } else if (chosen.kind === "unpin") {
        if (curSess.pinned) togglePin(curSt, curSess);
      } else if (chosen.kind === "section" && chosen.secId === "pinned:pinned") {
        const curPinnedIds = curSections.find((x) => x.grouping === "pinned")?.sessions.map((x) => x.sessionId) ?? [];
        const nextPinnedOrder = insertAt(curPinnedIds, id, chosen.slot);
        const collapsed = curSt.collapsed.filter((c) => c !== "pinned:pinned");
        curSt.set({ pinnedOrder: nextPinnedOrder, collapsed });
        if (!curSess.pinned) void curSt.run(() => setSessionPinned(id, true));
      } else if (chosen.kind === "section") {
        const domGroup = container.querySelector<HTMLElement>(`[data-sec-id="${CSS.escape(chosen.secId)}"]`);
        const visibleIds = domGroup
          ? [...domGroup.querySelectorAll<HTMLElement>(".srow[data-session-id]:not([data-child])")].map((r) => r.dataset.sessionId!)
          : [];
        const sec = curSections.find((x) => `${x.grouping}:${x.key}` === chosen.secId);
        const allSecIds = sec?.sessions.map((x) => x.sessionId) ?? [];
        const reorderedVisible = insertAt(visibleIds, id, chosen.slot);
        const hiddenIds = allSecIds.filter((x) => x !== id && !reorderedVisible.includes(x));
        const nextOrder = [...reorderedVisible, ...hiddenIds];
        const nextSectionOrder = { ...curSt.sectionOrder, [chosen.secId]: nextOrder };
        const patch: Partial<Store> = { sectionOrder: nextSectionOrder };
        if (chosen.secId === "status:active") patch.activeOrder = nextOrder;
        if (curSess.pinned) {
          patch.pinnedOrder = curSt.pinnedOrder.filter((x) => x !== id);
          curSt.set(patch);
          void curSt.run(() => setSessionPinned(id, false));
        } else {
          curSt.set(patch);
        }
      }
    };

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", finish);
    window.addEventListener("pointercancel", cancel);
    window.addEventListener("keydown", onKeyDown, true);
  };
  return (
    <div className="side-list" ref={list} onScroll={() => hoverCtrl.current.dismiss()}>
      <div className="side-sessions" role="list" aria-label={t("sidebar.sessions")} onPointerDown={onRowPointerDown}>
        {rendered.map(({ sec, id, open, limit, slice }) => (
          <div key={id} role="group" aria-label={sectionTitle(st, sec)} data-sec-id={id}>
            <div className="grp-hrow">
              <button
                className={`grp-h ${open ? "" : "shut"}`}
                aria-expanded={open}
                onClick={() => toggleGroup(id, open)}
              >
                {sectionTitle(st, sec)}
                <Icon name="caret" className="sm" />
              </button>
              {sec.grouping === "project" && <NewInProject sec={sec} />}
            </div>
            {open && sec.sessions.length === 0 && <div className="side-none">{t("sidebar.noneNow")}</div>}
            {open && slice.visible.map((s) => withTree(s, sec))}
            {open && slice.hiddenCount > 0 && (
              <button className="grp-more" onClick={() => showMore(id, limit, slice.hiddenCount)}>
                {t("sidebar.showMore", { count: slice.hiddenCount })}
              </button>
            )}
          </div>
        ))}
        {!st.sessionsLoaded ? null : moreSessions ? (
          anyTruncated ? (
            <div className="endrow">
              <button className="link" onClick={() => loadOlderSessions()}>{t("sidebar.loadOlder")}</button>
            </div>
          ) : (
            <div className="endrow" ref={end}>{t("sidebar.loadingOlder")}</div>
          )
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
      {hovered && hoveredSession && (
        <SessionHoverCard
          s={hoveredSession}
          sender={senderName(st, hoveredSession.createdBy)}
          now={now}
          anchor={hovered}
        />
      )}
    </div>
  );
}

type AccountItem = { id: "settings" | "account" | "newGrant" | "addMachine" | "enterCode"; icon: string; shortcut?: string } | null;

/**
 * The account menu, from the avatar at the sidebar's foot: it opens upward, as wide as the foot, with what adds
 * something and the settings.
 */
export function AccountMenu() {
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
        <span className="av" aria-hidden="true">
          {(window.hermesh?.userName ?? "H").slice(0, 1).toUpperCase()}
        </span>
        <span className="who">
          <b>{window.hermesh?.userName ?? "Hermesh"}</b>
          {/* Signed in or not (docs/cold-start-prd.md §6.1): the connection has its own line where it matters. */}
          <span>{(st.status?.initialized || st.status?.machineOnly) && !st.status.signedOut ? t("sidebar.signedIn") : t("sidebar.signedOut")}</span>
        </span>
        <Icon name="chevD" className="sm chev" />
      </button>
    </div>
  );
}

type RowHoverController = {
  enter: (sessionId: string, rowEl: HTMLElement) => void;
  leave: (sessionId: string) => void;
  dismiss: () => void;
};

/**
 * A session in the sidebar (docs/her-team.md §16): the coding agent's logo (with a brand-blue orbit while it runs),
 * the title (shimmering while it runs), and one mark (an orange dot while it waits for the owner, a red one when it
 * failed, or a delivered Git icon when pushed/merged); with metadata, who sent the work
 * to which agent, the project and the time. Quick archive/unarchive (when applicable) and ⋯ in the mark's place on hover; right-click for the native menu.
 */
export const SessionRow = memo(function SessionRow({ s, sel, sender, meta, renaming, store, hover, child, twist, treeRunning, treeMark, onTwist }: {
  s: Session; sel: boolean; sender: string; meta: boolean; renaming: boolean; store: RefObject<Store>; hover?: RefObject<RowHoverController>;
  /** Work under a conversation with Her: no pin of its own, and it goes where the conversation goes. */
  child?: boolean;
  /** A conversation with work under it: open or shut, with the tree's most urgent mark while shut (D12). */
  twist?: "open" | "shut";
  treeRunning?: boolean;
  treeMark?: "waiting" | "failed";
  onTwist?: (id: string) => void;
}) {
  const { t } = useTranslation("common");
  const archived = s.status === SessionStatus.ARCHIVED;
  const running = (treeRunning ?? (isActive(s) && !needsAttention(s))) && !treeMark;
  const runLabel = running ? t(s.status === SessionStatus.QUEUED ? "status.queued" : s.status === SessionStatus.PREPARING ? "status.preparing" : "status.running") : undefined;
  // Where the ⋯ menu opens (under its button), while it is open.
  const [menu, setMenu] = useState<{ x: number; y: number; key: boolean } | null>(null);
  const [pendingTitle, setPendingTitle] = useState<string | null>(null);
  const shownTitle = pendingTitle ?? displayTitle(s);
  return (
    <div
      className={`srow ${renaming ? "renaming" : "has-act"} ${sel ? "sel" : ""} ${menu ? "menu-open" : ""} ${archived ? "arch" : ""} ${twist ? "tree" : ""}`}
      role="listitem"
      data-session-id={s.sessionId}
      data-child={child || undefined}
      aria-current={sel || undefined}
      tabIndex={0}
      onMouseEnter={(e) => !renaming && !menu && hover?.current.enter(s.sessionId, e.currentTarget)}
      onMouseLeave={() => hover?.current.leave(s.sessionId)}
      onClick={() => {
        hover?.current.dismiss();
        if (!renaming) store.current.select(s.sessionId);
      }}
      onDoubleClick={() => {
        hover?.current.dismiss();
        if (!renaming) store.current.set({ renaming: s.sessionId });
      }}
      onKeyDown={(e) => !renaming && e.key === "Enter" && store.current.select(s.sessionId)}
      onContextMenu={(e) => {
        if (renaming) return;
        e.preventDefault();
        hover?.current.dismiss();
        sessionMenu(store.current, s);
      }}
    >
      {twist && (
        <button
          className={`twist ${twist}`}
          title={t(twist === "shut" ? "sidebar.expand" : "sidebar.collapse")}
          aria-label={t(twist === "shut" ? "sidebar.expand" : "sidebar.collapse")}
          aria-expanded={twist === "open"}
          onDoubleClick={(e) => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation();
            onTwist?.(s.sessionId);
          }}
        >
          <Icon name="chevD" className="sm" />
        </button>
      )}
      <span className={`slogo ${running ? "run" : ""}`} role={runLabel ? "img" : undefined} aria-label={runLabel}>
        {running && <i className="slogo-orbit" aria-hidden="true" />}
        <AgentLogo agent={s.agent} size={16} />
      </span>
      <div className="smain">
        <div className="sl1">
          {renaming ? (
            <TitleInput
              initial={shownTitle}
              className="srename"
              onDone={(title) => {
                const st = store.current;
                st.set({ renaming: null });
                if (!title) return;
                setPendingTitle(title);
                void st.run(() => sessionApi.updateSession({ sessionId: s.sessionId, title })).finally(() => setPendingTitle(null));
              }}
            />
          ) : (
            <RowTitle s={s} title={shownTitle} running={running} />
          )}
          {!renaming && <span className="smeta">{treeMark ? <TreeMark mark={treeMark} /> : <RowMark s={s} />}</span>}
          {!renaming && (
            <span className="rowacts">
              {(archived || canArchive(store.current, s)) && (
                <button
                  className="rowact"
                  title={t(archived ? "sessionMenu.unarchive" : "sessionMenu.archive")}
                  aria-label={t(archived ? "sessionMenu.unarchive" : "sessionMenu.archive")}
                  onDoubleClick={(e) => e.stopPropagation()}
                  onClick={(e) => {
                    e.stopPropagation();
                    hover?.current.dismiss();
                    if (archived) void unarchive(store.current, s);
                    else archive(store.current, s);
                  }}
                >
                  <Icon name={archived ? "unarchive" : "archive"} />
                </button>
              )}
              <button
                className="rowact"
                title={t("sessionMenu.more")}
                aria-label={t("sessionMenu.more")}
                aria-haspopup="menu"
                aria-expanded={!!menu}
                onDoubleClick={(e) => e.stopPropagation()}
                onClick={(e) => {
                  e.stopPropagation();
                  hover?.current.dismiss();
                  const r = e.currentTarget.getBoundingClientRect();
                  setMenu(menu ? null : { x: r.right, y: r.bottom + 4, key: e.detail === 0 });
                }}
              >
                <Icon name="more" />
              </button>
            </span>
          )}
          {menu && <RowMenu s={s} at={menu} store={store} onClose={() => setMenu(null)} />}
        </div>
        {meta && (
          <div className="sl2">
            <span className="sl2t">
              {isHerConversation(s) ? t("sidebar.sentToHer") : child ? `${codingAgentShort(s.agent)} · ${s.projectId}` : `${sender} → ${codingAgentShort(s.agent)} · ${s.projectId}`}
            </span>
            <RowTime s={s} />
          </div>
        )}
      </div>
    </div>
  );
});

/** The session's context menu (native): open, pin, rename, archive, copy its id, delete. */
async function sessionMenu(st: Store, s: Session) {
  const archived = s.status === SessionStatus.ARCHIVED;
  const child = !!st.view.nest && [...childrenOf(st.sessions, st.view).values()].some((kids) => kids.includes(s));
  const id = await window.hermesh?.popupMenu([
    { id: "open", label: i18n.t("common:sessionMenu.open") },
    { separator: true },
    ...(!archived && !child ? [{ id: s.pinned ? "unpin" : "pin", label: i18n.t(s.pinned ? "common:sessionMenu.unpin" : "common:sessionMenu.pin") }] : []),
    { id: "rename", label: i18n.t("common:sessionMenu.rename") },
    { id: archived ? "unarchive" : "archive", label: i18n.t(archived ? "common:sessionMenu.unarchive" : "common:sessionMenu.archive"), enabled: archived || canArchive(st, s) },
    { separator: true },
    { id: "copy", label: i18n.t("common:sessionMenu.copyId") },
    { separator: true },
    { id: "delete", label: i18n.t("common:sessionMenu.delete") },
  ]);
  switch (id) {
    case "open":
      return st.select(s.sessionId);
    case "pin":
    case "unpin":
      return togglePin(st, s);
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
 * A row's ⋯ menu: pin, rename, archive (or unarchive), and delete after a
 * separator. Drawn like the view menu, right-aligned under its button;
 * clicks stay in the menu, not the row.
 */
function RowMenu({ s, at, store, onClose }: { s: Session; at: { x: number; y: number; key: boolean }; store: RefObject<Store>; onClose: () => void }) {
  const { t } = useTranslation("common");
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: -9999, top: at.y });
  const archived = s.status === SessionStatus.ARCHIVED;
  const st = store.current;
  const child = !!st.view.nest && [...childrenOf(st.sessions, st.view).values()].some((kids) => kids.includes(s));
  // A conversation with Her waits for what it sent to stop (D14).
  const waits = !archived && isArchivable(s) && !canArchive(st, s);
  const items: ({ id: string; label: string; run: () => void; disabled?: boolean; danger?: boolean; title?: string } | null)[] = [
    ...(!archived && !child ? [{ id: s.pinned ? "unpin" : "pin", label: t(s.pinned ? "sessionMenu.unpin" : "sessionMenu.pin"), run: () => togglePin(store.current, s) }] : []),
    { id: "rename", label: t("sessionMenu.rename"), run: () => store.current.set({ renaming: s.sessionId }) },
    archived
      ? { id: "unarchive", label: t("sessionMenu.unarchive"), run: () => unarchive(store.current, s) }
      : { id: "archive", label: t("sessionMenu.archive"), run: () => archive(store.current, s), disabled: !canArchive(st, s), title: waits ? t("sessionMenu.stillRunning") : undefined },
    null,
    { id: "delete", label: t("sessionMenu.delete"), run: () => confirmDelete(store.current, s), danger: true },
  ];
  useLayoutEffect(() => {
    const el = ref.current!;
    const top = at.y + el.offsetHeight > innerHeight - 8 ? at.y - el.offsetHeight - 32 : at.y;
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
    <div className="vmenu" role="menu" aria-label={t("sessionMenu.more")} tabIndex={-1} ref={ref} style={pos} onClick={(e) => e.stopPropagation()} onDoubleClick={(e) => e.stopPropagation()} onKeyDown={onKeyDown} onContextMenu={(e) => e.stopPropagation()}>
      {items.map((it, i) =>
        it ? (
          <button
            key={it.id}
            className={`mi ${it.danger ? "danger" : ""}`}
            role="menuitem"
            tabIndex={-1}
            disabled={it.disabled}
            title={it.title}
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

/** A row's title; while it runs (and nothing needs the owner), a fainter band crosses it left to right (app.css .stitle.run). */
function RowTitle({ s, title, running }: { s: Session; title: string; running: boolean }) {
  const run = running || s.titleGenerating;
  return (
    <span className={`stitle ${run ? "run" : ""}`} title={title}>
      {title}
      {run && <TitleShine text={title} />}
    </span>
  );
}

/** A session's title edited in place: ↩ or leaving the field keeps what is written, Esc puts it back. */
export function TitleInput({
  initial,
  className,
  testId,
  onDone,
}: {
  initial: string;
  className: string;
  testId?: string;
  onDone: (title: string | null) => void;
}) {
  const { t } = useTranslation("common");
  const [value, setValue] = useState(initial);
  const done = useRef(false);
  const finish = (keep: boolean) => {
    if (done.current) return;
    done.current = true;
    const title = value.trim();
    onDone(keep && title && title !== initial ? title : null);
  };
  return (
    <input
      className={className}
      aria-label={t("sessionMenu.renameLabel")}
      data-testid={testId}
      autoFocus
      spellCheck={false}
      placeholder={initial}
      value={value}
      onFocus={(e) => e.currentTarget.select()}
      onChange={(e) => setValue(e.target.value)}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.nativeEvent.isComposing || e.keyCode === 229) return;
        if (e.key === "Enter") {
          e.preventDefault();
          finish(true);
        } else if (e.key === "Escape") {
          e.preventDefault();
          finish(false);
        }
      }}
      onBlur={() => finish(true)}
    />
  );
}

/** A shut tree's most urgent mark: something in it waits for the owner, or failed. */
function TreeMark({ mark }: { mark: "waiting" | "failed" }) {
  const { t } = useTranslation("common");
  return mark === "waiting" ? <span className="dot-w" role="img" aria-label={t("status.waiting")} /> : <span className="dot-r" role="img" aria-label={t("status.failed")} />;
}

/** A row's one mark, its state in words for screen readers; settled pushed/merged sessions show their Git mark. */
function RowMark({ s }: { s: Session }) {
  const { t } = useTranslation("common");
  if (s.status === SessionStatus.WAITING_INPUT) return <span className="dot-w" role="img" aria-label={t("status.waiting")} />;
  if (isFailed(s)) return <span className="dot-r" role="img" aria-label={t(s.status === SessionStatus.CRASHED ? "status.crashed" : "status.failed")} />;
  // Its machine cannot be read now: say so, there is no mark for it.
  if (s.status === SessionStatus.UNSPECIFIED) return <>{statusTitle(s.status)}</>;
  const gitMark = sessionGitMark(s);
  if (gitMark === "pushed") return <span className="gmark pushed" role="img" aria-label={t("git.pushed")}><Icon name="gitPushed" /></span>;
  if (gitMark === "merged") return <span className="gmark merged" role="img" aria-label={t("git.merged")}><Icon name="gitMerged" /></span>;
  return null;
}

/**
 * Pure read-only hover card anchored beside the sidebar: full title, metadata,
 * and local Git delivery status (branch, diff bar, latest session commit).
 */
function SessionHoverCard({ s, sender, now, anchor }: {
  s: Session;
  sender: string;
  now: Date;
  anchor: { top: number; left: number };
}) {
  const { t } = useTranslation("common");
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: anchor.left, top: anchor.top - 4 });
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const h = el.offsetHeight;
    const w = el.offsetWidth;
    setPos({
      left: Math.max(8, Math.min(anchor.left, innerWidth - w - 8)),
      top: Math.max(8, Math.min(anchor.top - 4, innerHeight - h - 8)),
    });
  }, [anchor.left, anchor.top, s]);

  const timeText = s.status === SessionStatus.RUNNING
    ? clockText(elapsedSeconds(dateOf(s.progress?.turnStartedAt) ?? createdDate(s), now))
    : listTime(updatedDate(s), now);
  const gs = s.gitStatus;
  const gitState = gs?.state ?? GitState.UNSPECIFIED;
  const diff = s.progress?.diffStat;
  const ins = diff?.insertions ?? 0;
  const del = diff?.deletions ?? 0;
  const showDiff = ins > 0 || del > 0;
  const blocks = showDiff ? diffBarBlocks(ins, del) : null;

  let gitTone = "clean";
  let gitIcon = "check";
  let gitLabel = "";
  let gitHint = "";
  if (gitState === GitState.PUSHED) {
    gitTone = "pushed";
    gitIcon = "gitPushed";
    gitLabel = t("git.hoverPushed");
    gitHint = t("git.hintPushed", { target: gs?.targetRef || "origin" });
  } else if (gitState === GitState.MERGED) {
    gitTone = "merged";
    gitIcon = "gitMerged";
    const target = gs?.targetRef || "main";
    gitLabel = t("git.hoverMerged", { target });
    gitHint = t("git.hintMerged", { target });
  } else if (gitState === GitState.UNPUSHED) {
    gitTone = "unpushed";
    gitIcon = "gitUnpushed";
    gitLabel = t("git.hoverUnpushed", { count: Math.max(1, gs?.commitsAhead ?? 1) });
    gitHint = t("git.hintUnpushed");
  } else if (gitState === GitState.DIRTY) {
    gitTone = "dirty";
    gitIcon = "gitDirty";
    gitLabel = t("git.hoverDirty", { count: Math.max(1, gs?.dirtyFiles ?? diff?.filesChanged ?? 1) });
    gitHint = t("git.hintDirty");
  }

  return createPortal(
    <div className="shover-card" data-testid="session-hover-card" ref={ref} style={pos} role="tooltip">
      <div className="shover-head">
        <span className="shover-logo"><AgentLogo agent={s.agent} size={18} /></span>
        <div className="shover-hmain">
          <div className="shover-title">{displayTitle(s)}</div>
          <div className="shover-sub">{isHerConversation(s) ? t("sidebar.sentToHer") : `${sender} → ${codingAgentShort(s.agent)} · ${s.projectId}`} · {timeText}</div>
        </div>
      </div>
      {hasGitDeliveryBox(s) && (
        <div className={`shover-git ${gitTone}`}>
          <div className="shover-git-top">
            <Icon name={gitIcon} />
            <span className="shover-state">{gitLabel}</span>
            {blocks && (
              <span className="shover-diff">
                <span className="shover-add">+{ins}</span>
                <span className="shover-del">−{del}</span>
                <span className="shover-dbar" aria-hidden="true">
                  {blocks.map((b, i) => <i key={i} className={`shover-sq ${b === "empty" ? "" : b}`} />)}
                </span>
              </span>
            )}
          </div>
          {gs?.branch && (
            <div className="shover-row">
              <Icon name="branch" className="sm" />
              <code className="shover-ref">{gs.branch}</code>
              {gs.targetRef && gs.targetRef !== gs.branch && (
                <>
                  <span className="shover-arr">→</span>
                  <code className="shover-ref">{gs.targetRef}</code>
                </>
              )}
            </div>
          )}
          {gs?.headCommit && (
            <div className="shover-row">
              <Icon name="gitCommit" className="sm" />
              <code className="shover-sha">{gs.headCommit}</code>
              {gs.headSubject && <span className="shover-subj">{gs.headSubject}</span>}
            </div>
          )}
          <div className="shover-hint">{gitHint}</div>
        </div>
      )}
    </div>,
    document.body,
  );
}

/** The second line's right end, under the status: how long a running turn has run, else when the session last changed. */
function RowTime({ s }: { s: Session }) {
  const running = s.status === SessionStatus.RUNNING;
  const now = useNow(running ? 1000 : 30_000);
  const text = running ? clockText(elapsedSeconds(dateOf(s.progress?.turnStartedAt) ?? createdDate(s), now)) : listTime(updatedDate(s), now);
  return <span className="stime">{text}</span>;
}

type ViewPane = "group" | "sort" | "show";
type ViewChoice = {
  label: string;
  on: boolean;
  role: "menuitemradio" | "menuitemcheckbox" | "menuitem";
  patch?: Partial<SessionView>;
  run?: () => void;
} | null;

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
    { id: "show", label: t("listView.show"), value: t(v.metadata ? (v.nest ? "listView.metadataNested" : "listView.metadata") : v.nest ? "listView.nestedOnly" : "listView.titleOnly") },
  ];
  const hasCustomOrder = st.activeOrder.length > 0 || st.pinnedOrder.length > 0 || Object.keys(st.sectionOrder).length > 0;
  const choices: Record<ViewPane, ViewChoice[]> = {
    group: (Object.keys(groups) as SessionView["group"][]).map((g) => ({ label: groups[g], on: v.group === g, role: "menuitemradio", patch: { group: g } })),
    sort: [
      { label: t("listView.defaultSort"), on: v.sort === "default", role: "menuitemradio", patch: { sort: "default" } },
      { label: t("listView.created"), on: v.sort === "created", role: "menuitemradio", patch: { sort: "created" } },
      { label: t("listView.lastActive"), on: v.sort === "updated", role: "menuitemradio", patch: { sort: "updated" } },
      null,
      { label: t("listView.newestFirst"), on: v.newestFirst, role: "menuitemradio", patch: { newestFirst: true } },
      { label: t("listView.oldestFirst"), on: !v.newestFirst, role: "menuitemradio", patch: { newestFirst: false } },
      ...(hasCustomOrder
        ? [
            null,
            {
              label: t("listView.resetOrder"),
              on: false,
              role: "menuitem" as const,
              run: () => {
                st.set({ activeOrder: [], pinnedOrder: [], sectionOrder: {} });
                close(true);
              },
            },
          ]
        : []),
    ],
    show: [
      { label: t("listView.metadata"), on: v.metadata, role: "menuitemcheckbox", patch: { metadata: !v.metadata } },
      { label: t("listView.nest"), on: v.nest, role: "menuitemcheckbox", patch: { nest: !v.nest } },
    ],
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
              <button
                key={c.label}
                className="mi"
                role={c.role}
                aria-checked={c.role === "menuitem" ? undefined : c.on}
                tabIndex={-1}
                onMouseMove={(e) => e.currentTarget.focus()}
                onClick={() => (c.run ? c.run() : choose(c.patch ?? {}))}
              >
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
