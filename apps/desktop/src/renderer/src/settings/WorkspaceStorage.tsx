import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { LocalWorkspace } from "@hermesh/proto/local";
import { SessionStatus } from "@hermesh/proto/session";
import { local } from "../api";
import { errorText, useStore } from "../store";
import { SRow } from "./common";

function formatSize(bytes: bigint): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = Number(bytes);
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })} ${units[unit]}`;
}

export function WorkspaceStorage() {
  const { t } = useTranslation("settings");
  const st = useStore();
  const [rows, setRows] = useState<LocalWorkspace[]>();
  const [scanning, setScanning] = useState(false);
  const [releasing, setReleasing] = useState("");
  const [error, setError] = useState("");
  const scanRequest = useRef<AbortController | null>(null);
  useEffect(() => () => scanRequest.current?.abort(), []);

  async function scan() {
    const controller = new AbortController();
    scanRequest.current = controller;
    setScanning(true);
    setError("");
    try {
      const result = await local.listLocalWorkspaces({}, { signal: controller.signal });
      setRows(result.workspaces.toSorted((a, b) => Number(b.sizeBytes - a.sizeBytes)));
    } catch (e) {
      if (!controller.signal.aborted) setError(errorText(e));
    } finally {
      setScanning(false);
    }
  }

  async function release(row: LocalWorkspace) {
    setReleasing(row.sessionId);
    setError("");
    try {
      await local.releaseLocalWorkspace({ sessionId: row.sessionId });
      setRows((current) => current?.filter((x) => x.sessionId !== row.sessionId));
      st.show(t("workspaces.released", { title: row.title || row.sessionId }));
    } catch (e) {
      setError(t("workspaces.kept", { path: row.path, reason: errorText(e) }));
    } finally {
      setReleasing("");
    }
  }

  const statuses = new Map(st.sessions.map((s) => [s.sessionId, s.status]));
  const projects = new Map<string, LocalWorkspace[]>();
  for (const row of rows ?? []) {
    const group = projects.get(row.projectId);
    if (group) group.push(row);
    else projects.set(row.projectId, [row]);
  }
  return (
    <section aria-label={t("workspaces.title")}>
      <div className="fhead">{t("workspaces.title")}</div>
      <div className="fgroup">
        <SRow title={t("workspaces.usage")} desc={t("workspaces.note")}>
          <button className="btn sm" disabled={scanning || !!releasing} onClick={scan}>
            {t(scanning ? "workspaces.scanning" : rows ? "workspaces.refresh" : "workspaces.scan")}
          </button>
        </SRow>
        {rows?.length === 0 && <div className="frow muted">{t("workspaces.empty")}</div>}
        {Array.from(projects, ([project, group]) => (
          <details key={project}>
            <summary className="workspace-summary">
              {project} · {formatSize(group.reduce((sum, x) => sum + x.sizeBytes, 0n))}
              {group.some((x) => x.sizeError) && ` (${t("workspaces.partial")})`}
            </summary>
            {group.map((row) => {
              const status = statuses.get(row.sessionId) ?? row.status;
              return (
                <SRow key={row.sessionId} title={row.title || row.sessionId} desc={row.sizeError ? t("workspaces.sizeError", { reason: row.sizeError }) : formatSize(row.sizeBytes)}>
                  <button className="link" title={row.path} onClick={() => window.hermesh?.openFolder(row.path, "files")}>{t("workspaces.open")}</button>
                  <button className="btn sm" disabled={scanning || !!releasing || status !== SessionStatus.IDLE} onClick={() => release(row)}>
                    {t(releasing === row.sessionId ? "workspaces.releasing" : "workspaces.release")}
                  </button>
                </SRow>
              );
            })}
          </details>
        ))}
      </div>
      {error && <p className="set-note" role="alert" style={{ overflowWrap: "anywhere", userSelect: "text" }}>{error}</p>}
    </section>
  );
}
