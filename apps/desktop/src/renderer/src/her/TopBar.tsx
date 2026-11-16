import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { botWorking, routineDot } from "@hermesh/her-client";
import { Icon } from "../icons";
import { Avatar, BotAvatar } from "./Avatar";
import { DeliveriesButton } from "./Deliveries";
import { useHer } from "./context";
import { reachable } from "./environments";
import { menuKeys } from "./menus";
import { TagWords, TeamPanel, useTeam } from "../team";
import { useStore } from "../store";

/**
 * The Her page's top (docs/her-server.md §9.1), as on the iPhone: Her's avatar in the middle, larger than a bar's,
 * its name on a capsule over the avatar's lower edge, and in the capsule the environment as a quiet
 * menu when Her runs on more than one machine; the conversation scrolls under them. A click on the avatar or the name
 * opens Her's settings beside the conversation, as a tap does on the iPhone. On the right, Progress (the work she sent
 * out, docs/her-team.md §15), marked while a piece of it waits for the owner; Her's routines, which open beside the
 * conversation too; and ⋯: the environment's console, and another environment while there is no menu of them.
 */
export function TopBar() {
  const { t } = useTranslation("her");
  const her = useHer();
  const bot = her.bot;
  const panel = her.panel;
  const dot = bot ? routineDot(bot.routines) : null;
  const toggle = (kind: "settings" | "routines" | "files") => her.setPanel(panel?.kind === kind ? null : kind === "routines" ? { kind, route: {} } : { kind });
  const team = useTeam();
  const [teamOpen, setTeamOpen] = useState(false);
  const capsule = useRef<HTMLButtonElement>(null);
  const closeTeam = useCallback(() => {
    setTeamOpen(false);
    capsule.current?.focus();
  }, []);
  return (
    <div className="ctool drag her-head">
      <div className="her-id">
        {bot ? (
          <button className="her-id-av" title={t("head.settings")} aria-label={t("head.settings")} aria-pressed={panel?.kind === "settings"} onClick={() => toggle("settings")}>
            <BotAvatar bot={bot} size={56} busy={botWorking(bot)} />
          </button>
        ) : (
          <Avatar spec={{ kind: "mark" }} name="" size={56} />
        )}
        {/* The capsule is the team's name tag (docs/her-team.md §14.3): Her's name and what the team does; it opens the team panel. */}
        <button ref={capsule} className={`her-cap ${teamOpen ? "on" : ""}`} aria-expanded={teamOpen} aria-haspopup="dialog" aria-label={t("head.team")} onClick={() => setTeamOpen(!teamOpen)}>
          <span className="her-id-n">{bot?.name ?? "Her"}</span>
          <TagWords team={team} />
        </button>
      </div>
      {teamOpen && <TeamPanel onClose={closeTeam} />}
      <span className="bhead-gap" />
      {bot && <ProgressButton />}
      {bot && <DeliveriesButton bot={bot} open={panel?.kind === "files"} onClick={() => toggle("files")} />}
      {bot && (
        <button className={`tb rtn-btn ${panel?.kind === "routines" ? "on" : ""}`} title={t("routines.title")} aria-label={t("routines.title")} aria-pressed={panel?.kind === "routines"} onClick={() => toggle("routines")}>
          <Icon name="clock" />
          {dot && <i className={`rtn-dot ${dot === "running" ? "run" : ""}`} />}
        </button>
      )}
      <MoreMenu />
    </div>
  );
}

/**
 * Progress, a page of Her's (docs/her-team.md §15): an orange dot while a sub-agent of hers waits for the owner, else a
 * blue one while one is at work, as her page follows her conversation.
 */
function ProgressButton() {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const { waits, running } = st.herAgents ?? { waits: 0, running: 0 };
  const label = waits > 0 ? `${t("progress.title")} · ${t("progress.waiting", { count: waits })}`
    : running > 0 ? `${t("progress.title")} · ${t("progress.atWork", { count: running })}`
    : t("progress.title");
  return (
    <button className="tb rtn-btn" title={label} aria-label={label} onClick={() => st.set({ selection: { kind: "progress", machine: her.machine.machineId } })}>
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

function Item({ icon, children, onClick }: { icon: string; children: ReactNode; onClick: () => void }) {
  return (
    <button className="mi" role="menuitem" tabIndex={-1} onMouseMove={(e) => e.currentTarget.focus()} onClick={onClick}>
      <Icon name={icon} />
      <span>{children}</span>
    </button>
  );
}

/**
 * ⋯: the environments when Her runs on more than one machine (the one shown checked, whether each is online and
 * what is unread there; the capsule is the team's now, docs/her-team.md §14.3), the environment's console ("Manage
 * this environment"), and Add Environment.
 */
function MoreMenu() {
  const { t } = useTranslation("her");
  const her = useHer();
  const menu = useMenu("right");
  const m = her.machine;
  return (
    <>
      <button ref={menu.btn} className={`tb ${menu.open ? "on" : ""}`} title={t("page.more")} aria-label={t("page.more")} aria-haspopup="menu" aria-expanded={!!menu.open} onClick={menu.toggle}>
        <Icon name="more" />
      </button>
      {menu.open && (
        <div {...menu.props(t("page.more"))}>
          {her.envs.length > 1 && (
            <>
              {her.envs.map((e) => (
                <button key={e.machineId} className="mi" role="menuitemradio" aria-checked={e.machineId === m.machineId} tabIndex={-1} onMouseMove={(ev) => ev.currentTarget.focus()} onClick={() => (menu.close(), her.choose(e.machineId))}>
                  <span className="ck">{e.machineId === m.machineId && <Icon name="check" className="sm" />}</span>
                  <span className="two">
                    {e.machineName}
                    <span>{reachable(e) ? t("env.online") : t("env.offlineShort")}</span>
                  </span>
                  {(e.her?.unread ?? 0) > 0 && <span className="bbadge">{e.her!.unread}</span>}
                </button>
              ))}
              <div className="msep" role="separator" />
            </>
          )}
          <Item icon="gear" onClick={() => (menu.close(), her.openConsole())}>
            {t("page.manage", { name: m.machineName })}
          </Item>
          <Item icon="plus" onClick={() => (menu.close(), her.setSheet({ kind: "add" }))}>
            {t("page.addEnvironment")}
          </Item>
        </div>
      )}
    </>
  );
}
