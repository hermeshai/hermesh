import { useCallback, useEffect, useState } from "react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { WorkspaceBlock, type ListWorkspacesResponse, type Workspace } from "@hermesh/proto/machine";
import { agoText, dateOf } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local } from "../api";
import { Icon } from "../icons";
import { machineAnswers, needsUpdate } from "../skew";
import { errorText, useNow, useStore, type Store } from "../store";
import { formatSize } from "./Environment";

// An environment's storage (docs/settings-environments.md §5.7), as System Settings' Storage:
// what its tasks' worktrees take, by project; recommendations; a project's worktrees in a table,
// the idle ones released together. Releasing keeps each task's branch.

const sum = (ws: Workspace[]) => ws.reduce((n, w) => n + Number(w.sizeBytes), 0);
const releasable = (w: Workspace) => w.block === WorkspaceBlock.UNSPECIFIED;
/** The bar's colors, one per project in turn. */
const tones = ["--bar-1", "--bar-2", "--bar-3", "--bar-4", "--bar-5"];

/** A machine's worktrees: the last scan when cached, else a new one; again with reload. */
export function useWorkspaces(m: Machine, cached: boolean, enabled = true) {
  const [res, setRes] = useState<ListWorkspacesResponse>();
  const [error, setError] = useState<unknown>();
  const [loading, setLoading] = useState(false);
  const load = useCallback(
    async (fresh: boolean) => {
      setLoading(true);
      try {
        setRes(await local.listWorkspaces({ machineId: m.machineId, cached: !fresh }));
        setError(undefined);
      } catch (e) {
        setError(e);
      } finally {
        setLoading(false);
      }
    },
    [m.machineId],
  );
  useEffect(() => {
    if (enabled) void load(!cached);
  }, [enabled, cached, load]);
  return { res, error, loading, reload: load };
}

/** Why a worktree stays. */
export function blockText(t: TFunction<"settings">, w: Workspace): string {
  switch (w.block) {
    case WorkspaceBlock.BUSY:
      return t("storage.busy");
    case WorkspaceBlock.CHANGES:
      return t("storage.changes");
    case WorkspaceBlock.LOCAL_FILES:
      return t("storage.localFiles", { path: w.blockPath });
    default:
      return t("storage.unverified");
  }
}

/** Releases worktrees and says how it went; returns once the list is read again. */
async function release(st: Store, t: TFunction<"settings">, m: Machine, ws: Workspace[], reload: () => Promise<void>) {
  try {
    const res = await local.releaseWorkspaces({ machineId: m.machineId, sessionIds: ws.map((w) => w.sessionId) });
    const done = res.results.filter((r) => r.released).length;
    const kept = res.results.length - done;
    if (done === 1 && ws.length === 1) st.show(t("storage.releasedOne", { title: ws[0].title || ws[0].sessionId }));
    else if (done > 0) st.show(t("storage.released", { count: done }));
    if (kept > 0) st.show(t("storage.keptSome", { count: kept }));
  } catch (e) {
    st.show(errorText(e));
  }
  await reload();
}

export function Storage({ m }: { m: Machine }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const answers = machineAnswers(m, "ListWorkspaces");
  const { res, error, loading, reload } = useWorkspaces(m, false, answers !== false);
  const [open, setOpen] = useState("");
  const again = () => reload(false);

  if (answers === false || (error && needsUpdate(error))) {
    return (
      <div className="form">
        <div className="set-banner" role="status">
          <Icon name="info" />
          <div className="ft">{t("storage.needsUpdate", { name: m.machineName })}</div>
        </div>
      </div>
    );
  }
  if (!res) {
    return (
      <div className="form">
        <div className="fgroup">
          <div className="frow muted" role="status">
            {loading && <Mark size={14} variant="seq" />}
            {error ? errorText(error) : t("storage.scanning")}
          </div>
        </div>
      </div>
    );
  }
  const ws = res.workspaces;
  const projects = [...new Set(ws.map((w) => w.projectId))].sort((a, b) => sum(ws.filter((w) => w.projectId === b)) - sum(ws.filter((w) => w.projectId === a)));
  const tone = (p: string) => tones[projects.indexOf(p) % tones.length];
  const idle = ws.filter(releasable);
  const total = sum(ws);
  const askAuto = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("storage.autoAskTitle"),
        message: t("storage.autoAskMessage"),
        button: t("storage.autoAskButton"),
        safe: true,
        run: async () => {
          await local.setWorkspacePolicy({ machineId: m.machineId, autoRelease: true });
          await reload(false);
        },
      },
    });
  const autoOff = () => void st.run(async () => {
    await local.setWorkspacePolicy({ machineId: m.machineId, autoRelease: false });
    await reload(false);
  });
  const askFree = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("storage.freeAskTitle", { count: idle.length }),
        message: t("storage.freeAskMessage", { size: formatSize(sum(idle)) }),
        button: t("storage.releaseButton"),
        run: () => release(st, t, m, idle, again),
      },
    });

  return (
    <div className="form">
      <div className="fgroup set-store" aria-label={t("storage.title")}>
        <div className="set-store-h">
          <b>{t("storage.title")}</b>
          <span>{loading ? t("storage.scanning") : t("storage.used", { size: formatSize(total) })}</span>
        </div>
        <div className="set-bar" role="img" aria-label={projects.map((p) => `${p} ${formatSize(sum(ws.filter((w) => w.projectId === p)))}`).join(", ")}>
          {projects.map((p) => <i key={p} style={{ flex: sum(ws.filter((w) => w.projectId === p)) || 1, background: `var(${tone(p)})` }} />)}
        </div>
        {projects.length > 0 && (
          <div className="set-key">
            {projects.map((p) => (
              <span key={p}>
                <i style={{ background: `var(${tone(p)})` }} />
                {p}
              </span>
            ))}
          </div>
        )}
      </div>
      <div>
        <div className="fhead">{t("storage.recs")}</div>
        <div className="fgroup">
          <div className="frow" aria-label={t("storage.autoTitle")}>
            <span className="set-tile blue md"><Icon name="clock" /></span>
            <div className="ft">
              <div>{t("storage.autoTitle")}</div>
              <div className="fdesc">{t("storage.autoDesc")}</div>
            </div>
            {res.autoRelease ? (
              <span className="set-done">
                <Icon name="check" />
                {t("storage.on")}
                <button className="link" onClick={autoOff}>{t("storage.turnOff")}</button>
              </span>
            ) : (
              <button className="btn" onClick={askAuto}>{t("storage.turnOn")}</button>
            )}
          </div>
          <div className="frow" aria-label={t("storage.freeTitle")}>
            <span className="set-tile sage md"><Icon name="broom" /></span>
            <div className="ft">
              <div>{t("storage.freeTitle")}</div>
              <div className="fdesc">{idle.length ? t("storage.freeDesc", { count: idle.length, size: formatSize(sum(idle)) }) : t("storage.freeNone")}</div>
            </div>
            {idle.length ? (
              <button className="btn" onClick={askFree}>{t("storage.free")}</button>
            ) : (
              <span className="set-done">
                <Icon name="check" />
                {t("storage.done")}
              </span>
            )}
          </div>
        </div>
      </div>
      <div>
        <div className="fgroup" role="list" aria-label={t("env.projects")}>
          {projects.length === 0 && <div className="frow muted">{t("storage.empty")}</div>}
          {projects.map((p) => {
            const mine = ws.filter((w) => w.projectId === p);
            return (
              <div key={p} className="frow" role="listitem" aria-label={p}>
                <span className="set-tile sage"><Icon name="folder" /></span>
                <div className="ft">
                  <div className="fn">{p}</div>
                  <div className="fdesc">{t("storage.dirs", { count: mine.length })}</div>
                </div>
                <span className="fv-side">{formatSize(sum(mine))}</span>
                <button className="set-info" aria-label={t("storage.info")} title={t("storage.info")} onClick={() => setOpen(p)}>
                  <Icon name="info" />
                </button>
              </div>
            );
          })}
        </div>
        <p className="set-foot">{t("storage.note", { name: m.machineName })}</p>
      </div>
      {open && <ProjectSheet m={m} project={open} ws={ws.filter((w) => w.projectId === open)} reload={again} close={() => setOpen("")} />}
    </div>
  );
}

/** A project's worktrees in a table: the releasable ones picked and released together. */
function ProjectSheet({ m, project, ws, reload, close }: { m: Machine; project: string; ws: Workspace[]; reload: () => Promise<void>; close: () => void }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(60_000);
  const [picked, setPicked] = useState<string[]>([]);
  const path = st.projects.find((p) => p.machineId === m.machineId && p.projectId === project)?.path ?? "";
  const sel = ws.filter((w) => picked.includes(w.sessionId));
  const toggle = (w: Workspace) => releasable(w) && setPicked(picked.includes(w.sessionId) ? picked.filter((x) => x !== w.sessionId) : [...picked, w.sessionId]);
  useEffect(() => {
    if (ws.length === 0) close();
  }, [ws.length, close]);
  const ask = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("storage.selAskTitle", { count: sel.length }),
        message: t("storage.selAskMessage", { size: formatSize(sum(sel)) }),
        button: t("storage.releaseButton"),
        run: async () => {
          await release(st, t, m, sel, reload);
          setPicked([]);
        },
      },
    });
  const reveal = sel[0]?.path ?? ws[0]?.path;
  return (
    <>
      <div className="sheet-scrim" />
      <div className="sheet set-sheet" role="dialog" aria-label={project}>
        <h3>{project}</h3>
        <p className="sub">
          {path && <span className="mono" title={path}>{path}</span>}
          {t("storage.dirs", { count: ws.length })} · {formatSize(sum(ws))}
        </p>
        <div className="set-th">
          <span className="c1">{t("storage.colTask")}</span>
          <span className="c2">{t("storage.colActive")}</span>
          <span className="c3">{t("storage.colSize")}</span>
        </div>
        <div className="fgroup set-table" role="listbox" aria-multiselectable="true" aria-label={project}>
          {ws.map((w) => {
            const can = releasable(w);
            const at = dateOf(w.lastActive);
            return (
              <div
                key={w.sessionId}
                className={`frow ${picked.includes(w.sessionId) ? "sel" : ""} ${can ? "" : "dim"}`}
                role="option"
                aria-selected={picked.includes(w.sessionId)}
                aria-disabled={!can}
                aria-label={w.title || w.sessionId}
                tabIndex={can ? 0 : -1}
                onClick={() => toggle(w)}
                onKeyDown={(e) => e.key === " " && (e.preventDefault(), toggle(w))}
              >
                <span className="c1">
                  <div className="fn">{w.title || w.sessionId}</div>
                  {!can && <div className="fdesc warn">{blockText(t, w)}</div>}
                </span>
                <span className="c2">{w.block === WorkspaceBlock.BUSY ? t("storage.now") : at ? agoText(at, now) : "—"}</span>
                <span className="c3">{w.sizeError ? "—" : formatSize(w.sizeBytes)}</span>
              </div>
            );
          })}
        </div>
        <p className="set-foot">{t("storage.sheetNote")}</p>
        <div className="sheet-foot">
          {m.local && reveal && <button className="btn" onClick={() => window.hermesh?.openFolder(reveal, "files")}>{t("storage.showInFinder")}</button>}
          <span className="push" />
          <button className="btn" disabled={sel.length === 0} onClick={ask}>{sel.length ? t("storage.releaseSel", { size: formatSize(sum(sel)) }) : t("storage.releaseNone")}</button>
          <button className="btn primary" onClick={close}>{t("storage.doneButton")}</button>
        </div>
      </div>
    </>
  );
}
