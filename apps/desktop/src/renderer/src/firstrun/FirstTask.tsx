import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { codingAgentTitle, displayTitle } from "@hermesh/core";
import { kindNamed, Mark, personalAgents } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { PersonalAgentIcon } from "../components";
import { Icon } from "../icons";
import { useStore } from "../store";
import { Frame } from "./Frame";
import { setProgress } from "./progress";

/**
 * The first run's last page: the agent just connected, one thing to send it
 * that shows the whole way works (a coding agent saying hello in a project
 * it was given), then its first session as soon as it starts (the window
 * opens on it), or Continue to Hermesh.
 */
export function FirstTask({ agent }: { agent: string }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const [copied, setCopied] = useState(false);
  const kind = kindNamed(agent) ?? personalAgents[personalAgents.length - 1];
  const client = st.clients.find((c) => c.label === agent && !c.revoked);
  const prompt = helloPrompt(st, client?.spec?.agents ?? [], client?.spec?.projectIds ?? []);
  const first = client
    ? st.sessions.filter((s) => s.createdBy === client.clientId).sort((a, b) => ms(a.createdAt) - ms(b.createdAt))[0]
    : undefined;
  const firstId = first?.sessionId;

  useEffect(() => {
    if (!firstId) return;
    try {
      new Notification(t("firstTask.notification", { name: agent }), { body: first ? displayTitle(first) : "" });
    } catch {
      // Notifications are a courtesy here; the window opens on the session anyway.
    }
    const timer = setTimeout(() => {
      setProgress(null);
      st.select(firstId);
    }, 1300);
    return () => clearTimeout(timer);
  }, [firstId]); // once, when the first session appears

  return (
    <Frame
      foot={
        <button className="btn" style={{ marginLeft: "auto" }} onClick={() => setProgress(null)}>
          {t("firstTask.enter")}
        </button>
      }
    >
      <div className="status">
        <div className="ft-icon">
          <PersonalAgentIcon kind={kind} size={64} />
          <span className="ft-ok" aria-hidden="true">
            <Icon name="check" />
          </span>
        </div>
        <h2 className="ob-h">{t("firstTask.title", { name: agent })}</h2>
        <p className="ob-p">{t("firstTask.tryIt", { name: agent })}</p>
        <div className="ft-prompt">
          <span className="sel-text">{prompt}</span>
          <button className={`btn sm ${copied ? "" : "primary"}`} onClick={() => (navigator.clipboard.writeText(prompt).catch(() => {}), setCopied(true))}>
            <Icon name={copied ? "check" : "copy"} />
            {copied ? t("copied") : t("copy")}
          </button>
        </div>
        <div className="status-note">
          <Mark size={16} variant={first ? "lock" : "seq"} />
          {first ? t("firstTask.started", { name: agent }) : t("firstTask.waiting")}
        </div>
      </div>
    </Frame>
  );
}

/**
 * A hello that is sure to run: a coding agent it was given that is ready on
 * this Mac, in a project it was given that is set up for that agent.
 */
export function helloPrompt(st: ReturnType<typeof useStore>, granted: string[], grantedProjects: string[]): string {
  const local = st.machines.find((m) => m.local);
  const ready = (local?.status?.agents ?? []).filter((a) => a.available && a.loggedIn).map((a) => a.name);
  const agents = granted.filter((a) => a !== "*");
  const coder = (agents.length > 0 ? agents : ready).find((a) => ready.includes(a)) ?? agents[0] ?? ready[0];
  const projects = st.projects.filter((p) => p.machineId === local?.machineId && (!coder || p.agents.length === 0 || p.agents.includes(coder)));
  const allowed = grantedProjects.filter((p) => p !== "*");
  const project = (allowed.length > 0 ? projects.find((p) => allowed.includes(p.projectId)) : projects[0])?.projectId ?? allowed[0];
  const who = coder ? codingAgentTitle(coder) : i18n.t("firstrun:firstTask.codingAgent");
  return project ? i18n.t("firstrun:firstTask.promptInProject", { project, agent: who }) : i18n.t("firstrun:firstTask.prompt", { agent: who });
}

const ms = (t: Parameters<typeof timestampMs>[0] | undefined) => (t ? timestampMs(t) : 0);
