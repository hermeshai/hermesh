import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import { RuntimeMode } from "@hermesh/proto/identity";
import type { Machine, Project } from "@hermesh/proto/local";
import type { AttachmentRef, ModelSelection, ProviderInstance } from "@hermesh/proto/session";
import {
  ORDERED_RUNTIME_MODES,
  SCRATCH_PROJECT,
  findModels,
  findProviderInstance,
  isRuntimeModeAllowed,
  providerUnusable,
  reasoningOf,
  selectModel,
  selectionTitle,
  withReasoning,
  type Reasoning,
} from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import { MenuButton } from "../her/menus";
import { Tile, useFileDrop } from "../her/Composer";
import { pastedFiles } from "../her/attachments";
import { attachmentUrl } from "./attachments";
import { openAttachmentLightbox } from "./files/ImageLightbox";
import "../her/her.css";

// The composer (docs/thread-composer.md §16, §17), a new session's and a
// session's: Her's box, with what the session runs with inside it (＋ for
// files and the permission mode, the model, how hard it thinks); over a new
// session's box where it works and on which branch.

/**
 * The box (docs/thread-composer.md §17): the text, growing with what is written, over a row that holds ＋, the model
 * and how hard it thinks, Stop and Send. It is always both: it does not fold to one line while empty.
 */
export function ThreadComposer({ value, onChange, onSend, onStop, running, sending, placeholder, attachments, onAttach, onRemove, offline, blocked = false, mode, maxCeiling, onMode, model }: {
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  onStop?: () => void;
  running: boolean;
  sending: boolean;
  placeholder: string;
  attachments: AttachmentRef[];
  /** Files picked from ＋, pasted into the box or dropped on the window. */
  onAttach: (files: File[]) => void;
  onRemove: (sha: string) => void;
  /** The machine is offline: Send queues it. */
  offline: boolean;
  /** Nothing can be sent there (its Hermesh needs an update). */
  blocked?: boolean;
  /** The permission mode, chosen from ＋. */
  mode: RuntimeMode;
  maxCeiling: RuntimeMode;
  onMode: (m: RuntimeMode) => void;
  /** The model and how hard it thinks (ModelButtons). */
  model: ReactNode;
}) {
  const { t } = useTranslation("thread");
  const area = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);
  const ready = !sending && !blocked && (value.trim() !== "" || attachments.length > 0);
  const shellShortcut = /^[\t ]*!(?!\[)/.test(value);
  // Files dragged anywhere over the window come here, as to Her's box.
  const dropping = useFileDrop((files) => (onAttach(files), area.current?.focus()));
  // The text grows with what is written, up to ten lines, then scrolls (as Her's).
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [value]);
  // Typing anywhere on the page (not in another field, no shortcut) goes into the box.
  useEffect(() => {
    const type = (e: globalThis.KeyboardEvent) => {
      const el = document.activeElement;
      if (e.metaKey || e.ctrlKey || e.altKey || e.key.length !== 1 || e.isComposing) return;
      if (/^[1-9]$/.test(e.key) && document.querySelector('[data-testid="pending-question-banner"]')) return;
      if (el && el !== document.body && (el.closest("input,textarea,select,[contenteditable],[role=menu],[role=dialog]") || el.matches("input,textarea"))) return;
      area.current?.focus();
    };
    window.addEventListener("keydown", type, true);
    return () => window.removeEventListener("keydown", type, true);
  }, []);
  const sendTitle = running ? t("composer.queue") : offline ? t("composer.queueOffline") : t("composer.send");
  const plus = (
    <MenuButton
      className="cmp-plus"
      title={t("composer.attach")}
      label={<Icon name="plus" />}
    >
      {(close) => (
        <div className="th-modes" data-testid="composer-plus-menu">
          <Item icon={<Icon name="doc" />} label={t("composer.addAttachment")} testId="composer-attach" onClick={() => (close(), file.current?.click())} />
          <div className="msep" role="separator" />
          <div className="mgh">{t("mode.title")}</div>
          {ORDERED_RUNTIME_MODES.map((m) => {
            const allowed = isRuntimeModeAllowed(m, maxCeiling);
            return (
              <Item
                key={m}
                checked={mode === m}
                label={t(`mode.${modeKey(m)}`)}
                desc={allowed ? t(`mode.${modeKey(m)}Hint`) : t("mode.ceilingMachine")}
                disabled={!allowed}
                testId={`runtime-mode-${m}`}
                onClick={() => (close(), onMode(m))}
              />
            );
          })}
        </div>
      )}
    </MenuButton>
  );
  return (
    <div
      className={`cmp th-cmp ${dropping ? "dropping" : ""} ${shellShortcut ? "shell-mode" : ""}`}
      data-testid="thread-composer"
      onMouseDown={(e) => e.target instanceof HTMLElement && e.target.matches(".cmp,.cmp-row,.cmp-att,.th-bar") && (e.preventDefault(), area.current?.focus())}
      // A screenshot, a picture or files copied in Finder are attached, as from ＋; words paste as words (as Her's).
      onPasteCapture={(e) => {
        const files = pastedFiles(e.clipboardData);
        if (!files.length) return;
        e.preventDefault();
        e.stopPropagation();
        onAttach(files);
      }}
    >
      {dropping && (
        <div className="cmp-drop" aria-hidden="true">
          <b>{t("composer.dropTitle")}</b>
          <span className="cmp-drop-hint">{t("composer.dropHint")}</span>
        </div>
      )}
      <input ref={file} type="file" multiple hidden onChange={(e) => (e.target.files && onAttach([...e.target.files]), (e.target.value = ""))} />
      {attachments.length > 0 && (
        <div className="cmp-att">
          {attachments.map((a) => {
            const src = attachmentUrl(a);
            // Only pictures already here: nothing in the composer is read from a machine.
            const pictures = attachments.filter((x) => attachmentUrl(x));
            return (
              <Tile
                key={a.blobSha256}
                name={a.name}
                src={src}
                onOpen={src ? () => openAttachmentLightbox("", "", pictures, pictures.indexOf(a)) : undefined}
                onRemove={() => onRemove(a.blobSha256)}
              />
            );
          })}
        </div>
      )}
      <div className="cmp-row">
        <textarea
          ref={area}
          className="cmp-in"
          rows={1}
          value={value}
          placeholder={placeholder}
          aria-label={placeholder}
          spellCheck={false}
          data-testid="thread-composer-input"
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || e.shiftKey || e.altKey || e.metaKey || e.ctrlKey || e.nativeEvent.isComposing) return;
            e.preventDefault();
            if (ready) onSend();
          }}
        />
      </div>
      <div className="th-bar" data-testid="composer-bar">
        {plus}
        {model}
        {shellShortcut && (
          <span className="cmp-shell-pill" data-testid="composer-shell-badge" title={t("composer.shellHint")}>
            <Icon name="terminal" />
            <span>{t("composer.shellBadge")}</span>
          </span>
        )}
        <span className="th-grow" />
        {running && onStop && (
          <button className="th-stop" title={t("composer.stop")} aria-label={t("composer.stop")} data-testid="composer-stop-btn" onClick={onStop}>
            <Icon name="stop" />
          </button>
        )}
        <button className="send" disabled={!ready} title={sendTitle} aria-label={sendTitle} data-testid="composer-submit-btn" onClick={onSend}>
          <Icon name="arrowUp" />
        </button>
      </div>
    </div>
  );
}

/** A menu row: a check, its words (and a line under them), a value at its right. */
function Item({ checked, label, desc, value, disabled, testId, icon, onClick }: {
  checked?: boolean;
  label: ReactNode;
  desc?: string;
  value?: ReactNode;
  disabled?: boolean;
  testId?: string;
  icon?: ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      className="mi"
      role={checked === undefined ? "menuitem" : "menuitemradio"}
      aria-checked={checked}
      tabIndex={-1}
      disabled={disabled}
      data-testid={testId}
      onMouseMove={(e) => e.currentTarget.focus()}
      onClick={onClick}
    >
      {checked !== undefined && <span className="ck">{checked && <Icon name="check" className="sm" />}</span>}
      {icon}
      {desc ? (
        <span className="two">
          {label}
          <span>{desc}</span>
        </span>
      ) : (
        <span>{label}</span>
      )}
      {value !== undefined && <span className="val">{value}</span>}
    </button>
  );
}

export function machineIcon(m?: Machine): string {
  if (!m || m.local) return "laptop";
  return m.status?.platform.startsWith("darwin") ? "mini" : "server";
}

/** The thinking level's name: ours for the levels we know, else the provider's. */
function levelTitle(id: string, label: string): string {
  return i18n.t(`thread:picker.level.${id}`, { defaultValue: label });
}

export interface Choice {
  machineId: string;
  projectId: string;
  worktree: boolean;
  sel: ModelSelection;
  mode: RuntimeMode;
  plan: boolean;
}

type Pickers = {
  machines: Machine[];
  projects: Project[];
  /** The machine is offline: its projects (and models) are those last read there. */
  projectsStale?: boolean;
  choice: Choice;
  onMachine: (id: string) => void;
  onProject: (id: string) => void;
  onAddProject: () => void;
};

/** A new session's question: the pickers, and under them what happens once it is sent (`after`, kept out of the layout). */
type LaunchAskProps = Pickers & { after?: ReactNode; /** Who it goes to, above the question (Presence.tsx). */ top?: ReactNode; /** The question rises into place: the owner just switched to the agent. */ rise?: boolean };

/**
 * A new session's question, in the middle of its page: "What should we build in <project>?", and under it the
 * environment it runs on. Both are menus (opening below), set as last time (choices.ts).
 */
export function LaunchAsk({ machines, projects, projectsStale, choice, onMachine, onProject, onAddProject, after, top, rise }: LaunchAskProps) {
  const { t } = useTranslation("thread");
  const machine = machines.find((m) => m.machineId === choice.machineId);
  const machineName = machine?.machineName ?? choice.machineId;
  const here = projects.filter((p) => p.machineId === choice.machineId);
  const projectName = choice.projectId === SCRATCH_PROJECT ? t("launch.scratch") : choice.projectId;
  const canAdd = !!machine && (machine.online || machine.local);
  const project = (
    <MenuButton
      className="th-pick"
      below
      title={t("launch.project")}
      label={
        <>
          <span className="lbl" data-testid="launch-project-chip">{projectName}</span>
          <Icon name="chevD" />
        </>
      }
    >
      {(close) => (
        <>
          <div className="mgh">
            {t("launch.projectsOn", { machine: machineName })}
            {projectsStale && ` · ${t("launch.lastKnown")}`}
          </div>
          {here.map((p) => (
            <Item key={p.projectId} checked={p.projectId === choice.projectId} label={p.projectId} desc={p.path} testId={`project-item-${p.projectId}`} onClick={() => (close(), onProject(p.projectId))} />
          ))}
          <Item checked={choice.projectId === SCRATCH_PROJECT} label={t("launch.scratch")} testId="project-item-scratch" onClick={() => (close(), onProject(SCRATCH_PROJECT))} />
          <div className="msep" role="separator" />
          <Item icon={<Icon name="plus" />} label={t("launch.addProject")} disabled={!canAdd} testId="project-add" onClick={() => (close(), onAddProject())} />
        </>
      )}
    </MenuButton>
  );
  return (
    <div className="th-ask">
      {top}
      <h1 key="agent" className={rise ? "pr-rise" : undefined}>
        <Trans t={t} i18nKey="launch.ask" components={{ project }} />
      </h1>
      <MenuButton
        className={`th-on ${rise ? "pr-rise" : ""}`}
        below
        title={t("launch.environment")}
        label={
          <>
            <Icon name={machineIcon(machine)} />
            <span className="lbl" data-testid="launch-machine-chip">{machineName}</span>
            {machine && !machine.online && !machine.local && <i className="th-off" />}
            <Icon name="chevR" />
          </>
        }
      >
        {(close) => (
          <>
            <div className="mgh">{t("launch.environment")}</div>
            {machines.map((m) => (
              <Item
                key={m.machineId}
                checked={m.machineId === choice.machineId}
                icon={<Icon name={machineIcon(m)} />}
                label={m.machineName}
                desc={m.online || m.local ? undefined : t("launch.offlineQueues")}
                testId={`machine-item-${m.machineName}`}
                onClick={() => (close(), onMachine(m.machineId))}
              />
            ))}
          </>
        )}
      </MenuButton>
      {after && <div className="th-ask-after">{after}</div>}
    </div>
  );
}

const modeKey = (m: RuntimeMode) => (m === RuntimeMode.SUPERVISED ? "supervised" : m === RuntimeMode.AUTO_EDITS ? "autoEdits" : m === RuntimeMode.FULL_ACCESS ? "fullAccess" : "auto");

/** A repository's branches as its folder says (ListFolders): the one checked out, the default, those other worktrees have. */
export interface Branches {
  current: string;
  default: string;
  all: string[];
  inWorktrees: string[];
}

/** Where a new session works, over the box: its folder or a worktree of its own; and on which branch. */
export interface Place {
  worktree: boolean;
  /** A worktree's base; empty: the default branch. */
  base: string;
  /** In the folder, the branch to work on; empty: the one checked out. */
  on: string;
  fromRemote: boolean;
}

/**
 * Over a new session's box (§16): the working copy, and the branch (a worktree's base, or the branch the folder works
 * on). Only for a repository; the branch only once its folder was read.
 */
export function ComposerPlace({ place, branches, onPlace }: { place: Place; branches?: Branches; onPlace: (p: Place) => void }) {
  const { t } = useTranslation("thread");
  const [find, setFind] = useState("");
  const [turns, setTurns] = useState(0);
  const chosen = branches ? (place.worktree ? place.base || branches.default || branches.current : place.on || branches.current) : "";
  const modeLabel = t(place.worktree ? "launch.worktree" : "launch.checkout");
  const modeHint = t(place.worktree ? "launch.worktreeHint" : "launch.checkoutHint");
  const switchHint = t(place.worktree ? "launch.switchToCheckout" : "launch.switchToWorktree");
  return (
    <div className="th-place">
      <div className="th-wt-wrap">
        <button
          type="button"
          className="mb th-wtbtn"
          aria-label={`${t("launch.workingCopy")}: ${modeLabel}`}
          aria-pressed={place.worktree}
          onClick={() => {
            setTurns((n) => n + 1);
            onPlace({ ...place, worktree: !place.worktree });
          }}
        >
          <span key={String(place.worktree)} className="th-wt-slot">
            <Icon name={place.worktree ? "branch" : "folder"} />
            <span className="lbl" data-testid="launch-worktree">{modeLabel}</span>
          </span>
          <span className="th-swap" style={{ transform: `rotate(${turns * 180}deg)` }} aria-hidden="true">
            <Icon name="swap" />
          </span>
        </button>
        <div className="th-wttip" role="tooltip" aria-hidden="true">
          <b>{modeHint}</b>
          <span>· {switchHint}</span>
        </div>
      </div>
      {branches && (
        <MenuButton
          className="mb"
          title={t(place.worktree ? "branch.base" : "branch.workOn")}
          label={
            <>
              <Icon name="branch" />
              <span className="lbl th-br" data-testid="branch-trigger">
                {place.worktree ? <Trans t={t} i18nKey="branch.from" values={{ branch: (place.fromRemote ? "origin/" : "") + chosen }} components={{ b: <b /> }} /> : <b>{chosen}</b>}
              </span>
              <Icon name="chevD" />
            </>
          }
        >
          {(close) => {
            const q = find.trim().toLowerCase();
            const order = (n: string) => (n === branches.current ? 0 : n === branches.default ? 1 : 2);
            const list = [...new Set([branches.current, ...branches.all])]
              .filter((n) => n && !n.startsWith("hermesh/") && (!q || n.toLowerCase().includes(q)))
              .sort((a, b) => order(a) - order(b) || a.localeCompare(b));
            return (
              <div className="th-branches" data-testid="branch-panel">
                <label className="th-mfind">
                  <Icon name="search" className="sm" />
                  <input autoFocus value={find} placeholder={t("branch.find")} spellCheck={false} data-testid="branch-find" onChange={(e) => setFind(e.target.value)} onKeyDown={(e) => e.key === "Escape" && find && (e.stopPropagation(), setFind(""))} />
                </label>
                <div className="mgh">{t(place.worktree ? "branch.base" : "branch.workOn")}</div>
                {place.worktree && (
                  <button className="mi" role="menuitemcheckbox" aria-checked={place.fromRemote} tabIndex={-1} data-testid="branch-remote" onClick={() => onPlace({ ...place, fromRemote: !place.fromRemote })}>
                    <span className="ck" />
                    <span className="two">
                      {t("branch.remote")}
                      <span>{t("branch.remoteHint")}</span>
                    </span>
                    <span className={`sw th-sw ${place.fromRemote ? "on" : ""}`} />
                  </button>
                )}
                {!place.worktree && chosen !== branches.current && <p className="th-brnote">{t("branch.switchNote")}</p>}
                <div className="msep" role="separator" />
                <div className="th-brlist">
                  {list.map((n) => {
                    const tags = [n === branches.current && t("branch.current"), n === branches.default && t("branch.default"), branches.inWorktrees.includes(n) && t("branch.worktree")].filter(Boolean);
                    // In the folder, a branch another worktree has can't be checked out too.
                    const blocked = !place.worktree && branches.inWorktrees.includes(n) && n !== branches.current;
                    return (
                      <Item
                        key={n}
                        checked={n === chosen}
                        icon={<Icon name="branch" />}
                        label={<span className="th-mono">{n}</span>}
                        desc={blocked ? t("branch.inWorktree") : tags.join(" · ") || undefined}
                        disabled={blocked}
                        testId={`branch-item-${n}`}
                        onClick={() => {
                          close();
                          setFind("");
                          onPlace(place.worktree ? { ...place, base: n } : { ...place, on: n });
                        }}
                      />
                    );
                  })}
                  {list.length === 0 && <div className="mempty">{t("branch.none")}</div>}
                </div>
              </div>
            );
          }}
        </MenuButton>
      )}
    </div>
  );
}

/**
 * In the box's bottom row: the model, its provider's logo and name, and how hard it thinks after it in grey, when it
 * has levels. One panel holds both: the level at its top, the models under it.
 */
export function ModelButtons({ providers, sel, stale, machineName, projectAgents, onModel, onNotice }: {
  providers: ProviderInstance[];
  sel: ModelSelection;
  stale?: boolean;
  machineName: string;
  projectAgents: string[];
  onModel: (sel: ModelSelection) => void;
  onNotice: (text: string) => void;
}) {
  const { t } = useTranslation("thread");
  const inst = findProviderInstance(providers, sel.instanceId);
  const reasoning = reasoningOf(providers, sel);
  const entry = inst?.models.find((m) => m.slug === sel.model);
  return (
    <MenuButton
      className="mb th-pickbtn"
      title={t("picker.model")}
      label={
        <>
          {inst && <AgentLogo agent={inst.instanceId} size={14} />}
          <span className="lbl strong" data-testid="model-picker-trigger">{entry ? entry.shortName || entry.name : inst ? selectionTitle(providers, sel) : t("picker.model")}</span>
          {reasoning && (
            <span className="th-lvl" data-testid="thinking-trigger">
              {levelTitle(reasoning.value, reasoning.choices.find((c) => c.id === reasoning.value)?.label ?? reasoning.value)}
            </span>
          )}
          <Icon name="chevD" />
        </>
      }
    >
      {(close) => (
        <ModelPanel
          providers={providers}
          sel={sel}
          stale={stale}
          machineName={machineName}
          projectAgents={projectAgents}
          onLevel={(id) => reasoning && onModel(withReasoning(sel, reasoning.id, id))}
          onSel={(next, changed) => {
            close();
            onModel(next);
            if (changed) onNotice(t("picker.thinkingChanged", { model: selectionTitle(providers, next), from: levelTitle(changed.from, changed.from), to: levelTitle(changed.to, changed.to) }));
          }}
        />
      )}
    </MenuButton>
  );
}

/**
 * How hard the model thinks, at the top of the model panel: its levels side by side, least to most, the chosen one on
 * a raised plate, as on iPhone. The plate is dragged: it follows the pointer, swells a little, and the level under it
 * is chosen as it passes; let go, it settles on that level with a small bounce. A click goes straight to a level; ← and
 * → step through them (from the panel's search while it is empty). Choosing one leaves the panel open.
 */
function Effort({ reasoning, onLevel }: { reasoning: Reasoning; onLevel: (id: string) => void }) {
  const { t } = useTranslation("thread");
  const row = useRef<HTMLDivElement>(null);
  // Where the plate's left edge is while dragged, in px from the first level's.
  const [dragged, setDragged] = useState<number | null>(null);
  const n = reasoning.choices.length;
  const i = Math.max(0, reasoning.choices.findIndex((c) => c.id === reasoning.value));
  const pick = (k: number) => k !== i && onLevel(reasoning.choices[k].id);
  const column = () => (row.current!.getBoundingClientRect().width - 4) / n;
  const along = (x: number) => {
    const r = row.current!.getBoundingClientRect();
    return Math.min(column() * (n - 1), Math.max(0, x - r.left - 2 - column() / 2));
  };
  const follow = (x: number) => {
    const at = along(x);
    setDragged(at);
    pick(Math.round(at / column()));
  };
  return (
    <div className="th-effort" data-testid="picker-effort">
      <div className="th-effort-h">
        <Icon name="gauge" className="sm" />
        {t("picker.thinking")}
        <span className="th-effort-keys" aria-hidden="true">← →</span>
      </div>
      <div
        ref={row}
        className={`th-seg ${dragged !== null ? "drag" : ""}`}
        role="radiogroup"
        aria-label={t("picker.thinking")}
        style={{ "--n": n, "--i": i, ...(dragged !== null && { "--at": `${dragged}px` }) } as CSSProperties}
        onPointerDown={(e) => {
          e.preventDefault();
          e.currentTarget.setPointerCapture(e.pointerId);
          follow(e.clientX);
        }}
        onPointerMove={(e) => dragged !== null && follow(e.clientX)}
        onPointerUp={() => setDragged(null)}
        onPointerCancel={() => setDragged(null)}
        onKeyDown={(e) => {
          const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
          if (!step) return;
          e.preventDefault();
          e.stopPropagation();
          const k = Math.min(n - 1, Math.max(0, i + step));
          pick(k);
          row.current?.querySelectorAll<HTMLElement>("[role=radio]")[k]?.focus();
        }}
      >
        <span className="th-seg-plate" aria-hidden="true" />
        {reasoning.choices.map((c, k) => (
          <button key={c.id} type="button" role="radio" aria-checked={k === i} tabIndex={k === i ? 0 : -1} data-testid={`picker-level-${c.id}`}>
            {levelTitle(c.id, c.label)}
          </button>
        ))}
      </div>
    </div>
  );
}

/**
 * The model: one column (§16). A search over every provider; the providers that can be used one under another, a
 * model a row; those not installed, turned off or not for this project left out. Choosing closes it.
 */
function ModelPanel({ providers, sel, stale, machineName, projectAgents, onSel, onLevel }: {
  providers: ProviderInstance[];
  sel: ModelSelection;
  stale?: boolean;
  machineName: string;
  projectAgents: string[];
  onSel: (sel: ModelSelection, changed?: { from: string; to: string }) => void;
  onLevel: (id: string) => void;
}) {
  const { t } = useTranslation("thread");
  const [query, setQuery] = useState("");
  const find = useRef<HTMLInputElement>(null);
  useEffect(() => find.current?.focus(), []);
  const usable = providers.filter((p) => ["", "signedOut"].includes(providerUnusable(p, projectAgents)));
  const groups = query.trim() ? findModels(usable, query).map((g) => ({ inst: g.inst, models: g.models })) : usable.map((inst) => ({ inst, models: inst.models }));
  const reasoning = reasoningOf(providers, sel);
  const keys = (e: KeyboardEvent) => {
    if (e.key === "Escape" && query) {
      e.stopPropagation();
      setQuery("");
    }
    // With nothing searched, ← and → set how hard it thinks.
    if (!query && reasoning && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
      e.preventDefault();
      const i = Math.max(0, reasoning.choices.findIndex((c) => c.id === reasoning.value)) + (e.key === "ArrowRight" ? 1 : -1);
      if (i >= 0 && i < reasoning.choices.length) onLevel(reasoning.choices[i].id);
    }
  };
  return (
    <div className="th-mpanel" data-testid="model-traits-popover">
      {reasoning && reasoning.choices.length > 1 && <Effort reasoning={reasoning} onLevel={onLevel} />}
      <label className="th-mfind">
        <Icon name="search" className="sm" />
        <input ref={find} value={query} placeholder={t("picker.find")} spellCheck={false} onChange={(e) => setQuery(e.target.value)} onKeyDown={keys} />
      </label>
      {stale && <div className="th-mstale">{t("picker.lastKnown", { machine: machineName })}</div>}
      <div className="th-mlist">
        {usable.length === 0 ? (
          <div className="mempty">{t("picker.none", { machine: machineName })}</div>
        ) : (
          groups.map((g) => (
            <div key={g.inst.instanceId} role="group" aria-label={g.inst.displayName} data-testid={`picker-provider-${g.inst.instanceId}`}>
              <div className="mgh th-mgh">
                <AgentLogo agent={g.inst.instanceId} size={14} />
                {g.inst.displayName}
                {providerUnusable(g.inst, projectAgents) === "signedOut" && <span className="th-why">· {t("picker.signedOut")}</span>}
              </div>
              {g.models.map((m) => (
                <Item
                  key={m.slug}
                  checked={sel.instanceId === g.inst.instanceId && sel.model === m.slug}
                  label={m.name}
                  value={
                    g.inst.instanceId === "her" && m.isDefault ? <span className="th-badge-new th-badge-def">{t("launch.defaultModel")}</span>
                    : m.badge ? <span className="th-badge-new">{t("picker.isNew")}</span> : undefined
                  }
                  testId={`picker-model-${m.slug}`}
                  onClick={() => {
                    const r = selectModel(providers, sel, g.inst.instanceId, m.slug);
                    onSel(r.sel, r.changed);
                  }}
                />
              ))}
            </div>
          ))
        )}
        {usable.length > 0 && groups.length === 0 && <div className="mempty">{t("picker.noMatch")}</div>}
      </div>
    </div>
  );
}
