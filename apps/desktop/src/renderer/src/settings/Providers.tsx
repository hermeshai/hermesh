import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { codingAgentTitle } from "@hermesh/core";
import { AgentLogo, AgentTile, Mark } from "@hermesh/ui";
import { BrokenState, Defaults, broken, logins, sortAgents, useMissingAgentList, whyBroken } from "../AgentList";
import { useCheckAgents, useRecheckAgents } from "../agents";
import { local } from "../api";
import { ProviderMaintenance } from "./ProviderMaintenance";
import { Icon } from "../icons";

// An environment's agent providers (docs/settings-environments.md §5.5): each installed one
// opening its page, the ones not installed after them.

export function Providers({ m, open }: { m: Machine; open: (agent: string) => void }) {
  const { t } = useTranslation("settings");
  const { t: f } = useTranslation("firstrun");
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
          {installed.map((a) => row(a.name, <span className={a.loggedIn ? "" : "warn"}>{f(a.loggedIn ? "prepare.signedIn" : "prepare.notSignedIn")}</span>, a.version && <span className="mono">{a.version}</span>))}
          {troubled.map((a) => row(a.name, <BrokenState a={a} />, whyBroken(a, f)))}
          {checking.map((a) => row(a.name, <span className="set-checking"><Mark size={11} variant="seq" />{f("prepare.checking")}</span>, undefined, false))}
        </div>
        <p className="set-foot">{t("providers.note")}</p>
      </div>
      {known && missing.length > 0 && (
        <div>
          <div className="fhead">{t("providers.notInstalled")}</div>
          <div className="fgroup" role="list" aria-label={t("providers.notInstalled")} hidden={!missingList.names.length}>
            {missingList.names.map((name) => (
              <div key={name} className="frow muted" role="listitem" aria-label={codingAgentTitle(name)}>
                <span className="set-gray"><AgentLogo agent={name} size={16} /></span>
                <div className="ft">
                  <div className="fn">{codingAgentTitle(name)}</div>
                  <div className="fdesc">{t("providers.notInstalledRow")}</div>
                </div>
                {m.status?.providerMaintenance.some((c) => c.provider === name && c.package) &&
                  <button type="button" className="btn" onClick={() => open(name)}>{t("maintenance.installDetail")}</button>}
              </div>
            ))}
          </div>
          {missingList.toggle}
          {rootAccount && <p className="set-foot">{t("maintenance.rootAccount")}</p>}
        </div>
      )}
      {!!m.status?.providerMaintenance.length && <div className="set-acts"><button className="btn" onClick={() => open("history")}>{t("maintenance.history")}</button></div>}
      {(m.local || m.online) && (
        <div className="set-acts">
          <button className="btn push" onClick={() => void local.checkAgents({ machineId: m.machineId }).catch(() => {})}>{t("providers.recheck")}</button>
        </div>
      )}
    </div>
  );
}

/** A provider's page: who it is and whether it is signed in, signing in, and its sessions' default model. */
export function Provider({ m, agent }: { m: Machine; agent: string }) {
  const { t: f } = useTranslation("firstrun");
  const a = m.status?.agents.find((x) => x.name === agent);
  const [ran, setRan] = useState(false);
  useRecheckAgents(ran && !a?.loggedIn);
  const title = codingAgentTitle(agent);
  // Logging in runs in this Mac's Terminal: another machine's agent is logged in there.
  const login = m.local ? logins[agent] : undefined;
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
      <ProviderMaintenance machine={m} agent={agent} />
      {a && broken(a) && <p className="set-foot">{whyBroken(a, f)}</p>}
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
    </div>
  );
}
