import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { botWorking, herStatus, routineDot, runningAgents, type SessionView } from "@hermesh/her-client";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { isArchivable } from "@hermesh/core";
import { Icon } from "../icons";
import { setSidebar } from "../layout";
import { TitleShine } from "../runningMotion";
import { archive, canArchive, confirmDelete, togglePin, unarchive } from "../Sidebar";
import { Avatar, BotAvatar } from "./Avatar";
import { useHer } from "./context";
import { reachable } from "./environments";
import { menuKeys } from "./menus";
import { TagWords, TeamPanel, useTeam } from "../team";
import { useNow, useStore } from "../store";

/**
 * The Her page's top (docs/her-server.md §9.1, docs/her-conversations.md §3.2), shared by Her's main conversation and
 * conversations opened from the task list: Her's avatar in the middle, larger than a bar's, its name on a capsule over
 * the avatar's lower edge, and the conversation scrolling under them. A click on the avatar opens her page beside the
 * conversation (docs/her-panel-demo.html), where her work, her abilities and her settings are; the avatar is marked
 * while a scheduled task's last run failed. The capsule is the team's: it opens the team panel. On the right Progress,
 * looked at often, marked while her work waits or runs; then ⋯: task actions (when in a task conversation), the
 * environments, the environment's console, and another environment.
 */
export function TopBar({ view, task }: { view?: SessionView | null; task?: Session } = {}) {
  const { t } = useTranslation("her");
  const her = useHer();
  const bot = her.bot;
  const panel = her.panel;
  const team = useTeam();
  const now = useNow(view?.live?.retry ? 1000 : 60_000).getTime();
  const status = view ? herStatus(view, now) : null;
  const statusText = status ? t(`line.${status}`) : null;
  const [teamOpen, setTeamOpen] = useState(false);
  const capsule = useRef<HTMLButtonElement>(null);
  const closeTeam = useCallback(() => {
    setTeamOpen(false);
    capsule.current?.focus();
  }, []);
  // A scheduled task's last run failed, which her page's card says; what waits in Progress, its button does.
  const waits = bot ? routineDot(bot.routines) === "failed" : false;
  const label = waits ? `${t("head.settings")} · ${t("head.waiting")}` : t("head.settings");
  const busy = task ? !!(view?.live || (view && runningAgents(view).length > 0)) : bot ? botWorking(bot) : false;
  return (
    <div className="ctool drag her-head">
      <div className="her-id">
        {bot ? (
          <button className="her-id-av" title={label} aria-label={label} aria-pressed={!!panel} data-testid="her-avatar" onClick={() => her.setPanel(panel ? null : { kind: "settings" })}>
            <BotAvatar bot={bot} size={56} busy={busy} />
            {waits && <i className="her-id-dot" aria-hidden="true" />}
          </button>
        ) : (
          <Avatar spec={{ kind: "mark" }} name="" size={56} />
        )}
        {/* The capsule is the team's name tag (docs/her-team.md §14.3): Her's name (or live status while she answers) and what the team does; it opens the team panel. */}
        <button ref={capsule} className={`her-cap ${teamOpen ? "on" : ""}`} aria-expanded={teamOpen} aria-haspopup="dialog" aria-label={t("head.team")} onClick={() => setTeamOpen(!teamOpen)}>
          {statusText ? <span className="her-id-n her-id-st stitle run" title={statusText}>{statusText}<TitleShine text={statusText} /></span> : <span className="her-id-n">{bot?.name ?? "Her"}</span>}
          <TagWords team={team} />
        </button>
      </div>
      {teamOpen && <TeamPanel onClose={closeTeam} />}
      <span className="bhead-gap" />
      {bot && <ProgressButton view={view} task={task} />}
      <MoreMenu task={task} />
    </div>
  );
}

/** Progress (docs/her-team.md §15): marked orange while a sub-agent of hers waits for the owner, else blue while one is at work. */
function ProgressButton({ view, task }: { view?: SessionView | null; task?: Session }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const counts = task
    ? (() => {
        const live = view ? runningAgents(view) : [];
        const waits = live.filter((a) => (a.view.live?.approvals.length ?? 0) > 0).length;
        return { waits, running: live.length - waits };
      })()
    : (st.herAgents ?? { waits: 0, running: 0 });
  const { waits, running } = counts;
  const label = waits > 0 ? `${t("progress.title")} · ${t("progress.waiting", { count: waits })}`
    : running > 0 ? `${t("progress.title")} · ${t("progress.atWork", { count: running })}`
    : t("progress.title");
  return (
    <button className="tb rtn-btn" title={label} aria-label={label} data-testid="her-progress" onClick={() => her.openProgress()}>
      <Icon name="pulse" />
      {(waits > 0 || running > 0) && <i className={`rtn-dot ${waits > 0 ? "" : "run"}`} />}
    </button>
  );
}

/** A menu under its button, from the button's left or right edge, opaque; the keyboard works as in a native menu. */
function useMenu(edge: "left" | "right") {
  const [open, setOpen] = useState<false | "mouse" | "key">(false);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState<{ left?: number; right?: number; top: number }>({ top: 0 });
  useEffect(() => {
    if (!open) return;
    (open === "key" ? menu.current?.querySelector<HTMLElement>("[role^=menuitem]") : menu.current)?.focus();
    const outside = (e: MouseEvent) => ![btn, menu].some((r) => r.current?.contains(e.target as Node)) && setOpen(false);
    const blur = () => setOpen(false);
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", blur);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", blur);
    };
  }, [open]);
  const toggle = (e: { currentTarget: HTMLElement; detail: number }) => {
    if (open) return setOpen(false);
    const r = e.currentTarget.getBoundingClientRect();
    setAt(edge === "left" ? { left: r.left, top: r.bottom + 4 } : { right: innerWidth - r.right, top: r.bottom + 4 });
    setOpen(e.detail ? "mouse" : "key");
  };
  const close = () => setOpen(false);
  const props = (label: string) => ({
    className: "vmenu her-menu down",
    role: "menu",
    "aria-label": label,
    tabIndex: -1,
    ref: menu,
    style: at,
    onKeyDown: (e: ReactKeyboardEvent) => menuKeys(e, menu.current, () => (close(), btn.current?.focus())),
  });
  return { open, btn, toggle, close, props };
}

function Item({ icon, children, disabled, title, danger, onClick }: { icon?: string; children: ReactNode; disabled?: boolean; title?: string; danger?: boolean; onClick: () => void }) {
  return (
    <button className={`mi ${danger ? "danger" : ""}`} role="menuitem" tabIndex={-1} disabled={disabled} title={title} onMouseMove={(e) => e.currentTarget.focus()} onClick={onClick}>
      {icon && <Icon name={icon} />}
      <span>{children}</span>
    </button>
  );
}

/**
 * ⋯: on a task conversation, its actions (pin, rename, archive, copy id, delete) and the environment's console; on
 * Her's main page, the environments when Her runs on more than one machine, the environment's console, and Add Environment.
 */
function MoreMenu({ task }: { task?: Session }) {
  const { t } = useTranslation(["her", "common"]);
  const st = useStore();
  const her = useHer();
  const menu = useMenu("right");
  const m = her.machine;
  const archived = task?.status === SessionStatus.ARCHIVED;
  const archivable = task ? archived || canArchive(st, task) : false;
  const waits = task ? !archived && isArchivable(task) && !canArchive(st, task) : false;
  return (
    <>
      <button ref={menu.btn} className={`tb ${menu.open ? "on" : ""}`} title={t("her:page.more")} aria-label={t("her:page.more")} aria-haspopup="menu" aria-expanded={!!menu.open} onClick={menu.toggle}>
        <Icon name="more" />
      </button>
      {menu.open && (
        <div {...menu.props(t("her:page.more"))}>
          {task && (
            <>
              {!archived && (
                <Item onClick={() => (menu.close(), togglePin(st, task))}>
                  {t(task.pinned ? "common:sessionMenu.unpin" : "common:sessionMenu.pin")}
                </Item>
              )}
              <Item onClick={() => (menu.close(), setSidebar(st, true), st.set({ renaming: task.sessionId }))}>
                {t("common:sessionMenu.rename")}
              </Item>
              <Item
                disabled={!archivable}
                title={waits ? t("common:sessionMenu.stillRunning") : undefined}
                onClick={() => (menu.close(), archived ? void unarchive(st, task) : archive(st, task))}
              >
                {t(archived ? "common:sessionMenu.unarchive" : "common:sessionMenu.archive")}
              </Item>
              <Item onClick={() => (menu.close(), void navigator.clipboard.writeText(task.sessionId))}>
                {t("common:sessionMenu.copyId")}
              </Item>
              <Item danger onClick={() => (menu.close(), confirmDelete(st, task))}>
                {t("common:sessionMenu.delete")}
              </Item>
              <div className="msep" role="separator" />
            </>
          )}
          {her.envs.length > 1 && !her.conversation && (
            <>
              {her.envs.map((e) => (
                <button key={e.machineId} className="mi" role="menuitemradio" aria-checked={e.machineId === m.machineId} tabIndex={-1} onMouseMove={(ev) => ev.currentTarget.focus()} onClick={() => (menu.close(), her.choose(e.machineId))}>
                  <span className="ck">{e.machineId === m.machineId && <Icon name="check" className="sm" />}</span>
                  <span className="two">
                    {e.machineName}
                    <span>{reachable(e) ? t("her:env.online") : t("her:env.offlineShort")}</span>
                  </span>
                  {(e.her?.unread ?? 0) > 0 && <span className="bbadge">{e.her!.unread}</span>}
                </button>
              ))}
              <div className="msep" role="separator" />
            </>
          )}
          <Item icon="gear" onClick={() => (menu.close(), her.openConsole())}>
            {t("her:page.manage", { name: m.machineName })}
          </Item>
          {!her.conversation && (
            <Item icon="plus" onClick={() => (menu.close(), her.setSheet({ kind: "add" }))}>
              {t("her:page.addEnvironment")}
            </Item>
          )}
        </div>
      )}
    </>
  );
}
