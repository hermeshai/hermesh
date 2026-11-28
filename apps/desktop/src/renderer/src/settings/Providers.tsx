import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { AgentProblem } from "@hermesh/proto/machine";
import { codingAgentTitle } from "@hermesh/core";
import { AgentLogo, AgentTile, Mark } from "@hermesh/ui";
import { BrokenState, Defaults, broken, installUrls, logins, sortAgents, useMissingAgentList, whyBroken } from "../AgentList";
import { useCheckAgents, useRecheckAgents } from "../agents";
import { local } from "../api";
import { ProviderMaintenance, providerActive, providerPhaseKey, useProviderOperationRunner } from "./ProviderMaintenance";
import { Icon } from "../icons";
import { useStore } from "../store";

// An environment's agent providers (docs/settings-environments.md §5.5): each installed one
// opening its page, the ones not installed after them (managed one-click installs first, external guides after).

export function Providers({ m, open }: { m: Machine; open: (agent: string) => void }) {
  const { t } = useTranslation("settings");
  const { t: f } = useTranslation("firstrun");
  const st = useStore();
  const { sending, run } = useProviderOperationRunner();
  const agents = sortAgents(m.status?.agents);
  const { installed, troubled, checking, missing, known } = agents;
  const missingList = useMissingAgentList(agents);
  const rootAccount = !!m.status?.providerMaintenance.length &&
    m.status.providerMaintenance.some((c) => c.account === "root") &&
    m.status.providerMaintenance.every((c) => !c.package);
  useCheckAgents(m.local);
  useEffect(() => {
    if (!m.local && m.online && !known) void local.checkAgents({ machineId: m.machineId }).catch(() => {});
  }, [m.machineId, m.local, m.online, known]);

  const capOf = (name: string) => m.status?.providerMaintenance.find((c) => c.provider === name);
  const latestOf = (name: string) => m.status?.providerOperations.findLast((j) => j.request?.provider === name);
  const openInstall = (agent: string) => st.set({ sheet: { kind: "installProvider", machineId: m.machineId, agent } });

  const row = (name: string, value: React.ReactNode, sub?: React.ReactNode, go = true) => (
    <button key={name} className="frow go" role="listitem" aria-label={codingAgentTitle(name)} disabled={!go} onClick={() => open(name)}>
      <AgentTile agent={name} size={26} />
      <div className="ft">
        <div className="fn">{codingAgentTitle(name)}</div>
        {sub && <div className="fdesc">{sub}</div>}
      </div>
      <span className="fv-side">{value}</span>
      {go && <Icon name="chevR" className="sm chev" />}
    </button>
  );

  // Managed one-click installable providers appear ahead of external-only providers.
  const orderedMissing = [...missingList.names].sort((a, b) => Number(!!capOf(b)?.package) - Number(!!capOf(a)?.package));
  const hasHistory = (m.status?.providerOperations.length ?? 0) > 0;

  return (
    <div className="form">
      <div>
        <div className="fgroup" role="list" aria-label={t("env.providers")}>
          {!known && (
            <div className="frow muted">
              {(m.online || m.local) && <Mark size={14} variant="seq" />}
              {m.online || m.local ? f("prepare.checking") : t("providers.offline")}
            </div>
          )}
          {installed.map((a) => {
            const cap = capOf(a.name);
            const latest = latestOf(a.name);
            const active = providerActive(latest?.phase);
            const upd = !!(cap?.managedVersion && cap.targetVersion && cap.managedVersion !== cap.targetVersion);
            const sub = active
              ? <>{a.version && <span className="mono">{a.version} · </span>}{m.online ? t(`maintenance.${providerPhaseKey(latest?.phase)}`) : t("maintenance.offline")}</>
              : upd
                ? <>{a.version && <span className="mono">{a.version} · </span>}{t("maintenance.canUpdate", { version: cap!.targetVersion })}</>
                : a.version && <span className="mono">{a.version}</span>;
            const status = active && m.online
              ? <span className="set-checking"><Mark size={11} variant="seq" />{t(`maintenance.${providerPhaseKey(latest?.phase)}`)}</span>
              : <span className={a.loggedIn ? "" : "warn"}>{f(a.loggedIn ? "prepare.signedIn" : "prepare.notSignedIn")}</span>;
            return row(a.name, status, sub);
          })}
          {troubled.map((a) => {
            const latest = latestOf(a.name);
            const active = providerActive(latest?.phase);
            return row(
              a.name,
              active && m.online ? <span className="set-checking"><Mark size={11} variant="seq" />{t(`maintenance.${providerPhaseKey(latest?.phase)}`)}</span> : <BrokenState a={a} />,
              active ? (m.online ? t(`maintenance.${providerPhaseKey(latest?.phase)}`) : t("maintenance.offline")) : whyBroken(a, f),
            );
          })}
          {checking.map((a) => row(a.name, <span className="set-checking"><Mark size={11} variant="seq" />{f("prepare.checking")}</span>, undefined, false))}
        </div>
        <p className="set-foot">{t("providers.note")}</p>
      </div>
      {known && missing.length > 0 && (
        <div>
          <div className="fhead">{t("providers.notInstalled")}</div>
          <div className="fgroup" role="list" aria-label={t("providers.notInstalled")} hidden={!orderedMissing.length}>
            {orderedMissing.map((name) => {
              const cap = capOf(name);
              const canManage = !!cap?.package;
              const job = latestOf(name);
              const active = providerActive(job?.phase);
              const failed = job?.phase === "failed" || job?.phase === "interrupted";
              const url = installUrls[name];
              return (
                <div key={name} className={`frow ${canManage || active || failed ? "" : "muted"}`} role="listitem" aria-label={codingAgentTitle(name)}>
                  {canManage || active ? <AgentTile agent={name} size={26} /> : <span className="set-gray"><AgentLogo agent={name} size={16} /></span>}
                  <div className="ft">
                    <div className="fn">{codingAgentTitle(name)}</div>
                    <div className="fdesc">
                      {active
                        ? (m.online ? [t(`maintenance.${providerPhaseKey(job?.phase)}`), job?.detail].filter(Boolean).join(" · ") : t("maintenance.offline"))
                        : failed
                          ? <span className="warn">{job?.detail || t(`maintenance.${providerPhaseKey(job?.phase)}`)}</span>
                          : canManage
                            ? t("maintenance.installableRow", { pkg: cap.package, version: cap.targetVersion })
                            : t("maintenance.externalRow", { machine: m.machineName })}
                    </div>
                  </div>
                  {active ? (
                    <>
                      <span className="fv-side set-checking">
                        {m.online && <Mark size={11} variant="seq" />}
                        {t(`maintenance.${providerPhaseKey(job?.phase)}`)}
                      </span>
                      {m.online && (job?.phase === "preflight" || job?.phase === "waiting") && (
                        <button type="button" className="btn" disabled={sending} onClick={() => void run(m, name, "cancel", cap?.managedVersion ?? "", cap?.targetVersion ?? "", job)}>
                          {t("maintenance.cancel")}
                        </button>
                      )}
                    </>
                  ) : job?.phase === "interrupted" ? (
                    <button type="button" className="btn" disabled={!m.online || sending} onClick={() => void run(m, name, "verify", "", "", job)}>
                      {t("maintenance.reverify")}
                    </button>
                  ) : canManage ? (
                    <button type="button" className="btn" disabled={!m.online} onClick={() => openInstall(name)}>
                      {t(failed ? "maintenance.retry" : "maintenance.installPrompt")}
                    </button>
                  ) : url ? (
                    <button type="button" className="link fv-side" onClick={() => window.hermesh?.openExternal(url)}>
                      {t("maintenance.howToInstall")} <Icon name="ext" className="xs" />
                    </button>
                  ) : null}
                </div>
              );
            })}
          </div>
          {missingList.toggle}
          {rootAccount && <p className="set-foot">{t("maintenance.rootAccount")}</p>}
        </div>
      )}
      {(hasHistory || m.local || m.online) && (
        <div className="set-acts">
          {hasHistory && <button type="button" className="btn" onClick={() => open("history")}>{t("maintenance.history")}</button>}
          {(m.local || m.online) && (
            <button type="button" className="btn push" onClick={() => void local.checkAgents({ machineId: m.machineId }).catch(() => {})}>
              {t("providers.recheck")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

/** A provider's page: who it is and whether it is signed in, signing in, its sessions' default model, and its CLI maintenance/test. */
export function Provider({ m, agent }: { m: Machine; agent: string }) {
  const { t } = useTranslation("settings");
  const { t: f } = useTranslation("firstrun");
  const st = useStore();
  const a = m.status?.agents.find((x) => x.name === agent);
  const cap = m.status?.providerMaintenance.find((c) => c.provider === agent);
  const latest = m.status?.providerOperations.findLast((j) => j.request?.provider === agent);
  const active = providerActive(latest?.phase);
  const [ran, setRan] = useState(false);
  useRecheckAgents(ran && !a?.loggedIn);
  const title = codingAgentTitle(agent);
  // Logging in runs in this Mac's Terminal: another machine's agent is logged in there.
  const login = m.local ? logins[agent] : undefined;
  const canInstallCli = !!(cap?.package && !cap.managedVersion && a?.problem === AgentProblem.APP_WITHOUT_CLI);
  const guideUrl = installUrls[agent];

  return (
    <div className="form set-env" role="region" aria-label={title}>
      <div className="set-hero">
        <AgentTile agent={agent} size={64} />
        <b>{title}</b>
        <span className="sub">
          {m.machineName}
          {a?.version && (
            <>
              {" · "}
              <span className="mono">{a.version}</span>
            </>
          )}
        </span>
        {a && (a.available || broken(a)) && (broken(a) ? <span className="set-pill"><BrokenState a={a} /></span> : (
          <span className={`set-pill ${a.loggedIn ? "on" : "warn"}`}>
            <i />
            {f(a.loggedIn ? "prepare.signedIn" : "prepare.notSignedIn")}
          </span>
        ))}
      </div>
      {a && broken(a) && (
        <div className="set-banner" role="status">
          <div className="ft">
            <div>{active ? (m.online ? t(`maintenance.${providerPhaseKey(latest?.phase)}`) : t("maintenance.offline")) : whyBroken(a, f)}</div>
          </div>
          {active && m.online ? (
            <span className="fv-side set-checking"><Mark size={11} variant="seq" />{t(`maintenance.${providerPhaseKey(latest?.phase)}`)}</span>
          ) : (
            <>
              {canInstallCli && (
                <button type="button" className="btn primary" disabled={!m.online} onClick={() => st.set({ sheet: { kind: "installProvider", machineId: m.machineId, agent } })}>
                  {t("maintenance.installCliPrompt")}
                </button>
              )}
              {!canInstallCli && guideUrl && (
                <button type="button" className="btn" onClick={() => window.hermesh?.openExternal(guideUrl)}>
                  {t("maintenance.howToInstall")}
                </button>
              )}
              <button type="button" className="btn" disabled={!m.online && !m.local} onClick={() => void local.checkAgents({ machineId: m.machineId }).catch(() => {})}>
                {t("providers.recheck")}
              </button>
            </>
          )}
        </div>
      )}
      {a && a.available && !a.loggedIn && (
        <div className="set-banner" role="status">
          {ran ? (
            <>
              <Mark size={12} variant="seq" />
              <div className="ft">{f("prepare.waitingLogin")}</div>
            </>
          ) : (
            <>
              <div className="ft">
                <div>{f("prepare.loginFirst", { agent: title })}</div>
                {login && <div className="fdesc mono">{login.text}</div>}
              </div>
              {login && (
                <button className="btn" onClick={() => (window.hermesh?.runInTerminal(login.key), setRan(true))}>
                  <Icon name="terminal" />
                  {f("prepare.runInTerminal")}
                </button>
              )}
            </>
          )}
        </div>
      )}
      {a && a.models.length > 0 && <Defaults a={a} m={m} />}
      {a && a.models.length === 0 && a.loggedIn && (a.modelsState === "unsupported" || a.modelsState === "error") && (
        <section aria-label={f("prepare.defaultsHead")}>
          <div className="fhead">{f("prepare.defaultsHead")}</div>
          <p className="set-foot">{f(`prepare.models.${a.modelsState}`, { agent: title })}</p>
        </section>
      )}
      <ProviderMaintenance machine={m} agent={agent} />
    </div>
  );
}
