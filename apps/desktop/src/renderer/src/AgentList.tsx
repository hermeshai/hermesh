import { useState, type ReactNode } from "react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { AgentSpeed } from "@hermesh/proto/identity";
import { AgentProblem, type AgentStatus } from "@hermesh/proto/machine";
import type { Machine } from "@hermesh/proto/local";
import { codingAgentIds, codingAgentTitle } from "@hermesh/core";
import { AgentLogo, AgentTile, Mark } from "@hermesh/ui";
import type { TerminalCommand } from "../../shared/bridge";
import { useRecheckAgents } from "./agents";
import { local } from "./api";
import { Icon } from "./icons";
import { useStore } from "./store";
import "./agentlist.css";

// A machine's agent providers, as the first run's "Check and Manage Available
// Agents" shows them: every coding agent Hermesh supports, what the machine has
// of each (version, logged in), and each installed one opening its page
// (AgentPage): signing in, and the model its sessions run with. Projects are
// the machine's, for every provider (docs/settings-environments.md §5.6).

export const ready = (a: AgentStatus) => a.available && a.loggedIn;

/** Found (or its app is), but it can't run: it says why rather than counting as not installed. */
export const broken = (a: AgentStatus) => !a.available && !a.checking && a.problem !== AgentProblem.UNSPECIFIED && a.problem !== AgentProblem.NOT_FOUND;

/** Why a broken agent can't run, and what to do. */
export function whyBroken(a: AgentStatus, t: TFunction<"firstrun">): string {
  const v = { agent: codingAgentTitle(a.name), binary: a.binary.replace(/^\/(Users|home)\/[^/]+/, "~"), detail: a.detail };
  switch (a.problem) {
    case AgentProblem.APP_WITHOUT_CLI:
      return t("prepare.why.appWithoutCli", { ...v, app: a.detail.replace(/\.app$/, "") });
    case AgentProblem.NO_PROCESSES:
      return t("prepare.why.noProcesses", v);
    case AgentProblem.TIMED_OUT:
      return t("prepare.why.timedOut", v);
    case AgentProblem.MISSING_RUNTIME:
      return t("prepare.why.missingRuntime", { ...v, runtime: a.detail });
    default:
      return t("prepare.why.failed", v);
  }
}

/** A broken agent's state in a few words: its CLI is missing, or it can't run. */
export function BrokenState({ a }: { a: AgentStatus }) {
  const { t } = useTranslation("firstrun");
  const cli = a.problem === AgentProblem.APP_WITHOUT_CLI;
  return (
    <span className={cli ? "warn" : "bad"}>
      <i />
      {t(cli ? "prepare.needsCli" : "prepare.cannotRun")}
    </span>
  );
}

/** Claude Code and Codex first, then the order Hermesh lists them in. */
const order = (name: string) => codingAgentIds.indexOf(name);

/**
 * A machine's agents sorted out: installed (ready first), still being checked
 * (not known yet), not installed; searching while none is known yet, and
 * known once the machine has said which it has.
 */
export function sortAgents(agents: AgentStatus[] | undefined) {
  const installed = (agents ?? []).filter((a) => a.available).sort((a, b) => Number(ready(b)) - Number(ready(a)) || order(a.name) - order(b.name));
  const troubled = (agents ?? []).filter(broken).sort((a, b) => order(a.name) - order(b.name));
  const checking = (agents ?? []).filter((a) => a.checking && !a.available).sort((a, b) => order(a.name) - order(b.name));
  const listed = (id: string) => [...installed, ...troubled, ...checking].some((a) => a.name === id);
  const missing = codingAgentIds.filter((id) => !listed(id));
  return { installed, troubled, checking, missing, searching: !agents || (installed.length === 0 && checking.length > 0), known: !!agents?.length };
}

/**
 * The list: one row per installed agent, opening its page (onOpen), the ones
 * being checked, then the ones not installed; until the machine says which
 * it has, unknown instead.
 */
export function AgentList({ agents, unknown, onOpen }: { agents: ReturnType<typeof sortAgents>; unknown?: ReactNode; onOpen: (agent: string) => void }) {
  const { t } = useTranslation("firstrun");
  const { installed, troubled, checking, missing, known } = agents;
  return (
    <>
      <div className="ag-list" role="list" aria-label={t("prepare.agents")}>
        {!known && unknown}
        {installed.map((a) => {
          const set = defaultsText(a, t);
          return (
            <div key={a.name} className="ag" role="listitem" aria-label={codingAgentTitle(a.name)} title={a.detail || undefined}>
              <button className="ag-head" onClick={() => onOpen(a.name)}>
                <AgentTile agent={a.name} size={32} />
                <span className="ag-text">
                  <b>{codingAgentTitle(a.name)}</b>
                  <span className="ag-sub">
                    {a.version && <span className="mono">{a.version}</span>}
                    <span className={a.loggedIn ? "ok" : "warn"}>
                      <i />
                      {a.loggedIn ? t("prepare.signedIn") : t("prepare.notSignedIn")}
                    </span>
                    {set && <span>{set}</span>}
                  </span>
                </span>
                <Icon name="chevR" className="ag-chev" />
              </button>
            </div>
          );
        })}
        {troubled.map((a) => (
          <div key={a.name} className="ag" role="listitem" aria-label={codingAgentTitle(a.name)}>
            <button className="ag-head" onClick={() => onOpen(a.name)}>
              <AgentTile agent={a.name} size={32} />
              <span className="ag-text">
                <b>{codingAgentTitle(a.name)}</b>
                <span className="ag-sub">
                  <BrokenState a={a} />
                </span>
                <span className="ag-why">{whyBroken(a, t)}</span>
              </span>
              <Icon name="chevR" className="ag-chev" />
            </button>
          </div>
        ))}
        {checking.map((a) => (
          <div key={a.name} className="ag" role="listitem" aria-label={codingAgentTitle(a.name)}>
            <div className="ag-head">
              <AgentTile agent={a.name} size={32} />
              <span className="ag-text">
                <b>{codingAgentTitle(a.name)}</b>
                <span className="ag-sub">
                  <span className="ag-checking">
                    <Mark size={11} variant="seq" />
                    {t("prepare.checking")}
                  </span>
                </span>
              </span>
            </div>
          </div>
        ))}
      </div>
      {known && missing.length > 0 && (
        <>
          <div className="ag-group">{t("prepare.notInstalled")}</div>
          <div className="ag-missing" role="list" aria-label={t("prepare.missing")}>
            {missing.map((name) => (
              <div key={name} role="listitem" aria-label={codingAgentTitle(name)}>
                <AgentLogo agent={name} size={16} />
                {codingAgentTitle(name)}
              </div>
            ))}
          </div>
        </>
      )}
    </>
  );
}

type T = TFunction<"firstrun">;

const modelName = (a: AgentStatus, id: string) => a.models.find((x) => x.id === id)?.name || id;
const effortName = (t: T, e: string) => t(`prepare.efforts.${e}`, { defaultValue: e });
const speedName = (t: T, v: AgentSpeed) => (v === AgentSpeed.FAST ? t("prepare.fast") : t("prepare.standard"));

/** What is set for the agent's sessions here, for its row: "" while it follows its own configuration. */
function defaultsText(a: AgentStatus, t: T): string {
  const d = a.defaults;
  if (!d) return "";
  return [d.model && modelName(a, d.model), d.effort && effortName(t, d.effort), d.speed ? speedName(t, d.speed) : ""].filter(Boolean).join(" · ");
}

export const logins: Record<string, { key: TerminalCommand; text: string }> = {
  codex: { key: "codexLogin", text: "codex login" },
  claude: { key: "claudeLogin", text: "claude" },
  antigravity: { key: "antigravityLogin", text: "hermesh agent login antigravity" },
};

/**
 * An agent's page on a machine, opened from its row: signing in when it
 * isn't, and the model, reasoning effort and (Codex) speed its sessions run
 * with when the agent starting one names no model (or why there is no choosing).
 */
export function AgentPage({ m, agent }: { m: Machine; agent: string }) {
  const { t } = useTranslation("firstrun");
  const a = m.status?.agents.find((x) => x.name === agent);
  const [ran, setRan] = useState(false);
  useRecheckAgents(ran && !a?.loggedIn);
  const title = codingAgentTitle(agent);
  // Logging in runs in this Mac's Terminal: another machine's agent is logged in there.
  const login = m.local ? logins[agent] : undefined;

  return (
    <div className="ag-page" role="region" aria-label={title}>
      <div className="ag-hero">
        <AgentTile agent={agent} size={44} />
        <span className="ag-text">
          <b>{title}</b>
          <span className="ag-sub">
            <span>{m.machineName}</span>
            {a?.version && <span className="mono">{a.version}</span>}
            {a && (broken(a) ? <BrokenState a={a} /> : (
              <span className={a.loggedIn ? "ok" : "warn"}>
                <i />
                {a.loggedIn ? t("prepare.signedIn") : t("prepare.notSignedIn")}
              </span>
            ))}
          </span>
        </span>
      </div>
      {a && broken(a) && <p className="ag-why">{whyBroken(a, t)}</p>}
      {a && a.available && !a.loggedIn && (
        <div className="ag-login">
          {ran ? (
            <>
              <Mark size={12} variant="seq" />
              {t("prepare.waitingLogin")}
            </>
          ) : (
            <>
              <span>{t("prepare.loginFirst", { agent: title })}</span>
              {login && (
                <>
                  <span className="chip mono">{login.text}</span>
                  <button className="btn sm" onClick={() => (window.hermesh?.runInTerminal(login.key), setRan(true))}>
                    <Icon name="terminal" />
                    {t("prepare.runInTerminal")}
                  </button>
                </>
              )}
            </>
          )}
        </div>
      )}
      {a && a.models.length > 0 && <Defaults a={a} m={m} />}
      {a && a.models.length === 0 && a.loggedIn && (a.modelsState === "unsupported" || a.modelsState === "error") && (
        <section aria-label={t("prepare.defaultsHead")}>
          <div className="ag-group">{t("prepare.defaultsHead")}</div>
          <p className="ag-note">{t(`prepare.models.${a.modelsState}`, { agent: title })}</p>
        </section>
      )}
    </div>
  );
}

/**
 * The model, reasoning effort and (Codex) speed an agent's sessions run with
 * when the agent starting one names no model, one row each: the agent's own
 * configuration unless set here; set here, only Hermesh's sessions use them.
 * Each row's button shows the value and opens a menu of the choices.
 */
export function Defaults({ a, m }: { a: AgentStatus; m: Machine }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const title = codingAgentTitle(a.name);
  const d = { model: a.defaults?.model ?? "", effort: a.defaults?.effort ?? "", speed: a.defaults?.speed ?? AgentSpeed.UNSPECIFIED };
  const own = a.own;
  const same = (v: string) => (v ? t("prepare.sameAsValue", { agent: title, value: v }) : t("prepare.sameAs", { agent: title }));
  const effortsOf = (id: string) => a.models.find((x) => x.id === id)?.efforts;
  const model = d.model || own?.model || "";
  // An agent whose models take no reasoning effort has no row for it.
  const efforts = effortsOf(model) ?? a.models.find((x) => x.efforts.length > 0)?.efforts ?? [];

  const save = (next: typeof d) => st.run(() => local.setAgentDefaults({ machineId: m.machineId, defaults: { agent: a.name, ...next } }));
  const pick = async (items: { id: string; label: string; checked: boolean }[], set: (v: string) => void) => {
    const id = await window.hermesh?.popupMenu(items);
    if (id == null) return;
    set(id);
  };
  const rows = [
    {
      key: "model",
      label: t("prepare.model"),
      value: d.model ? modelName(a, d.model) : same(own?.model ? modelName(a, own.model) : ""),
      open: () =>
        pick(
          [{ id: "", label: same(own?.model ? modelName(a, own.model) : ""), checked: !d.model }, ...a.models.map((x) => ({ id: x.id, label: x.name || x.id, checked: d.model === x.id }))],
          (v) => {
            // An effort the newly picked model lacks goes back to the agent's own.
            const ok = effortsOf(v || own?.model || "");
            save({ ...d, model: v, effort: d.effort && ok && !ok.includes(d.effort) ? "" : d.effort });
          },
        ),
    },
    ...(a.models.some((x) => x.efforts.length > 0)
      ? [
          {
            key: "effort",
            label: t("prepare.effort"),
            value: d.effort ? effortName(t, d.effort) : same(own?.effort ? effortName(t, own.effort) : ""),
            open: () =>
              pick(
                [
                  { id: "", label: same(own?.effort ? effortName(t, own.effort) : ""), checked: !d.effort },
                  ...efforts.map((e) => ({ id: e, label: effortName(t, e), checked: d.effort === e })),
                ],
                (v) => save({ ...d, effort: v }),
              ),
          },
        ]
      : []),
    ...(a.name === "codex"
      ? [
          {
            key: "speed",
            label: t("prepare.speed"),
            value: d.speed ? speedName(t, d.speed) : same(own?.speed ? speedName(t, own.speed) : ""),
            open: () =>
              pick(
                [
                  { id: "0", label: same(own?.speed ? speedName(t, own.speed) : ""), checked: !d.speed },
                  { id: "1", label: t("prepare.standard"), checked: d.speed === AgentSpeed.STANDARD },
                  { id: "2", label: t("prepare.fast"), checked: d.speed === AgentSpeed.FAST },
                ],
                (v) => save({ ...d, speed: Number(v) }),
              ),
          },
        ]
      : []),
  ];
  return (
    <section aria-label={t("prepare.defaultsHead")}>
      <div className="ag-group">{t("prepare.defaultsHead")}</div>
      <div className="ag-prjs">
        {rows.map((r) => (
          <div key={r.key} className="ag-set" role="group" aria-label={r.label}>
            <span>{r.label}</span>
            <button className="ag-pop" onClick={r.open} disabled={!m.online && !m.local}>
              {r.value}
              <Icon name="chevD" />
            </button>
          </div>
        ))}
      </div>
      <p className="ag-note">{t("prepare.defaultsNote", { agent: title })}</p>
    </section>
  );
}
