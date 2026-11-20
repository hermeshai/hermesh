import { useState } from "react";
import { codingAgentTitle } from "@hermesh/core";
import { CodingConsentScope } from "@hermesh/proto/session";
import { local } from "../api";
import { errorText, useStore } from "../store";

export function CodingSharing() {
  const st = useStore();
  const [machineId, setMachineId] = useState("");
  const [projectId, setProjectId] = useState("");
  const [agent, setAgent] = useState("");
  const [scope, setScope] = useState(CodingConsentScope.HER);
  const [busy, setBusy] = useState(false);
  const machine = st.machines.find((m) => m.machineId === machineId);
  const projects = st.projects.filter((p) => p.machineId === machineId);
  const project = projects.find((p) => p.projectId === projectId);
  const agents = (machine?.status?.agents ?? []).filter((a) => !project?.agents.length || project.agents.includes(a.name));
  const consent = { agent, projectId, scope };

  async function review() {
    setBusy(true);
    try {
      const shown = await local.codingConsent({ machineId, consent });
      if (shown.local || shown.approved) {
        st.show(shown.local ? "This coding agent runs in the offline demo." : "This coding agent’s current route is approved for this use.");
        return;
      }
      st.set({
        sheet: {
          kind: "confirm",
          title: "Share with this coding agent?",
          message: `${shown.recipient} · ${machine?.machineName ?? shown.machineId}\n\n${shown.route}\n\nData: ${shown.dataCategories}\n\nPurpose: ${shown.purpose}\n\nYou can revoke future sharing here. Data already sent cannot be recalled. Approval expires when the execution host restarts.`,
          button: "Allow Coding Agent Sharing",
          run: async () => {
            await local.codingConsent({ machineId, consent: { ...consent, approveVersion: shown.version } });
            st.show("Coding agent sharing approved.");
          },
        },
      });
    } catch (error) { st.show(errorText(error)); }
    finally { setBusy(false); }
  }

  async function revoke() {
    setBusy(true);
    try {
      await local.codingConsent({ machineId, consent: { ...consent, revoke: true } });
      st.show("Coding agent sharing revoked.");
    } catch (error) { st.show(errorText(error)); }
    finally { setBusy(false); }
  }

  return (
    <section>
      <h3 className="fhead">Coding agent sharing</h3>
      <div className="fgroup">
        <label className="frow">Execution host
          <select value={machineId} onChange={(e) => { setMachineId(e.target.value); setProjectId(""); setAgent(""); }}>
            <option value="">Choose a host</option>
            {st.machines.map((m) => <option key={m.machineId} value={m.machineId}>{m.machineName}</option>)}
          </select>
        </label>
        <label className="frow">Project
          <select value={projectId} disabled={!machineId} onChange={(e) => { setProjectId(e.target.value); setAgent(""); }}>
            <option value="">Choose a project</option>
            {projects.map((p) => <option key={p.projectId}>{p.projectId}</option>)}
          </select>
        </label>
        <label className="frow">Coding agent
          <select value={agent} disabled={!machineId} onChange={(e) => setAgent(e.target.value)}>
            <option value="">Choose an agent</option>
            {agents.map((a) => <option key={a.name} value={a.name}>{codingAgentTitle(a.name)}</option>)}
          </select>
        </label>
        <label className="frow">Use
          <select value={scope} onChange={(e) => setScope(Number(e.target.value))}>
            <option value={CodingConsentScope.HER}>Her coding handoffs</option>
            <option value={CodingConsentScope.MOBILE_ANSWER}>iPhone answers</option>
          </select>
        </label>
        <button className="frow" disabled={busy || !projectId || !agent} onClick={() => void review()}>Review and allow sharing</button>
        <button className="frow" disabled={busy || !agent} onClick={() => void revoke()}>Revoke coding agent sharing</button>
      </div>
    </section>
  );
}
