import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import type { ProviderOperation } from "@hermesh/proto/machine";
import { codingAgentTitle, platformTitle } from "@hermesh/core";
import { AgentTile, Mark } from "@hermesh/ui";
import { local } from "../api";
import { useStore } from "../store";
import { SRow, Switch } from "./common";
import { runningOn } from "./Environment";

const actions = ["install", "update", "verify", "enable_auto", "disable_auto", "cancel"] as const;
export type MaintenanceAction = (typeof actions)[number];
const phases = ["preflight", "waiting", "installing", "verifying", "succeeded", "failed", "interrupted", "cancelled"] as const;

export const providerActive = (phase?: string) => ["preflight", "waiting", "installing", "verifying"].includes(phase ?? "");
export const providerPhaseKey = (phase?: string) => phases.find((p) => p === phase) ?? "unknown";

const shortPath = (binary: string) => binary.replace(/^\/(Users|home)\/[^/]+/, "~");

export function useProviderOperationRunner() {
  const store = useStore();
  const [sending, setSending] = useState(false);
  const receipt = useRef({ key: "", id: "" });
  const run = async (machine: Machine, agent: string, action: MaintenanceAction, expectedVersion: string, targetVersion: string, latest?: ProviderOperation) => {
    const key = [machine.machineId, agent, action, expectedVersion, targetVersion].join("/");
    if (receipt.current.key !== key || receipt.current.id === latest?.request?.requestId)
      receipt.current = { key, id: crypto.randomUUID() };
    setSending(true);
    try {
      return await store.run(() => local.maintainProvider({
        machineId: machine.machineId,
        operation: {
          provider: agent,
          action,
          requestId: action === "cancel" ? latest!.request!.requestId : receipt.current.id,
          expectedVersion,
          targetVersion,
        },
      }));
    } finally {
      setSending(false);
    }
  };
  return { sending, run };
}

export function InstallProviderSheet({ machineId, agent }: { machineId: string; agent: string }) {
  const { t } = useTranslation("settings");
  const { t: s } = useTranslation("sheets");
  const st = useStore();
  const { sending, run } = useProviderOperationRunner();
  const m = st.machines.find((x) => x.machineId === machineId);
  const cap = m?.status?.providerMaintenance.find((c) => c.provider === agent);
  const latest = m?.status?.providerOperations.findLast((j) => j.request?.provider === agent);
  if (!m || !cap?.package) return null;
  const title = codingAgentTitle(agent);
  const envLabel = [m.machineName, platformTitle(m.status?.platform ?? "")].filter(Boolean).join(" · ");
  const confirm = async () => {
    const ok = await run(m, agent, "install", cap.managedVersion, cap.targetVersion, latest);
    if (ok) st.set({ sheet: null });
  };
  return (
    <>
      <div style={{ display: "flex", gap: 12, alignItems: "center" }}>
        <AgentTile agent={agent} size={42} />
        <div>
          <h3 id="sheet-title">{t("maintenance.installTitle", { machine: m.machineName, agent: title })}</h3>
          <p className="sub">{t("maintenance.installSub")}</p>
        </div>
      </div>
      <div className="wk-kv">
        <div><span>{t("maintenance.installEnv")}</span><b>{envLabel}</b></div>
        {cap.account && <div><span>{t("maintenance.installAccount")}</span><b className="mono">{cap.account}</b></div>}
        <div><span>{t("maintenance.installPackage")}</span><b className="mono">npm · {cap.package}@{cap.targetVersion}</b></div>
        <div><span>{t("maintenance.installPrefix")}</span><b>{t("maintenance.installPrefixValue")}</b></div>
      </div>
      <p className="sub sheet-note">{t("maintenance.installNote", { agent: title })}</p>
      <div className="sheet-foot">
        <button type="button" className="btn" disabled={sending} onClick={() => st.set({ sheet: null })}>{s("cancel")}</button>
        <button type="button" className="btn primary" disabled={sending || !m.online} onClick={() => void confirm()}>{t("maintenance.install")}</button>
      </div>
    </>
  );
}

export function ProviderMaintenance({ machine, agent }: { machine: Machine; agent: string }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const { sending, run } = useProviderOperationRunner();
  const [testing, setTesting] = useState(false);
  const [modelResult, setModelResult] = useState<{ ok: boolean; text: string } | null>(null);
  const info = machine.status?.agents.find((a) => a.name === agent);
  if (!info?.available) return null;
  const cap = machine.status?.providerMaintenance.find((c) => c.provider === agent);
  const latest = machine.status?.providerOperations.findLast((j) => j.request?.provider === agent);
  const activeOp = providerActive(latest?.phase);
  const disabled = !machine.online || sending || activeOp;
  const managed = !!cap?.managedVersion;
  const hasUpdate = !!(cap?.managedVersion && cap.targetVersion && cap.managedVersion !== cap.targetVersion);
  const busySessions = runningOn(st, machine.machineId) > 0;
  const phaseLabel = latest ? t(`maintenance.${providerPhaseKey(latest.phase)}`) : "";

  const trigger = (action: MaintenanceAction) =>
    void run(machine, agent, action, cap?.managedVersion ?? "", cap?.targetVersion ?? "", latest);

  const testModel = async () => {
    setTesting(true);
    setModelResult(null);
    try {
      await st.run(async () => {
        const result = await local.testMachineAgent({ machineId: machine.machineId, agent });
        const ready = result.ok && /^ready[.!]?$/i.test(result.reply.trim());
        setModelResult({
          ok: ready,
          text: t(ready ? "maintenance.modelReply" : "maintenance.modelFailed", { reply: result.reply, ms: result.millis }),
        });
      });
    } finally {
      setTesting(false);
    }
  };

  let versionDesc: React.ReactNode = "";
  let versionControl: React.ReactNode = null;
  if (managed && cap) {
    if (activeOp) {
      versionDesc = machine.online ? [phaseLabel, latest?.detail].filter(Boolean).join(" · ") : t("maintenance.offline");
      versionControl = (
        <>
          <span className="fv-side set-checking"><Mark size={11} variant="seq" />{phaseLabel}</span>
          {(latest?.phase === "preflight" || latest?.phase === "waiting") && (
            <button type="button" className="btn" disabled={!machine.online || sending} onClick={() => trigger("cancel")}>
              {t("maintenance.cancel")}
            </button>
          )}
        </>
      );
    } else if (latest?.phase === "interrupted") {
      versionDesc = <span className="warn">{[phaseLabel, latest.detail].filter(Boolean).join(" · ")}</span>;
      versionControl = <button type="button" className="btn" disabled={disabled} onClick={() => trigger("verify")}>{t("maintenance.reverify")}</button>;
    } else if (latest?.phase === "failed" && latest.request?.action === "update") {
      versionDesc = <span className="warn">{latest.detail || phaseLabel}</span>;
      versionControl = <button type="button" className="btn" disabled={disabled} onClick={() => trigger("update")}>{t("maintenance.retry")}</button>;
    } else {
      versionDesc = [
        t("maintenance.managedSource", { pkg: cap.package, account: cap.account }),
        hasUpdate ? t("maintenance.canUpdate", { version: cap.targetVersion }) : "",
      ].filter(Boolean).join(" · ");
      versionControl = hasUpdate
        ? <button type="button" className="btn" disabled={disabled} onClick={() => trigger("update")}>{t(busySessions ? "maintenance.updateIdle" : "maintenance.update")}</button>
        : <span className="fv-side">{t("maintenance.upToDate")}</span>;
    }
  }

  return (
    <section aria-label={t(managed ? "maintenance.headingManaged" : "maintenance.headingExternal")}>
      <div className="fhead">{t(managed ? "maintenance.headingManaged" : "maintenance.headingExternal")}</div>
      <div className="fgroup">
        {managed && cap ? (
          <>
            <SRow title={<>{t("maintenance.versionTitle")} <span className="mono" style={{ color: "var(--l2)" }}>{info.version}</span></>} desc={versionDesc}>
              {versionControl}
            </SRow>
            <SRow title={t("maintenance.autoTitle")} desc={t("maintenance.autoDesc")}>
              <Switch on={cap.autoUpdate} label={t("maintenance.autoTitle")} disabled={disabled} set={() => trigger(cap.autoUpdate ? "disable_auto" : "enable_auto")} />
            </SRow>
          </>
        ) : (
          <SRow title={t("maintenance.sourceTitle")} desc={[info.binary && shortPath(info.binary), t("maintenance.external")].filter(Boolean).join(" · ")}>
            <span className="fv-side">{t("maintenance.externalBadge")}</span>
          </SRow>
        )}
        <SRow
          title={t("maintenance.testTitle")}
          desc={
            <span role="status" className={modelResult ? (modelResult.ok ? "" : "warn") : ""} style={modelResult?.ok ? { color: "var(--green)" } : undefined}>
              {modelResult ? modelResult.text : t(info.loggedIn ? "maintenance.testDesc" : "maintenance.testNeedLogin")}
            </span>
          }
        >
          <button type="button" className="btn" disabled={!machine.online || !info.loggedIn || testing || activeOp} onClick={() => void testModel()}>
            {t(testing ? "maintenance.testingModel" : "maintenance.test")}
          </button>
        </SRow>
      </div>
    </section>
  );
}

export function ProviderHistory({ m }: { m: Machine }) {
  const { t } = useTranslation("settings");
  const { sending, run } = useProviderOperationRunner();
  const jobs = [...(m.status?.providerOperations ?? [])].sort((a, b) => Number(b.updatedUnixMs - a.updatedUnixMs));
  return (
    <div className="form">
      {!jobs.length ? (
        <p className="set-foot">{t("maintenance.empty")}</p>
      ) : (
        <div>
          <div className="fgroup" role="list" aria-label={t("maintenance.history")}>
            {jobs.map((job) => {
              const agent = job.request?.provider ?? "";
              const act = actions.find((a) => a === job.request?.action) ?? "unknown";
              const canCancel = m.online && (job.phase === "preflight" || job.phase === "waiting");
              return (
                <div className="frow" role="listitem" key={job.request?.requestId}>
                  <AgentTile agent={agent} size={26} />
                  <div className="ft">
                    <div className="fn">{codingAgentTitle(agent)}</div>
                    <div className="fdesc">
                      {t(`maintenance.${act}`)} {job.request?.targetVersion} · {new Date(Number(job.updatedUnixMs)).toLocaleString()}
                    </div>
                    {job.detail && <div className="fdesc">{job.detail}</div>}
                    {!m.online && providerActive(job.phase) && <div className="fdesc">{t("maintenance.offline")}</div>}
                  </div>
                  <span className={`fv-side ${job.phase === "failed" || job.phase === "interrupted" ? "warn" : ""}`}>
                    {providerActive(job.phase) && m.online && <Mark size={11} variant="seq" />}
                    {t(`maintenance.${providerPhaseKey(job.phase)}`)}
                  </span>
                  {canCancel && (
                    <button type="button" className="btn" disabled={sending} onClick={() => void run(m, agent, "cancel", job.request?.expectedVersion ?? "", job.request?.targetVersion ?? "", job)}>
                      {t("maintenance.cancel")}
                    </button>
                  )}
                </div>
              );
            })}
          </div>
          <p className="set-foot">{t("maintenance.historyNote", { machine: m.machineName })}</p>
        </div>
      )}
    </div>
  );
}
