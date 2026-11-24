import { useEffect, useLayoutEffect, useMemo, useRef, useState, type FocusEvent, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { codingAgentTitle, displayTitle, isActive, isFailed, liveByAgent, teamState } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { Cat } from "./her/Cat";
import { unreadCount } from "./her/environments";
import { Icon } from "./icons";
import { setSidebar } from "./layout";
import { AppMenuButton } from "./MainWindow";
import { keys, ownKeys } from "./platform";
import { AccountMenu, overviewMarks, RowMark } from "./Sidebar";
import { useNow, useStore } from "./store";

// The sidebar's rail (docs/her-team.md §16): Her and the overview, then one logo for each coding agent with
// sessions at hand (waiting for the owner, failed, at work), 74 px wide. The rail says who is at work; the
// sidebar, expanded, says on what. Each button says on hover (or focus) what it is and how it stands.

/** What a rail button says beside it while the pointer or the focus is on it. */
type Tip = { text: string; top: number } | null;

/** An agent's menu of its sessions, open beside its logo; key: opened from the keyboard. */
type AgentMenuAt = { agent: string; top: number; key: boolean; anchor: HTMLElement };

export function Rail() {
  const st = useStore();
  const { t } = useTranslation("common");
  const now = useNow(30_000);
  const [tip, setTip] = useState<Tip>(null);
  const [menu, setMenu] = useState<AgentMenuAt | null>(null);
  const show = (text: string) => ({
    onMouseEnter: (e: MouseEvent<HTMLElement>) => setTip({ text, top: e.currentTarget.getBoundingClientRect().top + e.currentTarget.offsetHeight / 2 }),
    onFocus: (e: FocusEvent<HTMLElement>) => setTip({ text, top: e.currentTarget.getBoundingClientRect().top + e.currentTarget.offsetHeight / 2 }),
    onMouseLeave: () => setTip(null),
    onBlur: () => setTip(null),
  });
  const sel = st.selection;
  const chosen = sel.kind === "session" ? sel.id : "";
  const owner = !st.status?.machineOnly;
  const unread = unreadCount(st.machines);
  const herOn = st.machines.some((m) => m.her?.enabled);
  const marks = overviewMarks(st, now);
  const groups = useMemo(() => liveByAgent(teamState(st.sessions, 0)), [st.sessions]);
  const button = (o: { label: string; current: boolean; onClick: (e: MouseEvent<HTMLButtonElement>) => void; small?: boolean; open?: boolean; children: ReactNode; marks?: ReactNode }) => (
    <button
      className={`rb ${o.small ? "small" : ""} ${o.current ? "sel" : ""} ${o.open ? "open" : ""}`}
      aria-label={o.label}
      aria-current={o.current || undefined}
      aria-haspopup={o.open === undefined ? undefined : "menu"}
      aria-expanded={o.open}
      onClick={o.onClick}
      {...show(o.label)}
    >
      {o.children}
      {o.marks}
    </button>
  );
  /** How an agent's sessions stand, in words: waiting, failed, at work. */
  const words = (list: Session[]) => {
    const waiting = list.filter((s) => s.status === SessionStatus.WAITING_INPUT).length;
    const failed = list.filter(isFailed).length;
    const running = list.filter(isActive).length;
    return [waiting ? t("rail.waiting", { count: waiting }) : "", failed ? t("rail.failed", { count: failed }) : "", running ? t("sidebar.running", { count: running }) : ""].filter(Boolean);
  };
  const herLabel = !herOn ? t("rail.herOff") : unread > 0 ? t("sidebar.herUnread", { count: unread }) : "Her";
  const overviewLabel = [t("sidebar.overview"), marks.waits ? t("sidebar.attention", { count: marks.waits }) : "", marks.running ? t("sidebar.running", { count: marks.running }) : ""].filter(Boolean).join(" · ");
  const open = menu && groups.find((g) => g.agent === menu.agent);
  return (
    <aside className="rail" aria-label={t("rail.label")}>
      <div className="rail-top drag">
        <AppMenuButton />
      </div>
      <nav className="rail-nav" onScroll={() => (setTip(null), setMenu(null))}>
        {owner &&
          button({
            label: herLabel,
            current: sel.kind === "her" || sel.kind === "progress",
            onClick: () => sel.kind !== "her" && st.set({ selection: { kind: "her" } }),
            children: <span className="rb-cat" aria-hidden="true"><Cat small /></span>,
            marks: herOn ? unread > 0 && <i className="rb-dot u" aria-hidden="true" /> : <span className="rb-off" aria-hidden="true" />,
          })}
        {button({
          label: overviewLabel,
          current: sel.kind === "overview",
          onClick: () => st.set({ selection: { kind: "overview" } }),
          children: <Icon name="grid" />,
          marks: (
            <>
              {marks.waits > 0 && <i className="rb-dot" aria-hidden="true" />}
            </>
          ),
        })}
        {groups.length > 0 && <div className="rail-sep" role="separator" />}
        {groups.map((g) => (
          <div key={g.agent}>
            {button({
              label: [codingAgentTitle(g.agent), ...words(g.sessions)].join(" · "),
              current: g.sessions.some((s) => s.sessionId === chosen),
              small: true,
              open: g.sessions.length > 1 ? menu?.agent === g.agent : undefined,
              // One session: it opens. More: a menu of them beside the logo.
              onClick: (e) => {
                if (g.sessions.length === 1) return (setMenu(null), st.select(g.sessions[0].sessionId));
                const anchor = e.currentTarget;
                setTip(null);
                setMenu(menu?.agent === g.agent ? null : { agent: g.agent, top: anchor.getBoundingClientRect().top, key: e.detail === 0, anchor });
              },
              children: <AgentLogo agent={g.agent} size={22} />,
              marks: (
                <>
                  {g.sessions.some((s) => s.status === SessionStatus.WAITING_INPUT) ? <i className="rb-dot" aria-hidden="true" /> : g.sessions.some(isFailed) && <i className="rb-dot r" aria-hidden="true" />}
                  {g.sessions.some(isActive) && <span className="rb-busy" aria-hidden="true"><i /></span>}
                </>
              ),
            })}
          </div>
        ))}
      </nav>
      <div className="rail-foot">
        <button className="rb" aria-label={t("sidebar.search")} onClick={() => st.set({ searching: true })} {...show(`${t("sidebar.search")} ${keys("CmdOrCtrl+K")}`)}>
          <Icon name="search" />
        </button>
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
      {menu && open && <AgentMenu at={menu} sessions={open.sessions} onClose={(back) => (setMenu(null), back && menu.anchor.focus())} />}
    </aside>
  );
}

/**
 * An agent's sessions at hand, beside its logo in the rail: its name, then each session's title and mark; a click
 * opens one. The keyboard works as in a native menu (arrows move, Esc closes back to the logo).
 */
function AgentMenu({ at, sessions, onClose }: { at: AgentMenuAt; sessions: Session[]; onClose: (back: boolean) => void }) {
  const st = useStore();
  const ref = useRef<HTMLDivElement>(null);
  const [top, setTop] = useState(at.top);
  // Kept inside the window: up from the logo when there is no room under it.
  useLayoutEffect(() => {
    const h = ref.current!.offsetHeight;
    setTop(Math.max(8, Math.min(at.top, innerHeight - 8 - h)));
  }, [at.top, sessions.length]);
  useEffect(() => {
    (at.key ? ref.current?.querySelector<HTMLElement>("[role=menuitem]") : ref.current)?.focus();
    // A click on the logo itself is its own: it closes the menu.
    const outside = (e: globalThis.MouseEvent) => !ref.current?.contains(e.target as Node) && !at.anchor.contains(e.target as Node) && onClose(false);
    const blur = () => onClose(false);
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", blur);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", blur);
    };
  }, [at]);
  const onKeyDown = (e: ReactKeyboardEvent) => {
    const all = [...(ref.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
    const i = all.indexOf(document.activeElement as HTMLElement);
    const go = { ArrowDown: (i + 1) % all.length, ArrowUp: i <= 0 ? all.length - 1 : i - 1, Home: 0, End: all.length - 1 }[e.key];
    if (go !== undefined) {
      e.preventDefault();
      all[go]?.focus();
    } else if (e.key === "Escape" || e.key === "Tab") {
      e.preventDefault();
      e.stopPropagation();
      onClose(true);
    }
  };
  const name = codingAgentTitle(at.agent);
  return (
    <div className="vmenu rmenu" role="menu" aria-label={name} tabIndex={-1} ref={ref} style={{ top }} onKeyDown={onKeyDown}>
      <div className="rmenu-h" aria-hidden="true">{name}</div>
      {sessions.map((s) => (
        <button key={s.sessionId} className="mi" role="menuitem" tabIndex={-1} onMouseMove={(e) => e.currentTarget.focus()} onClick={() => (onClose(false), st.select(s.sessionId))}>
          <span className="rmenu-t">{displayTitle(s)}</span>
          <RowMark s={s} />
        </button>
      ))}
    </div>
  );
}
