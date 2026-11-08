import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { botWorking, routineDot } from "@hermesh/her-client";
import { Icon } from "../icons";
import { SidebarToggle } from "../MainWindow";
import { Avatar, BotAvatar } from "./Avatar";
import { useHer } from "./context";
import { reachable } from "./environments";
import { menuKeys } from "./menus";

/**
 * The Her page's top (docs/her-server.md §9.1): Her's avatar and name in the middle, as in a chat
 * app, and the environment beside them as a quiet menu when Her runs on more than one machine; on the
 * right, Her's routines and settings, which open beside the conversation, and ⋯: the environment's
 * console, and another environment while there is no menu of them.
 */
export function TopBar() {
  const { t } = useTranslation("her");
  const her = useHer();
  const bot = her.bot;
  const panel = her.panel;
  const dot = bot ? routineDot(bot.routines) : null;
  const toggle = (kind: "settings" | "routines") => her.setPanel(panel?.kind === kind ? null : kind === "settings" ? { kind } : { kind, route: {} });
  return (
    <div className="ctool drag her-head">
      <SidebarToggle />
      <div className="her-id">
        {bot ? <BotAvatar bot={bot} size={22} busy={botWorking(bot)} /> : <Avatar spec={{ kind: "mark" }} name="" size={22} />}
        <span className="her-id-n">{bot?.name ?? "Her"}</span>
        {her.envs.length > 1 && <EnvSwitch />}
      </div>
      <span className="bhead-gap" />
      {bot && (
        <>
          <button className={`tb rtn-btn ${panel?.kind === "routines" ? "on" : ""}`} title={t("routines.title")} aria-label={t("routines.title")} aria-pressed={panel?.kind === "routines"} onClick={() => toggle("routines")}>
            <Icon name="clock" />
            {dot && <i className={`rtn-dot ${dot === "running" ? "run" : ""}`} />}
          </button>
          <button className={`tb ${panel?.kind === "settings" ? "on" : ""}`} title={t("head.settings")} aria-label={t("head.settings")} aria-pressed={panel?.kind === "settings"} onClick={() => toggle("settings")}>
            <Icon name="toggles" />
          </button>
        </>
      )}
      <MoreMenu />
    </div>
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

/** The environment shown, beside Her's name; its menu lists every environment, whether it is online and what is unread there, then Add Environment. */
function EnvSwitch() {
  const { t } = useTranslation("her");
  const her = useHer();
  const m = her.machine;
  const menu = useMenu("left");
  const choose = (machineId: string) => {
    menu.close();
    her.choose(machineId);
  };
  return (
    <>
      <span className="her-id-sep" aria-hidden="true">·</span>
      <button ref={menu.btn} className={`her-env ${menu.open ? "on" : ""}`} aria-haspopup="menu" aria-expanded={!!menu.open} aria-label={t("page.environment", { name: m.machineName })} onClick={menu.toggle}>
        <span className="her-env-n">{m.machineName}</span>
        <Icon name="chevD" className="sm" />
      </button>
      {menu.open && (
        <div {...menu.props(t("page.environments"))}>
          {her.envs.map((e) => (
            <button key={e.machineId} className="mi" role="menuitemradio" aria-checked={e.machineId === m.machineId} tabIndex={-1} onMouseMove={(ev) => ev.currentTarget.focus()} onClick={() => choose(e.machineId)}>
              <span className="ck">{e.machineId === m.machineId && <Icon name="check" className="sm" />}</span>
              <span className="two">
                {e.machineName}
                <span>{reachable(e) ? t("env.online") : t("env.offlineShort")}</span>
              </span>
              {(e.her?.unread ?? 0) > 0 && <span className="bbadge">{e.her!.unread}</span>}
            </button>
          ))}
          <div className="msep" role="separator" />
          <Item icon="plus" onClick={() => (menu.close(), her.setSheet({ kind: "add" }))}>
            {t("page.addEnvironment")}
          </Item>
        </div>
      )}
    </>
  );
}

/** ⋯: the environment's console ("Manage this environment"), and Add Environment while there is no menu of environments. */
function MoreMenu() {
  const { t } = useTranslation("her");
  const her = useHer();
  const menu = useMenu("right");
  return (
    <>
      <button ref={menu.btn} className={`tb ${menu.open ? "on" : ""}`} title={t("page.more")} aria-label={t("page.more")} aria-haspopup="menu" aria-expanded={!!menu.open} onClick={menu.toggle}>
        <Icon name="more" />
      </button>
      {menu.open && (
        <div {...menu.props(t("page.more"))}>
          <Item icon="gear" onClick={() => (menu.close(), her.openConsole())}>
            {t("page.manage", { name: her.machine.machineName })}
          </Item>
          {her.envs.length === 1 && (
            <Item icon="plus" onClick={() => (menu.close(), her.setSheet({ kind: "add" }))}>
              {t("page.addEnvironment")}
            </Item>
          )}
        </div>
      )}
    </>
  );
}
