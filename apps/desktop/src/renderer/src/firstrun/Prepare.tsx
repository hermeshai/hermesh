import { useRef, useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { codingAgentIds } from "@hermesh/core";
import { AgentLogo, Mark } from "@hermesh/ui";
import { AgentList, AgentPage, projectsFor, ready, sortAgents } from "../AgentList";
import { useCheckAgents } from "../agents";
import { Icon } from "../icons";
import { useStore } from "../store";
import { Frame, StepIcon } from "./Frame";
import { setProgress, useProgress } from "./progress";

// "Check and Manage Available Agents": every coding agent Hermesh supports, what this Mac
// has of each (version, logged in), each installed one opening its page: its
// default model and the projects it may work in (AgentList and AgentPage, as
// settings shows a machine's too). Nothing there must be set to go on. Opening the
// page checks the agents (each CLI is run: they are checked only when
// something needs them); until the first comes in, the page says it is
// looking for them, and an agent whose check takes longer shows as being
// checked.

export function Prepare() {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const machine = st.machines.find((m) => m.local);
  const projects = st.projects.filter((p) => p.machineId === machine?.machineId);
  const agents = sortAgents(machine?.status?.agents);
  const { searching } = agents;
  useCheckAgents();
  const canGo = agents.installed.some((a) => ready(a) && projectsFor(projects, a.name).length > 0);
  // Found after the person watched the search: the page comes in.
  const searched = useRef(searching).current;
  const [open, setOpen] = useState("");
  const progress = useProgress();
  // Joined an owner: this Mac is all there is left to set up.
  const next = progress?.step === "prepare" && progress.joined ? null : ({ step: "agent" } as const);

  return (
    <Frame
      className={!searching && searched ? "ob-in" : undefined}
      foot={
        <>
          <p className="promise">
            <Icon name="shield" />
            {t("prepare.promise")}
          </p>
          <button className="link" onClick={() => setProgress(next)}>
            {t("prepare.later")}
          </button>
          <button className="btn primary" disabled={!canGo} onClick={() => setProgress(next)}>
            {t(next ? "prepare.next" : "prepare.done")}
          </button>
        </>
      }
    >
      {!searching && machine && open ? (
        <>
          <button className="ag-back" onClick={() => setOpen("")}>
            <Icon name="chevR" />
            {t("prepare.backToAgents")}
          </button>
          <AgentPage m={machine} agent={open} />
        </>
      ) : !searching && machine ? (
        <>
          <StepIcon name="laptop" />
          <h2 className="ob-h">{t("prepare.title")}</h2>
          <AgentList m={machine} agents={agents} onOpen={setOpen} />
        </>
      ) : (
        <Searching />
      )}
    </Frame>
  );
}

/** Looking for the agents: the mark at work, and every agent Hermesh supports lit in turn. */
function Searching() {
  const { t } = useTranslation("firstrun");
  return (
    <div className="status" role="status">
      <Mark size={64} variant="pulse" />
      <h2 className="ob-h">{t("prepare.searching")}</h2>
      <p className="ob-p">{t("prepare.searchingBody")}</p>
      <div className="ag-sweep" aria-hidden="true" style={{ "--n": codingAgentIds.length } as CSSProperties}>
        {codingAgentIds.map((name, i) => (
          <span key={name} style={{ "--i": i } as CSSProperties}>
            <AgentLogo agent={name} size={20} />
          </span>
        ))}
      </div>
    </div>
  );
}
