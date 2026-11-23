import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import { RuntimeMode } from "@hermesh/proto/identity";
import type { Machine, Project } from "@hermesh/proto/local";
import { Workspace } from "@hermesh/proto/identity";
import type { AttachmentRef, ModelSelection, ProviderInstance } from "@hermesh/proto/thread";
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
} from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import { MenuButton } from "../her/menus";
import { Tile } from "../her/Composer";
import "../her/her.css";

// The new-session composer (docs/thread-composer.md §4): Her's box as it is
// (＋, the text, Send), and under it the line of what the session runs on
// and with: environment, project, a separate worktree, the model, and the
// permission mode.

/** The box: what is attached, the text, and Send (with Stop beside it while a turn runs). */
export function ThreadComposer({ value, onChange, onSend, onStop, running, sending, placeholder, attachments, onAttach, onRemove, offline, blocked = false }: {
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  onStop?: () => void;
  running: boolean;
  sending: boolean;
  placeholder: string;
  attachments: AttachmentRef[];
  onAttach: (files: FileList) => void;
  onRemove: (sha: string) => void;
  /** The machine is offline: Send queues it. */
  offline: boolean;
  /** Nothing can be sent there (its Hermesh needs an update). */
  blocked?: boolean;
}) {
  const { t } = useTranslation("thread");
  const area = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);
  const ready = !sending && !blocked && (value.trim() !== "" || attachments.length > 0);
  // The text grows with what is written, up to ten lines, then scrolls (as Her's).
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [value]);
  useEffect(() => area.current?.focus(), []);
  const sendTitle = running ? t("composer.queue") : offline ? t("composer.queueOffline") : t("composer.send");
  return (
    <div
      className="cmp th-cmp"
      onMouseDown={(e) => e.target instanceof HTMLElement && e.target.matches(".cmp,.cmp-row,.cmp-att") && (e.preventDefault(), area.current?.focus())}
    >
      {attachments.length > 0 && (
        <div className="cmp-att">
          {attachments.map((a) => (
            <Tile key={a.blobSha256} name={a.name} onRemove={() => onRemove(a.blobSha256)} />
          ))}
        </div>
      )}
      <div className="cmp-row">
        <input ref={file} type="file" multiple hidden onChange={(e) => (e.target.files && onAttach(e.target.files), (e.target.value = ""))} />
        <button className="cmp-plus" title={t("composer.attach")} aria-label={t("composer.attach")} onClick={() => file.current?.click()}>
          <Icon name="plus" />
        </button>
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

/**
 * A new session's question, in the middle of its page: "What should we build in <project>?", and under it the
 * environment it runs on. Both are menus (opening below), set as last time (choices.ts).
 */
export function LaunchAsk({ machines, projects, projectsStale, choice, onMachine, onProject, onAddProject }: Pickers) {
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
      <h1>
        <Trans t={t} i18nKey="launch.ask" components={{ project }} />
      </h1>
      <MenuButton
        className="th-on"
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
                desc={m.status && !m.status.threads ? t("launch.needsUpdateShort") : m.online || m.local ? undefined : t("launch.offlineQueues")}
                testId={`machine-item-${m.machineName}`}
                onClick={() => (close(), onMachine(m.machineId))}
              />
            ))}
          </>
        )}
      </MenuButton>
    </div>
  );
}

/**
 * The line under the box, the same in a new session and in one: on the left where it works (a new session's
 * working copy, when its project can have its own worktree; a session's machine and project, fixed), on the right
 * the model and the mode, which in a session apply from the next turn.
 */
export function ComposerOptions({ fixed, machines, projects, providersStale, choice, providers, maxCeiling, onWorktree, onModel, onMode, onPlan, onNotice }: {
  /** In a session: where it runs, not to be changed. */
  fixed: boolean;
  machines: Machine[];
  projects: Project[];
  providersStale?: boolean;
  choice: Choice;
  providers: ProviderInstance[];
  maxCeiling: RuntimeMode;
  onWorktree: (on: boolean) => void;
  onModel: (sel: ModelSelection) => void;
  onMode: (m: RuntimeMode) => void;
  onPlan: (on: boolean) => void;
  onNotice: (text: string) => void;
}) {
  const { t } = useTranslation("thread");
  const machine = machines.find((m) => m.machineId === choice.machineId);
  const machineName = machine?.machineName ?? choice.machineId;
  const project = projects.find((p) => p.machineId === choice.machineId && p.projectId === choice.projectId);
  const projectName = choice.projectId === SCRATCH_PROJECT ? t("launch.scratch") : choice.projectId;
  const modeKey = (m: RuntimeMode) => (m === RuntimeMode.SUPERVISED ? "supervised" : m === RuntimeMode.AUTO_EDITS ? "autoEdits" : m === RuntimeMode.FULL_ACCESS ? "fullAccess" : "auto");
  const inst = findProviderInstance(providers, choice.sel.instanceId);
  const reasoning = reasoningOf(providers, choice.sel);
  return (
    <div className="th-opts" data-testid="composer-options">
      {fixed ? (
        <span className="th-where">
          <Icon name={machineIcon(machine)} />
          {machineName}
          <span>·</span>
          {projectName}
        </span>
      ) : (
        (project?.repo || project?.workspace === Workspace.WORKTREE) && (
          <MenuButton
            className="mb"
            title={t("launch.workingCopy")}
            label={
              <>
                <Icon name={choice.worktree ? "branch" : "folder"} />
                <span className="lbl" data-testid="launch-worktree">{t(choice.worktree ? "launch.worktree" : "launch.checkout")}</span>
                <Icon name="chevD" />
              </>
            }
          >
            {(close) => (
              <>
                <div className="mgh">{t("launch.workingCopy")}</div>
                <Item checked={!choice.worktree} label={t("launch.checkout")} desc={t("launch.checkoutHint")} testId="worktree-off" onClick={() => (close(), onWorktree(false))} />
                <Item checked={choice.worktree} label={t("launch.worktree")} desc={t("launch.worktreeHint")} testId="worktree-on" onClick={() => (close(), onWorktree(true))} />
              </>
            )}
          </MenuButton>
        )
      )}
      <span className="th-grow" />
      <MenuButton
        className="mb"
        align="right"
        title={t("picker.model")}
        label={
          <>
            {inst && <AgentLogo agent={inst.instanceId} size={14} />}
            <span className="lbl strong" data-testid="model-picker-trigger">
              {inst ? selectionTitle(providers, choice.sel) : t("picker.model")}
              {reasoning && <span className="th-lvl">{levelTitle(reasoning.value, reasoning.choices.find((c) => c.id === reasoning.value)?.label ?? reasoning.value)}</span>}
            </span>
            <Icon name="chevD" />
          </>
        }
      >
        {() => (
          <ModelPanel
            providers={providers}
            sel={choice.sel}
            stale={providersStale}
            machineName={machineName}
            projectAgents={project?.agents ?? []}
            onSel={(next, changed) => {
              onModel(next);
              if (changed) onNotice(t("picker.thinkingChanged", { model: selectionTitle(providers, next), from: levelTitle(changed.from, changed.from), to: levelTitle(changed.to, changed.to) }));
            }}
          />
        )}
      </MenuButton>
      <MenuButton
        className="mb"
        align="right"
        title={t("mode.title")}
        label={
          <>
            <span className="lbl strong" data-testid="runtime-mode-chip">
              {t(`mode.${modeKey(choice.mode)}`)}
              {choice.plan && <span className="th-lvl">{t("mode.plan")}</span>}
            </span>
            <Icon name="chevD" />
          </>
        }
      >
        {(close) => (
          <div className="th-modes">
            <div className="mgh">{t("mode.title")}</div>
            {ORDERED_RUNTIME_MODES.map((m) => {
              const allowed = isRuntimeModeAllowed(m, maxCeiling);
              return (
                <Item
                  key={m}
                  checked={choice.mode === m}
                  label={t(`mode.${modeKey(m)}`)}
                  desc={allowed ? t(`mode.${modeKey(m)}Hint`) : t("mode.ceilingMachine")}
                  disabled={!allowed}
                  testId={`runtime-mode-${m}`}
                  onClick={() => (close(), onMode(m))}
                />
              );
            })}
            <div className="msep" role="separator" />
            <Item checked={choice.plan} label={t("mode.plan")} desc={t("mode.planHint")} testId="mode-plan-btn" onClick={() => (close(), onPlan(!choice.plan))} />
          </div>
        )}
      </MenuButton>
    </div>
  );
}

/**
 * The model: one panel, nothing nested (docs/thread-composer.md §4.4). A
 * search over every provider; the providers, and the chosen one's models
 * beside them; how hard it thinks, along the bottom. Choosing leaves it open,
 * to set the level next.
 */
function ModelPanel({ providers, sel, stale, machineName, projectAgents, onSel }: {
  providers: ProviderInstance[];
  sel: ModelSelection;
  stale?: boolean;
  machineName: string;
  projectAgents: string[];
  onSel: (sel: ModelSelection, changed?: { from: string; to: string }) => void;
}) {
  const { t } = useTranslation("thread");
  const [query, setQuery] = useState("");
  const [shown, setShown] = useState(sel.instanceId || providers[0]?.instanceId || "");
  const find = useRef<HTMLInputElement>(null);
  useEffect(() => find.current?.focus(), []);
  const inst = findProviderInstance(providers, shown);
  const reasoning = reasoningOf(providers, sel);
  const current = findProviderInstance(providers, sel.instanceId);
  const pick = (instanceId: string, slug: string) => {
    const r = selectModel(providers, sel, instanceId, slug);
    onSel(r.sel, r.changed);
  };
  const row = (p: ProviderInstance, slug: string, name: string, badge: string) => (
    <Item
      key={`${p.instanceId}/${slug}`}
      checked={sel.instanceId === p.instanceId && sel.model === slug}
      label={name}
      value={badge ? <span className="th-badge-new">{t("picker.isNew")}</span> : undefined}
      disabled={providerUnusable(p, projectAgents) !== "" && providerUnusable(p, projectAgents) !== "signedOut"}
      testId={`picker-model-${slug}`}
      onClick={() => pick(p.instanceId, slug)}
    />
  );
  const found = findModels(providers, query);
  const keys = (e: KeyboardEvent) => {
    if (e.key === "Escape" && query) {
      e.stopPropagation();
      setQuery("");
    }
  };
  return (
    <div className="th-mpanel" data-testid="model-traits-popover">
      <label className="th-mfind">
        <Icon name="search" className="sm" />
        <input ref={find} value={query} placeholder={t("picker.find")} spellCheck={false} onChange={(e) => setQuery(e.target.value)} onKeyDown={keys} />
      </label>
      {stale && <div className="th-mstale">{t("picker.lastKnown", { machine: machineName })}</div>}
      {providers.length === 0 ? (
        <div className="mempty">{t("picker.none", { machine: machineName })}</div>
      ) : query.trim() ? (
        <div className="th-mcols">
          <div className="th-mmod wide">
            {found.map((g) => (
              <div key={g.inst.instanceId} role="group" aria-label={g.inst.displayName}>
                <div className="mgh">{g.inst.displayName}</div>
                {g.models.map((m) => row(g.inst, m.slug, m.name, m.badge))}
              </div>
            ))}
            {found.length === 0 && <div className="mempty">{t("picker.noMatch")}</div>}
          </div>
        </div>
      ) : (
        <div className="th-mcols">
          <div className="th-mprov">
            {providers.map((p) => {
              const why = providerUnusable(p, projectAgents);
              return (
                <button
                  key={p.instanceId}
                  className={`mi ${p.instanceId === shown ? "on" : ""}`}
                  role="menuitem"
                  tabIndex={-1}
                  data-testid={`picker-provider-${p.instanceId}`}
                  onMouseMove={(e) => e.currentTarget.focus()}
                  onClick={() => setShown(p.instanceId)}
                >
                  <AgentLogo agent={p.instanceId} size={16} />
                  <span className={why && why !== "signedOut" ? "th-dim" : ""}>{p.displayName}</span>
                  {why && <span className="val">{t(`picker.${why}`)}</span>}
                </button>
              );
            })}
          </div>
          <div className="th-mmod">{inst?.models.map((m) => row(inst, m.slug, m.name, m.badge))}</div>
        </div>
      )}
      <div className="th-mfoot">
        <span>{t("picker.thinking")}</span>
        {reasoning ? (
          <div className="seg th-lvls" role="radiogroup" aria-label={t("picker.thinking")}>
            {reasoning.choices.map((c) => (
              <button
                key={c.id}
                type="button"
                role="radio"
                aria-checked={c.id === reasoning.value}
                className={c.id === reasoning.value ? "on" : ""}
                data-testid={`picker-level-${c.id}`}
                onClick={() => onSel(withReasoning(sel, reasoning.id, c.id))}
              >
                {levelTitle(c.id, c.label)}
              </button>
            ))}
          </div>
        ) : (
          <span className="th-none">{t("picker.noThinking", { model: current ? selectionTitle(providers, sel) : "" })}</span>
        )}
      </div>
    </div>
  );
}
