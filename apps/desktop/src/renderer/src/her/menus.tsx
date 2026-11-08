import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { modelGroups } from "@hermesh/her-client";
import type { ModelsListResult, ThinkingLevel } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { useStore } from "../store";
import { useHer } from "./context";
import { menuModels, modelTitle, thinkingFor, thinkingTitle, type MenuModel } from "./format";

// The Her page's menus: opaque, under or over the
// button they opened from and aligned to its edge; the keyboard works as in a
// native menu.

/** Arrows, Home and End move between a menu's items; Esc and Tab close it (back to its button). */
export function menuKeys(e: KeyboardEvent, menu: HTMLElement | null, close: () => void) {
  e.stopPropagation();
  // The menu's own items, not those of a submenu open inside it.
  const all = [...(menu?.querySelectorAll<HTMLElement>("[role^=menuitem]:not(:disabled)") ?? [])].filter((el) => el.parentElement?.closest("[role=menu]") === menu);
  const i = all.indexOf(document.activeElement as HTMLElement);
  const go = { ArrowDown: (i + 1) % all.length, ArrowUp: i <= 0 ? all.length - 1 : i - 1, Home: 0, End: all.length - 1 }[e.key];
  if (go !== undefined) {
    e.preventDefault();
    all[go]?.focus();
  } else if (e.key === "Escape" || e.key === "Tab") {
    e.preventDefault();
    e.stopPropagation();
    close();
  }
}

/** A button that opens a menu over it (or under it, `below`), aligned to its left or right edge. */
export function MenuButton({ className, title, label, align = "left", below, disabled, children }: {
  className: string;
  title: string;
  label: ReactNode;
  align?: "left" | "right";
  below?: boolean;
  disabled?: boolean;
  children: (close: () => void) => ReactNode;
}) {
  const [at, setAt] = useState<{ x: number; y: number; key: boolean } | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const close = () => setAt(null);
  useLayoutEffect(() => {
    if (!at || !menu.current) return;
    const m = menu.current;
    const r = m.getBoundingClientRect();
    if (r.right > innerWidth - 8) m.style.left = `${Math.max(8, innerWidth - 8 - r.width)}px`;
    if (r.left < 8) m.style.left = "8px";
    m.querySelector("[aria-checked=true]")?.scrollIntoView({ block: "nearest" });
    (at.key ? (m.querySelector<HTMLElement>("[aria-checked=true]") ?? m.querySelector<HTMLElement>("[role^=menuitem]:not(:disabled)")) : m)?.focus();
  }, [at]);
  useEffect(() => {
    if (!at) return;
    const outside = (e: MouseEvent) => ![btn, menu].some((r) => r.current?.contains(e.target as Node)) && close();
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", close);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", close);
    };
  }, [at]);
  return (
    <>
      <button
        ref={btn}
        type="button"
        className={className}
        title={title}
        aria-label={title}
        aria-haspopup="menu"
        aria-expanded={!!at}
        disabled={disabled}
        onClick={(e) => {
          if (at) return close();
          const r = e.currentTarget.getBoundingClientRect();
          setAt({ x: align === "left" ? r.left : innerWidth - r.right, y: below ? r.bottom + 4 : innerHeight - r.top + 4, key: e.detail === 0 });
        }}
      >
        {label}
      </button>
      {at &&
        createPortal(
          <div
            className={`vmenu her-menu ${below ? "down" : ""}`}
            role="menu"
            aria-label={title}
            tabIndex={-1}
            ref={menu}
            style={{ [align]: at.x, [below ? "top" : "bottom"]: at.y, maxHeight: innerHeight - at.y - 8 }}
            onKeyDown={(e) => menuKeys(e, menu.current, () => (close(), btn.current?.focus()))}
          >
            {children(close)}
          </div>,
          document.body,
        )}
    </>
  );
}

/** A row that opens a menu beside it, showing its current value: hovering or →/↩ opens it, ←/Esc goes back. */
export function SubMenuItem({ label, value, title, children }: { label: string; value: string; title: string; children: ReactNode }) {
  const [open, setOpen] = useState<{ key: boolean } | null>(null);
  const [at, setAt] = useState<{ left: number; top: number } | null>(null);
  const row = useRef<HTMLButtonElement>(null);
  const sub = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!open || !row.current || !sub.current) return setAt(null);
    const menu = row.current.closest("[role=menu]")!;
    const m = menu.getBoundingClientRect();
    const r = row.current.getBoundingClientRect();
    const { offsetWidth: w, offsetHeight: h } = sub.current;
    const up = r.bottom + 5 - h;
    const top = menu.classList.contains("down") || up < m.top ? r.top - 5 : up;
    setAt({ left: m.right + w + 4 > innerWidth ? m.left - w - 2 : m.right + 2, top: Math.max(8, Math.min(top, innerHeight - 8 - h)) });
    if (open.key) (sub.current.querySelector<HTMLElement>("[aria-checked=true]") ?? sub.current.querySelector<HTMLElement>("[role^=menuitem]"))?.focus();
  }, [open]);
  const back = () => (setOpen(null), row.current?.focus());
  return (
    <div style={{ display: "contents" }} onBlur={(e) => !e.currentTarget.contains(e.relatedTarget as Node) && setOpen(null)}>
      <button
        ref={row}
        className={`mi ${open ? "open" : ""}`}
        role="menuitem"
        aria-haspopup="menu"
        aria-expanded={!!open}
        tabIndex={-1}
        onMouseMove={(e) => document.activeElement !== e.currentTarget && (e.currentTarget.focus(), setOpen({ key: false }))}
        onClick={() => setOpen({ key: false })}
        onKeyDown={(e) => {
          if (e.key !== "ArrowRight" && e.key !== "Enter" && e.key !== " ") return;
          e.preventDefault();
          e.stopPropagation();
          setOpen({ key: true });
        }}
      >
        <span className="ck" />
        <span>{label}</span>
        <span className="val">{value}</span>
        <Icon name="chevR" className="sm" />
      </button>
      {open && (
        <div
          className="vmenu her-menu"
          role="menu"
          aria-label={title}
          tabIndex={-1}
          ref={sub}
          style={at ? { left: at.left, top: at.top } : { opacity: 0, left: 0, top: 0 }}
          onKeyDown={(e) => {
            if (e.key === "ArrowLeft" || e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              return back();
            }
            menuKeys(e, sub.current, back);
          }}
        >
          {children}
        </div>
      )}
    </div>
  );
}

/** A menu item: an icon or a check, its words (and a line under them), a value at its right. */
export function MenuItem({ icon, checked, label, desc, value, disabled, danger, onClick }: {
  icon?: string;
  checked?: boolean;
  label: string;
  desc?: string;
  value?: string;
  disabled?: boolean;
  danger?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      className={`mi ${danger ? "danger" : ""}`}
      role={checked === undefined ? "menuitem" : "menuitemradio"}
      aria-checked={checked}
      tabIndex={-1}
      disabled={disabled}
      onMouseMove={(e) => e.currentTarget.focus()}
      onClick={onClick}
    >
      {icon ? <Icon name={icon} /> : checked !== undefined && <span className="ck">{checked && <Icon name="check" className="sm" />}</span>}
      {desc ? (
        <span className="two">
          {label}
          <span>{desc}</span>
        </span>
      ) : (
        <span>{label}</span>
      )}
      {value && <span className="val">{value}</span>}
    </button>
  );
}

/** The list shows about a dozen rows; past that it scrolls, and a search helps. */
const LONG = 12;

/**
 * A model menu's models, by provider: those the environment can use (its
 * model connections are in its console, which the last row opens).
 */
export function ModelList({ models, current, search = false, onPick, onClose }: {
  models: ModelsListResult;
  current?: string | null;
  search?: boolean;
  onPick: (m: MenuModel) => void;
  /** Closes the menu: the console opens instead. */
  onClose: () => void;
}) {
  const { t } = useTranslation("her");
  const her = useHer();
  const [query, setQuery] = useState("");
  const box = useRef<HTMLDivElement>(null);
  const find = useRef<HTMLInputElement>(null);
  const list = menuModels(models, current);
  const long = search && list.length > LONG;
  const groups = modelGroups(list, models.providers, query);
  const rows = () => [...(box.current?.querySelectorAll<HTMLElement>(".mlist [role^=menuitem]:not(:disabled)") ?? [])];
  useEffect(() => {
    if (!long) return;
    const menu = box.current?.closest<HTMLElement>("[role=menu]");
    if (!menu) return;
    menu.style.minWidth = `${menu.offsetWidth}px`;
    if (document.activeElement === menu) find.current?.focus();
  }, [long]);
  const typed = (e: KeyboardEvent) => {
    if (!long || e.target === find.current || e.metaKey || e.ctrlKey || e.altKey) return;
    if ((e.key.length === 1 && e.key !== " ") || e.key === "Backspace") find.current?.focus();
  };
  const fieldKeys = (e: KeyboardEvent) => {
    if (e.key === "ArrowUp" || e.key === "Tab" || (e.key === "Escape" && !query)) return;
    e.stopPropagation();
    if (e.key === "ArrowDown") rows()[0]?.focus();
    else if (e.key === "Enter") rows()[0]?.click();
    else if (e.key === "Escape") setQuery("");
    else return;
    e.preventDefault();
  };
  return (
    <div className="mmodels" ref={box} onKeyDown={typed}>
      {long && (
        <label className="mfind">
          <Icon name="search" className="sm" />
          <input ref={find} value={query} placeholder={t("model.find")} spellCheck={false} onChange={(e) => setQuery(e.target.value)} onKeyDown={fieldKeys} />
        </label>
      )}
      <div className="mlist">
        {groups.map((g) => (
          <div key={g.provider} role="group" aria-label={g.name}>
            <div className="mgh">{g.name}</div>
            {g.models.map((m) => (
              <MenuItem key={m.id} checked={m.id === current} label={m.name} value={m.note ? t(`model.${m.note}`) : undefined} onClick={() => onPick(m)} />
            ))}
          </div>
        ))}
        {groups.length === 0 && list.length > 0 && <div className="mempty">{t("model.noMatch")}</div>}
        {list.length === 0 && <div className="mempty">{t("model.noneHere", { name: her.machine.machineName })}</div>}
      </div>
      <div className="msep" role="separator" />
      <MenuItem icon="plus" label={t("model.add")} onClick={() => (onClose(), her.openConsole())} />
    </div>
  );
}

/** The model shown, the levels it offers, and switching to another, wherever a model is chosen. */
export function useModelSwitch({ model, thinking, models, onModel }: { model: string | null; thinking: ThinkingLevel; models: ModelsListResult; onModel: (ref: string) => ThinkingLevel | void }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const current = model ?? models.defaults?.model ?? null;
  const entry = models.models.find((m) => m.id === current);
  const levels: ThinkingLevel[] = entry?.thinking_levels ?? [];
  const twin = entry && models.models.some((m) => m.shown && m.id !== entry.id && m.name === entry.name);
  const title = current ? modelTitle(current, models) + (twin ? ` · ${models.providers.find((p) => p.id === entry.provider)?.name ?? entry.provider}` : "") : t("model.choose");
  const choose = (id: string) => {
    const next = models.models.find((m) => m.id === id);
    const after = onModel(id) ?? thinkingFor(next, thinking);
    if (id !== current && next && thinking !== "default" && after !== thinking) {
      st.show(t("model.thinkingChanged", { model: next.name, from: thinkingTitle(thinking), to: thinkingTitle(after) }));
    }
  };
  return { current, levels, title, choose };
}

/** "Default" (the provider decides) and the provider's own levels for this model. */
export function ThinkingItems({ thinking, levels, onThinking, close }: { thinking: ThinkingLevel; levels: ThinkingLevel[]; onThinking: (l: ThinkingLevel) => void; close: () => void }) {
  const { t } = useTranslation("her");
  return (
    <>
      <MenuItem checked={thinking === "default"} label={thinkingTitle("default")} value={t("model.thinkingDefault")} onClick={() => (close(), onThinking("default"))} />
      <div className="msep" role="separator" />
      {levels.map((l) => (
        <MenuItem key={l} checked={l === thinking} label={thinkingTitle(l)} onClick={() => (close(), onThinking(l))} />
      ))}
    </>
  );
}

/** The model an assistant answers with and how hard it thinks, wherever they are set: making it, or its settings. */
export function ModelRows({ model, thinking, below, onModel, onThinking }: {
  model: string | null;
  thinking: ThinkingLevel;
  /** Menus open under the rows (near the top of the window), not over them. */
  below?: boolean;
  onModel: (ref: string) => ThinkingLevel | void;
  onThinking: (l: ThinkingLevel) => void;
}) {
  const { t } = useTranslation("her");
  const her = useHer();
  const models = her.env.models;
  const { current, levels, title, choose } = useModelSwitch({ model, thinking, models, onModel });
  return (
    <div className="fgroup bmodel">
      <div className="frow">
        <span className="fl">{t("model.model")}</span>
        <div className="fv">
          <MenuButton className="mb" align="right" below={below} title={t("model.model")} label={<><span className="lbl strong">{title}</span><Icon name="chevD" /></>}>
            {(close) => <ModelList models={models} current={current} search onClose={close} onPick={(m) => (close(), choose(m.id))} />}
          </MenuButton>
        </div>
      </div>
      {levels.length > 0 && (
        <div className="frow">
          <span className="fl">{t("model.thinking")}</span>
          <div className="fv">
            <MenuButton className="mb" align="right" below={below} title={t("model.thinking")} label={<><span className="lbl strong">{thinkingTitle(thinking)}</span><Icon name="chevD" /></>}>
              {(close) => <ThinkingItems thinking={thinking} levels={levels} onThinking={onThinking} close={close} />}
            </MenuButton>
          </div>
        </div>
      )}
    </div>
  );
}

/** Why a model chosen for an assistant can't answer in its environment yet, with the way to its console; nothing when it can. */
export function ModelBlocked({ model }: { model: string | null }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const entry = her.env.models.models.find((m) => m.id === model);
  if (entry?.available || !her.env.botsLoaded) return null;
  return (
    <p className="set-note bmodel-why">
      {entry ? t("model.notHere", { name: her.machine.machineName }) : t("model.none")}{" "}
      <button type="button" className="link" onClick={her.openConsole}>
        {t("model.openConsole")}
      </button>
    </p>
  );
}
