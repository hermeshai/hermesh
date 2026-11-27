import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { AgentProblem } from "@hermesh/proto/machine";
import { codingAgentTitle } from "@hermesh/core";
import { local } from "../api";
import { useStore } from "../store";

const actions = ["install", "update", "verify", "enable_auto", "disable_auto", "cancel"] as const;
type Action = (typeof actions)[number];
const phases = ["preflight", "waiting", "installing", "verifying", "succeeded", "failed", "interrupted", "cancelled"] as const;
const active = (phase: string) => ["preflight", "waiting", "installing", "verifying"].includes(phase);

export function ProviderMaintenance({ machine, agent }: { machine: Machine; agent: string }) {
  const { t } = useTranslation("settings");
  const store = useStore();
  const [sending, setSending] = useState(false);
  const [testing, setTesting] = useState(false);
  const [modelResult, setModelResult] = useState("");
  const receipt = useRef({ key: "", id: "" });
  const cap = machine.status?.providerMaintenance.find((c) => c.provider === agent);
  if (!cap) return null;
  const info = machine.status?.agents.find((a) => a.name === agent);
  const latest = machine.status?.providerOperations.findLast((j) => j.request?.provider === agent);
  const disabled = !machine.online || sending || active(latest?.phase ?? "");
  const run = async (action: Action) => {
    const key = [machine.machineId, agent, action, cap.managedVersion, cap.targetVersion].join("/");
    if (receipt.current.key !== key || receipt.current.id === latest?.request?.requestId)
      receipt.current = { key, id: crypto.randomUUID() };
    setSending(true);
    try {
      await store.run(() => local.maintainProvider({
        machineId: machine.machineId,
        operation: { provider: agent, action, requestId: action === "cancel" ? latest!.request!.requestId : receipt.current.id, expectedVersion: cap.managedVersion, targetVersion: cap.targetVersion },
      }));
    } finally { setSending(false); }
  };
  const testModel = async () => {
    setTesting(true);
    setModelResult("");
    try {
      await store.run(async () => {
        const result = await local.testMachineAgent({ machineId: machine.machineId, agent });
        const ready = result.ok && /^ready[.!]?$/i.test(result.reply.trim());
        setModelResult(t(ready ? "maintenance.modelReply" : "maintenance.modelFailed", { reply: result.reply, ms: result.millis }));
      });
    } finally { setTesting(false); }
  };
  const button = (action: Action) => <button type="button" className="btn" disabled={disabled} onClick={() => void run(action)}>{t(`maintenance.${action}`)}</button>;
  const canInstall = cap.package && !cap.managedVersion && !info?.available && !info?.checking &&
    (!info || info.problem === AgentProblem.NOT_FOUND || info.problem === AgentProblem.APP_WITHOUT_CLI);
  return <section>
    {cap.package && (canInstall || cap.managedVersion) && <p className="set-foot">{t("maintenance.source", { machine: machine.machineName, account: cap.account, pkg: cap.package, version: cap.targetVersion })}</p>}
    {info?.available && !cap.managedVersion && <p className="set-foot">{t("maintenance.external")}</p>}
    <div className="set-acts">
      {(latest?.phase === "preflight" || latest?.phase === "waiting") && <button type="button" className="btn" disabled={!machine.online || sending} onClick={() => void run("cancel")}>{t("maintenance.cancel")}</button>}
      {canInstall && button("install")}
      {cap.managedVersion && cap.managedVersion !== cap.targetVersion && button("update")}
      {cap.managedVersion && button(cap.autoUpdate ? "disable_auto" : "enable_auto")}
      {info?.available && button("verify")}
      {info?.available && <button type="button" className="btn" disabled={disabled || testing} onClick={() => void testModel()}>{t(testing ? "maintenance.testingModel" : "maintenance.testModel")}</button>}
    </div>
    {modelResult && <p role="status" className="set-foot">{modelResult}</p>}
    {latest && <p role="status" className="set-foot">{t(`maintenance.${phases.find((p) => p === latest.phase) ?? "unknown"}`)} {latest.detail}</p>}
    {!machine.online && <p className="set-foot">{t("maintenance.offline")}</p>}
  </section>;
}

export function ProviderHistory() {
  const { t } = useTranslation("settings");
  const { machines } = useStore();
  const jobs = machines.flatMap((m) => (m.status?.providerOperations ?? []).map((job) => ({ m, job })))
    .sort((a, b) => Number(b.job.updatedUnixMs - a.job.updatedUnixMs));
  return <div className="form">
    {!jobs.length && <p className="set-foot">{t("maintenance.empty")}</p>}
    {jobs.map(({ m, job }) => <div className="frow" key={m.machineId + job.request?.requestId}>
      <div className="ft">
        <div className="fn">{codingAgentTitle(job.request?.provider ?? "")} · {m.machineName}</div>
        <div className="fdesc">{t(`maintenance.${actions.find((a) => a === job.request?.action) ?? "unknown"}`)} {job.request?.targetVersion} · {new Date(Number(job.updatedUnixMs)).toLocaleString()}</div>
        {job.detail && <div className="fdesc">{job.detail}</div>}
        {!m.online && <div className="fdesc">{t("maintenance.offline")}</div>}
      </div>
      <span className="fv-side">{t(`maintenance.${phases.find((p) => p === job.phase) ?? "unknown"}`)}</span>
    </div>)}
  </div>;
}
