import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { GitState, SessionStatus, type Session } from "@hermesh/proto/session";
import {
  clockText,
  codingAgentShort,
  codingAgentTitle,
  createdDate,
  dateOf,
  diffBarBlocks,
  displayTitle,
  elapsedSeconds,
  hasGitDeliveryBox,
   HER_SECTION,
  isActive,
  isFailed,
  isHerConversation,
  needsGitRefresh,
  SCRATCH_PROJECT,
  searchSessions,
  sessionFilter,
  sessionGitMark,
  shortTime,
  updatedDate,
  type SessionFilter,
} from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../shared/i18n";
import { sessions as sessionApi } from "./api";
import { Icon, type IconName } from "./icons";
import { reachable } from "./her/environments";
import { addActions, openAdd, type AddAction } from "./overview/actions";
import { keys } from "./platform";
import {
  buildSessionExcerpt,
  hasActiveSearchFilter,
  sessionBranch,
  shouldShowActions,
  splitHighlight,
  visibleFilterKeys,
} from "./searchModel";
import { grantLabel, machineName, senderName } from "./Sidebar";
import { useStore, type Store } from "./store";

const excerptOf = (s: Session) =>
  buildSessionExcerpt({
    waiting: s.status === SessionStatus.WAITING_INPUT,
    failed: isFailed(s),
    active: isActive(s),
    error: s.error,
    result: s.result,
    progress: s.progress,
  });

type FilterKey = "project" | "agent" | "machine" | "grant";

type Action = {
  id: AddAction | "openSettings";
  icon: IconName;
  label: string;
  shortcut: string;
  run: (st: Store) => void;
};

const actionIcons: Record<AddAction, IconName> = {
  newGrant: "key",
  addMachine: "laptop",
  addProject: "folder",
  enterCode: "link",
};

const actions = (): Action[] => [
  ...addActions.map((a) => ({
    id: a.id,
    icon: actionIcons[a.id],
    label: a.label,
    shortcut: a.shortcut,
    run: (st: Store) => openAdd(st, a.id),
  })),
  {
    id: "openSettings",
    icon: "gear",
    label: i18n.t("common:search.openSettings"),
    shortcut: keys("CmdOrCtrl+,"),
    run: () => window.hermesh?.openSettings(),
  },
];

function projectLabel(projectId: string): string {
  if (projectId === HER_SECTION) return i18n.t("common:sidebar.herSection");
  if (projectId === SCRATCH_PROJECT) return i18n.t("thread:launch.scratch");
  return projectId;
}

function Highlighted({ text, query }: { text: string; query: string }) {
  const parts = splitHighlight(text, query);
  if (parts.length === 0) return null;
  if (parts.length === 1 && !parts[0]!.match) return <>{text}</>;
  return (
    <>
      {parts.map((p, i) => (p.match ? <mark key={i} className="pal-hl">{p.text}</mark> : <span key={i}>{p.text}</span>))}
    </>
  );
}

// The ⌘K search palette: split-pane task & quick-action search with essential
// single-row popover filters, keyword highlighting, and a live preview pane.
export function SearchPalette({ leaving = false }: { leaving?: boolean }) {
  const st = useStore();
  const { t } = useTranslation("common");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<SessionFilter>(() => sessionFilter(st.searchFor));
  const [index, setIndex] = useState(0);
  const [openMenu, setOpenMenu] = useState<{ key: FilterKey; anchor: HTMLElement } | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const searchableText = (s: Session) => {
    const excerpt = excerptOf(s)?.text ?? "";
    return [
      displayTitle(s),
      projectLabel(s.projectId),
      s.projectId,
      grantLabel(st, s),
      senderName(st, s.createdBy),
      codingAgentTitle(s.agent),
      codingAgentShort(s.agent),
      machineName(st, s.machineId),
      sessionBranch(s),
      excerpt,
    ].join(" ");
  };

  const found = searchSessions(st.sessions, query, filter, searchableText);
  const hasFilter = hasActiveSearchFilter(filter);
  const q = query.trim().toLowerCase();
  const sessions = found.slice(0, q || hasFilter ? 40 : 12);
  const acts = shouldShowActions(query, filter)
    ? actions().filter((a) => q === "" || a.label.toLowerCase().includes(q))
    : [];
  const count = sessions.length + acts.length;
  const clampedIndex = count === 0 ? 0 : Math.min(index, count - 1);

  const close = () => st.set({ searching: false, searchFor: {} });
  const open = (i: number) => {
    const s = sessions[i];
    if (s) {
      if (s.status === SessionStatus.ARCHIVED) st.set({ archived: true });
      close();
      st.select(s.sessionId);
      return;
    }
    const a = acts[i - sessions.length];
    if (a) {
      close();
      a.run(st);
    }
  };

  // Refresh unsettled Git status on demand for the currently previewed session.
  const selectedSession = clampedIndex < sessions.length ? sessions[clampedIndex] : undefined;
  const gitRefreshed = useRef(new Map<string, number>());
  useEffect(() => {
    if (!selectedSession || !needsGitRefresh(selectedSession)) return;
    const id = selectedSession.sessionId;
    const nowMs = Date.now();
    if (nowMs - (gitRefreshed.current.get(id) ?? 0) < 5000) return;
    gitRefreshed.current.set(id, nowMs);
    sessionApi.refreshGitStatus({ sessionIds: [id] }).catch(() => {});
  }, [selectedSession?.sessionId]);

  // Capture keyboard while open; when a filter menu is open, let it handle its own keys first.
  useLayoutEffect(() => {
    if (leaving) return;
    const h = (e: KeyboardEvent) => {
      if (e.isComposing) return;
      if (openMenu) {
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          setOpenMenu(null);
          inputRef.current?.focus();
        }
        return;
      }
      if (e.key === "Escape") close();
      else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        if (count > 0) setIndex(Math.max(0, Math.min(count - 1, clampedIndex + (e.key === "ArrowDown" ? 1 : -1))));
      } else if (e.key === "Enter") open(clampedIndex);
      else return;
      e.preventDefault();
      e.stopPropagation();
    };
    document.addEventListener("keydown", h, true);
    return () => document.removeEventListener("keydown", h, true);
  });

  useEffect(() => {
    listRef.current?.querySelector(".sres.on")?.scrollIntoView({ block: "nearest" });
  }, [clampedIndex]);

  const change = (patch: Partial<SessionFilter>) => {
    setFilter((cur) => ({ ...cur, ...patch }));
    setIndex(0);
  };
  const resetFilters = () => {
    setFilter(sessionFilter());
    setOpenMenu(null);
    setIndex(0);
    inputRef.current?.focus();
  };

  const projects = useMemo(
    () => [...new Set([...st.projects.map((p) => p.projectId), ...st.sessions.map((s) => s.projectId)].filter(Boolean))].sort(),
    [st.projects, st.sessions],
  );
  const agents = useMemo(() => [...new Set(st.sessions.map((s) => s.agent).filter(Boolean))].sort(), [st.sessions]);

  const filterOptions: Record<FilterKey, { label: string; icon?: IconName; options: [string, string][] }> = {
    project: {
      label: t("filter.project"),
      icon: "folder",
      options: projects.map((p) => [p, projectLabel(p)]),
    },
    agent: {
      label: t("search.filterAgent"),
      options: agents.map((a) => [a, codingAgentTitle(a)]),
    },
    machine: {
      label: t("filter.machine"),
      icon: "laptop",
      options: st.machines.map((m) => [m.machineId, m.machineName]),
    },
    grant: {
      label: t("filter.grant"),
      icon: "key",
      options: [
        ...(filter.grant === "owner" ? [["owner", t("sidebar.you")] as [string, string]] : []),
        ...st.clients.map((c): [string, string] => [c.clientId, c.label]),
      ],
    },
  };

  const shownFilterKeys = visibleFilterKeys({
    projectsCount: projects.length,
    agentsCount: agents.length,
    machinesCount: st.machines.length,
    filter,
  });

  const now = new Date();
  const previewAction = clampedIndex >= sessions.length ? acts[clampedIndex - sessions.length] : undefined;

  return (
    <>
      <div className={`scrim ${leaving ? "leaving" : ""}`} onClick={close} />
      <div className={`pal ${leaving ? "leaving" : ""}`} inert={leaving} role="dialog" aria-label={t("search.title")}>
        <div className="pal-in">
          <Icon name="search" className="pal-in-ic" />
          <input
            ref={inputRef}
            autoFocus
            placeholder={t("search.placeholder")}
            aria-label={t("search.title")}
            autoComplete="off"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setIndex(0);
            }}
          />
          {query !== "" && (
            <button
              className="pal-clear"
              title={t("search.clearQuery")}
              aria-label={t("search.clearQuery")}
              onClick={() => {
                setQuery("");
                setIndex(0);
                inputRef.current?.focus();
              }}
            >
              <Icon name="x" className="sm" />
            </button>
          )}
          <button className="pal-kbd" onClick={close} aria-label={t("search.keyClose")}>esc</button>
        </div>

        <div className="pal-bar">
          <div className="pal-chips">
            {shownFilterKeys.map((key) => {
              const def = filterOptions[key];
              const val = filter[key];
              const activeLabel = val ? (def.options.find(([v]) => v === val)?.[1] ?? val) : "";
              const isOpen = openMenu?.key === key;
              return (
                <span key={key} className="pal-chip-wrap">
                  <button
                    className={`pal-chip ${val ? "active" : ""} ${isOpen ? "open" : ""}`}
                    aria-haspopup="menu"
                    aria-expanded={isOpen}
                    onClick={(e) => {
                      const anchor = e.currentTarget;
                      setOpenMenu((cur) => (cur?.key === key ? null : { key, anchor }));
                    }}
                  >
                    {def.icon && <Icon name={def.icon} className="xs" />}
                    <span>{val ? t("search.activeChip", { label: def.label, value: activeLabel }) : def.label}</span>
                    {!val && <Icon name="chevD" className="chev" />}
                  </button>
                  {val && (
                    <button
                      className="pal-chip-x"
                      title={t("search.clearFilter", { label: def.label })}
                      aria-label={t("search.clearFilter", { label: def.label })}
                      onClick={(e) => {
                        e.stopPropagation();
                        setOpenMenu(null);
                        change({ [key]: "" });
                        inputRef.current?.focus();
                      }}
                    >
                      <Icon name="x" className="xs" />
                    </button>
                  )}
                </span>
              );
            })}
          </div>
          <div className="pal-bar-r">
            {hasFilter && (
              <button className="pal-reset" onClick={resetFilters}>
                {t("search.resetFilters")}
              </button>
            )}
            <button
              className={`pal-chip ${filter.archived ? "active" : ""}`}
              role="checkbox"
              aria-checked={filter.archived}
              onClick={() => {
                setOpenMenu(null);
                change({ archived: !filter.archived });
              }}
            >
              <Icon name="archive" className="xs" />
              <span>{t("search.includeArchived")}</span>
            </button>
          </div>
        </div>

        <div className="pal-body">
          <div className="pal-r" ref={listRef} role="listbox" aria-label={t("search.results")}>
            {sessions.length > 0 ? (
              <div className="pal-h">
                <span>{t("search.found", { n: found.length })}</span>
              </div>
            ) : acts.length === 0 ? (
              <div className="pal-empty">
                <span className="pal-empty-ic"><Icon name="search" /></span>
                <b>{t("search.none")}</b>
                <p>{t("search.noneHint")}</p>
                <div className="pal-empty-acts">
                  {!filter.archived && (
                    <button className="pal-ebtn primary" onClick={() => change({ archived: true })}>
                      {t("search.includeArchived")}
                    </button>
                  )}
                  {(query !== "" || hasFilter) && (
                    <button
                      className="pal-ebtn"
                      onClick={() => {
                        setQuery("");
                        resetFilters();
                      }}
                    >
                      {t("search.clearAll")}
                    </button>
                  )}
                </div>
              </div>
            ) : (
              <div className="pal-h">
                <span>{t("search.none")}</span>
              </div>
            )}

            {sessions.map((s, i) => (
              <SessionResultRow
                key={s.sessionId}
                s={s}
                st={st}
                query={query}
                now={now}
                on={i === clampedIndex}
                onPick={() => open(i)}
                onHover={() => setIndex(i)}
              />
            ))}

            {acts.length > 0 && (
              <div className="pal-h">
                <span>{t("search.actions")}</span>
                <span className="pal-h-cnt">{acts.length}</span>
              </div>
            )}
            {acts.map((a, j) => {
              const i = sessions.length + j;
              return (
                <PaletteRow key={a.id} className="action" on={i === clampedIndex} onPick={() => open(i)} onHover={() => setIndex(i)}>
                  <span className="sres-av action-av">
                    <Icon name={a.icon} className="sm" />
                  </span>
                  <span className="sres-main">
                    <span className="sres-l1">
                      <span className="sres-title t"><Highlighted text={a.label} query={query} /></span>
                    </span>
                  </span>
                  <span className="sres-right">
                    {a.shortcut ? <kbd className="pal-kbd">{a.shortcut}</kbd> : <span className="sres-enter">↩</span>}
                  </span>
                </PaletteRow>
              );
            })}
          </div>

          <PreviewPane
            session={selectedSession}
            action={previewAction}
            st={st}
            query={query}
            now={now}
            onOpen={() => open(clampedIndex)}
          />
        </div>

        <div className="pal-foot">
          <div className="pal-foot-keys">
            <span className="pal-fk">
              <kbd className="pal-kbd sm">↑</kbd>
              <kbd className="pal-kbd sm">↓</kbd>
              {t("search.keySelect")}
            </span>
            <span className="pal-fk">
              <kbd className="pal-kbd sm">↩</kbd>
              {t("search.keyOpen")}
            </span>
            <span className="pal-fk">
              <kbd className="pal-kbd sm">esc</kbd>
              {t("search.keyClose")}
            </span>
          </div>
          <span className="pal-foot-cnt">{t("search.resultCount", { count })}</span>
        </div>
      </div>

      {openMenu && !leaving && (
        <FilterMenu
          label={filterOptions[openMenu.key].label}
          value={filter[openMenu.key]}
          options={filterOptions[openMenu.key].options}
          anchor={openMenu.anchor}
          onPick={(v) => {
            change({ [openMenu.key]: v });
            setOpenMenu(null);
            inputRef.current?.focus();
          }}
          onClose={() => {
            setOpenMenu(null);
            inputRef.current?.focus();
          }}
        />
      )}
    </>
  );
}

function statusPill(s: Session, t: TFunction<"common">): { cls: string; label: string } | null {
  if (s.status === SessionStatus.WAITING_INPUT) return { cls: "waiting", label: t("status.waiting") };
  if (isFailed(s)) return { cls: "failed", label: t(s.status === SessionStatus.CRASHED ? "status.crashed" : "status.failed") };
  if (s.status === SessionStatus.RUNNING) return { cls: "running", label: t("status.running") };
  if (isActive(s)) return { cls: "running", label: t(s.status === SessionStatus.QUEUED ? "status.queued" : "status.preparing") };
  if (s.status === SessionStatus.ARCHIVED) return { cls: "archived", label: t("search.archived") };
  return null;
}

function statusDotClass(s: Session): string | null {
  if (s.status === SessionStatus.WAITING_INPUT) return "waiting";
  if (isFailed(s)) return "failed";
  if (isActive(s)) return "running";
  if (s.status === SessionStatus.ARCHIVED) return "archived";
  return null;
}

function SessionResultRow({
  s,
  st,
  query,
  now,
  on,
  onPick,
  onHover,
}: {
  s: Session;
  st: Store;
  query: string;
  now: Date;
  on: boolean;
  onPick: () => void;
  onHover: () => void;
}) {
  const { t } = useTranslation("common");
  const title = displayTitle(s);
  const proj = isHerConversation(s) ? t("sidebar.herSection") : projectLabel(s.projectId);
  const sender = senderName(st, s.createdBy);
  const agent = codingAgentShort(s.agent);
  const machine = machineName(st, s.machineId);
  const pill = statusPill(s, t);
  const dot = statusDotClass(s);
  const gitMark = sessionGitMark(s);

  const q = query.trim().toLowerCase();
  const titleHit = q !== "" && title.toLowerCase().includes(q);
  const branch = sessionBranch(s);
  const branchHit = q !== "" && branch !== "" && branch.toLowerCase().includes(q);
  const excerpt = excerptOf(s);
  const excerptHit = q !== "" && !titleHit && !branchHit && !!excerpt && excerpt.text.toLowerCase().includes(q);

  const timeText = s.status === SessionStatus.RUNNING
    ? clockText(elapsedSeconds(dateOf(s.progress?.turnStartedAt) ?? createdDate(s), now))
    : shortTime(updatedDate(s), now);

  return (
    <PaletteRow className={s.status === SessionStatus.ARCHIVED ? "archived" : ""} on={on} onPick={onPick} onHover={onHover}>
      <span className="sres-av">
        <AgentLogo agent={s.agent} size={16} />
        {dot && <span className={`sres-dot ${dot}`} aria-hidden="true" />}
      </span>
      <span className="sres-main">
        <span className="sres-l1">
          <span className="sres-title t"><Highlighted text={title} query={query} /></span>
          {pill && <span className={`pal-pill ${pill.cls}`}>{pill.label}</span>}
        </span>
        <span className="sres-l2">
          <span className="sres-proj">
            <Icon name="folder" className="xs" />
            <Highlighted text={proj} query={query} />
          </span>
          <span className="sres-sep">·</span>
          <span>
            {isHerConversation(s) ? (
              <Highlighted text={t("sidebar.sentToHer")} query={query} />
            ) : (
              <>
                <Highlighted text={sender} query={query} /> → <Highlighted text={agent} query={query} />
              </>
            )}
          </span>
          {st.machines.length > 1 && (
            <>
              <span className="sres-sep">·</span>
              <span><Highlighted text={machine} query={query} /></span>
            </>
          )}
        </span>
        {branchHit && (
          <span className="sres-snip">
            {t("search.branch")} <code><Highlighted text={branch} query={query} /></code>
          </span>
        )}
        {excerptHit && excerpt && (
          <span className="sres-snip">
            <Highlighted text={excerpt.text} query={query} />
          </span>
        )}
      </span>
      <span className="sres-right">
        {gitMark === "pushed" && <span className="gmark pushed" role="img" aria-label={t("git.pushed")}><Icon name="gitPushed" /></span>}
        {gitMark === "merged" && <span className="gmark merged" role="img" aria-label={t("git.merged")}><Icon name="gitMerged" /></span>}
        <span className="sres-time">{timeText}</span>
        <span className="sres-enter" aria-hidden="true">↩</span>
      </span>
    </PaletteRow>
  );
}

function PreviewPane({
  session: s,
  action,
  st,
  query,
  now,
  onOpen,
}: {
  session?: Session;
  action?: Action;
  st: Store;
  query: string;
  now: Date;
  onOpen: () => void;
}) {
  const { t } = useTranslation("common");

  if (action) {
    return (
      <aside className="pal-pv" aria-label={t("search.preview")}>
        <div className="pal-pv-actcard">
          <span className="pal-pv-actic"><Icon name={action.icon} /></span>
          <b><Highlighted text={action.label} query={query} /></b>
          <p>{t(`search.actionDesc.${action.id}`)}</p>
          {action.shortcut && <kbd className="pal-kbd">{action.shortcut}</kbd>}
        </div>
        <div className="pal-pv-acts">
          <button className="pal-pv-btn primary" onClick={onOpen}>
            {t("search.runAction")} <span className="kret">↩</span>
          </button>
        </div>
      </aside>
    );
  }

  if (!s) {
    return (
      <aside className="pal-pv" aria-label={t("search.preview")}>
        <div className="pal-pv-actcard">
          <span className="pal-pv-actic muted"><Icon name="search" /></span>
          <b>{t("search.none")}</b>
          <p>{t("search.noneHint")}</p>
        </div>
      </aside>
    );
  }

  const title = displayTitle(s);
  const pill = statusPill(s, t);
  const sender = senderName(st, s.createdBy);
  const proj = isHerConversation(s) ? t("sidebar.herSection") : projectLabel(s.projectId);
  const machineObj = st.machines.find((m) => m.machineId === s.machineId);
  const machName = machineName(st, s.machineId);
  const machOnline = !machineObj || reachable(machineObj);
  const excerpt = excerptOf(s);
  const turns = s.progress?.turnCount ?? 0;

  const timeText = s.status === SessionStatus.RUNNING
    ? clockText(elapsedSeconds(dateOf(s.progress?.turnStartedAt) ?? createdDate(s), now))
    : shortTime(updatedDate(s), now);

  const gs = s.gitStatus;
  const gitState = gs?.state ?? GitState.UNSPECIFIED;
  const diff = s.progress?.diffStat;
  const ins = diff?.insertions ?? 0;
  const del = diff?.deletions ?? 0;
  const blocks = ins > 0 || del > 0 ? diffBarBlocks(ins, del) : null;

  let gitTone = "clean";
  let gitIcon: IconName = "check";
  let gitLabel = "";
  if (gitState === GitState.PUSHED) {
    gitTone = "pushed";
    gitIcon = "gitPushed";
    gitLabel = t("git.hoverPushed");
  } else if (gitState === GitState.MERGED) {
    gitTone = "merged";
    gitIcon = "gitMerged";
    gitLabel = t("git.hoverMerged", { target: gs?.targetRef || "main" });
  } else if (gitState === GitState.UNPUSHED) {
    gitTone = "unpushed";
    gitIcon = "gitUnpushed";
    gitLabel = t("git.hoverUnpushed", { count: Math.max(1, gs?.commitsAhead ?? 1) });
  } else if (gitState === GitState.DIRTY) {
    gitTone = "dirty";
    gitIcon = "gitDirty";
    gitLabel = t("git.hoverDirty", { count: Math.max(1, gs?.dirtyFiles ?? diff?.filesChanged ?? 1) });
  }

  const excerptHeading =
    excerpt?.kind === "waiting" ? t("search.excerptWaiting")
    : excerpt?.kind === "failed" ? t("search.excerptFailed")
    : excerpt?.kind === "running" ? t("search.excerptRunning")
    : t("search.excerptLatest");

  return (
    <aside className="pal-pv" aria-label={t("search.preview")}>
      <div className="pal-pv-head">
        <span className="sres-av lg"><AgentLogo agent={s.agent} size={18} /></span>
        <div className="pal-pv-hmain">
          <div className="pal-pv-title"><Highlighted text={title} query={query} /></div>
          <div className="pal-pv-sub">
            {pill && <span className={`pal-pill ${pill.cls}`}>{pill.label}</span>}
            <span>{isHerConversation(s) ? t("sidebar.sentToHer") : `${sender} → ${codingAgentShort(s.agent)}`}</span>
          </div>
        </div>
      </div>

      {excerpt && (
        <div className={`pal-pv-ex ${excerpt.kind}`}>
          <div className="pal-pv-exh">{excerptHeading}</div>
          <div className="pal-pv-ext"><Highlighted text={excerpt.text} query={query} /></div>
        </div>
      )}

      {hasGitDeliveryBox(s) && gitLabel && (
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
              <code className="shover-ref"><Highlighted text={gs.branch} query={query} /></code>
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
        </div>
      )}

      <div className="pal-pv-meta">
        <div className="pal-pv-kv">
          <span><Icon name="folder" className="xs" />{t("search.metaProject")}</span>
          <b>{proj}</b>
        </div>
        <div className="pal-pv-kv">
          <span><Icon name="laptop" className="xs" />{t("search.metaMachine")}</span>
          <b>{machOnline ? machName : t("search.offlineMachine", { name: machName })}</b>
        </div>
        <div className="pal-pv-kv">
          <span><Icon name="clock" className="xs" />{t("search.metaActive")}</span>
          <b>{turns > 0 ? `${timeText} · ${t("search.turns", { count: turns })}` : timeText}</b>
        </div>
      </div>

      <div className="pal-pv-acts">
        <button className="pal-pv-btn primary" onClick={onOpen}>
          {t("search.openTask")} <span className="kret">↩</span>
        </button>
        <button
          className="pal-pv-btn icon-only"
          title={t("sessionMenu.copyId")}
          aria-label={t("sessionMenu.copyId")}
          onClick={() => {
            navigator.clipboard?.writeText(s.sessionId).catch(() => {});
            st.show(t("search.copiedId"));
          }}
        >
          <Icon name="copy" className="sm" />
        </button>
      </div>
    </aside>
  );
}

function PaletteRow({
  className = "",
  on,
  onPick,
  onHover,
  children,
}: {
  className?: string;
  on: boolean;
  onPick: () => void;
  onHover: () => void;
  children: ReactNode;
}) {
  return (
    <button
      className={`sres ${className} ${on ? "on" : ""}`}
      role="option"
      aria-selected={on}
      tabIndex={-1}
      onClick={onPick}
      onMouseMove={on ? undefined : onHover}
    >
      {children}
    </button>
  );
}

function FilterMenu({
  label,
  value,
  options,
  anchor,
  onPick,
  onClose,
}: {
  label: string;
  value: string;
  options: [string, string][];
  anchor: HTMLElement;
  onPick: (value: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation("common");
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: -9999, top: 0 });

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = anchor.getBoundingClientRect();
    const top = r.bottom + 5 + el.offsetHeight > innerHeight - 8 ? Math.max(8, r.top - 5 - el.offsetHeight) : r.bottom + 5;
    const left = Math.max(8, Math.min(r.left, innerWidth - 8 - el.offsetWidth));
    setPos({ left, top });
    el.querySelector<HTMLElement>('[aria-checked="true"]')?.focus();
  }, [anchor, options.length]);

  useEffect(() => {
    const outside = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && !anchor.contains(e.target as Node) && onClose();
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", onClose);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", onClose);
    };
  }, [anchor, onClose]);

  const onKeyDown = (e: ReactKeyboardEvent) => {
    e.stopPropagation();
    const all = [...(ref.current?.querySelectorAll<HTMLElement>("[role=menuitemradio]") ?? [])];
    if (!all.length) return;
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

  return createPortal(
    <div className="vmenu pal-fmenu" role="menu" aria-label={label} tabIndex={-1} ref={ref} style={pos} onKeyDown={onKeyDown}>
      <button
        className="mi"
        role="menuitemradio"
        aria-checked={value === ""}
        tabIndex={-1}
        onMouseMove={(e) => e.currentTarget.focus()}
        onClick={() => onPick("")}
      >
        <span className="ck">{value === "" && <Icon name="check" className="sm" />}</span>
        <span>{t("search.allFilter")}</span>
      </button>
      {options.length > 0 && <div className="msep" role="separator" />}
      {options.map(([v, l]) => (
        <button
          key={v}
          className="mi"
          role="menuitemradio"
          aria-checked={value === v}
          tabIndex={-1}
          onMouseMove={(e) => e.currentTarget.focus()}
          onClick={() => onPick(v)}
        >
          <span className="ck">{value === v && <Icon name="check" className="sm" />}</span>
          <span className="pal-fmi-t">{l}</span>
        </button>
      ))}
    </div>,
    document.body,
  );
}
