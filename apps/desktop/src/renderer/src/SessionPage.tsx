import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { clone, create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { RuntimeMode, Workspace } from "@hermesh/proto/identity";
import type { Project } from "@hermesh/proto/local";
import {
  DispatchIntent,
  InteractionMode,
  ModelSelectionSchema,
  QueueOperation,
  SessionSchema,
  SessionStatus,
  WorkspaceStrategySchema,
  type AttachmentRef,
  type BackgroundTask,
  type ModelSelection,
  type ProviderInstance,
  type SessionProjection,
} from "@hermesh/proto/session";
import {
  SCRATCH_PROJECT,
  activeBackgroundTasksOf,
  applySessionEvent,
  backgroundTasksOf,
  clampRuntimeMode,
  clockText,
  cycleReasoningSelection,
  dateOf,
  defaultModelSelectionFor,
  displayTitle,
  elapsedSeconds,
  findProviderInstance,
  groupSteps,
  isInterruption,
  isSessionRunningOrWaiting,
  pendingRequestOf,
  projectOnMachine,
  providerUnusable,
  queuedRunsOf,
  replyEnds,
  sessionFailure,
  sessionLiveReplies,
  sessionToolRunning,
  sessionTranscript,
  statusTitle,
} from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local, sessions as sessionApi } from "./api";
import { ConnectionCapsule } from "./ConnectionCapsule";
import { Icon } from "./icons";
import { layoutOf, setCard } from "./layout";
import { keys, ownKeys } from "./platform";
import { grantLabel, unarchive } from "./Sidebar";
import { errorText, useNow, useStore } from "./store";
import { ChatFrame, Failed, Row, Stale, Unread, rowHeights, rowsOf } from "./session/Chat";
import { ComposerPlace, LaunchAsk, ModelButtons, ThreadComposer, type Branches, type Place } from "./session/Composer";
import { Inspector } from "./session/Inspector";
import { SessionRequest } from "./session/Request";
import { keepProjects, keepProviders, lastProjects, lastProviders, remembered, remember } from "./session/choices";
import { ContentScope } from "./session/scope";
import "./session/markdown/markdown.css";
import "./session/session.css";
import "./session/composer.css";

/**
 * The workspace a new session asks for (docs/thread-composer.md §16): a worktree from its base (the default branch
 * unless another was chosen; from origin when asked), or the project's own folder, switched first when another branch
 * was chosen there. The temporary workspace has none to choose.
 */
function workspaceFor(place: Place | undefined, branches: Branches | undefined) {
  if (!place) return { mode: Workspace.IN_PLACE };
  if (place.worktree) return { mode: Workspace.WORKTREE, baseRef: place.base || branches?.default || "", fromRemote: place.fromRemote };
  return { mode: Workspace.IN_PLACE, branch: place.on && place.on !== branches?.current ? place.on : "" };
}

/**
 * The projections opened last, newest last: going back to a conversation shows it
 * at once (also while its machine cannot be reached).
 */
const keptProjections = new Map<string, SessionProjection>();
const keepAtMost = 10;

function rememberProjection(id: string, proj: SessionProjection) {
  if (!id || proj.items.length === 0) return;
  keptProjections.delete(id);
  keptProjections.set(id, proj);
  if (keptProjections.size > keepAtMost) keptProjections.delete(keptProjections.keys().next().value!);
}

// Unified session page for every coding agent session (Owner, Her, External Grant, or CLI):
// the conversation, its info cards, runtime request / queue controls, and composer;
// or, for a new session, the project & environment picker and composer in the middle of the page.
export function SessionPage() {
  const { t } = useTranslation("thread");
  const ts = useTranslation("session").t;
  const st = useStore();
  const sel = st.selection;
  const activeSessionId = sel.kind === "session" ? sel.id : undefined;
  const listedSession = activeSessionId ? st.sessions.find((x) => x.sessionId === activeSessionId) : undefined;

  const [projection, setProjection] = useState<SessionProjection | null>(() =>
    activeSessionId ? keptProjections.get(activeSessionId) ?? null : null,
  );
  /** The session's events are followed: its subscription has been open a moment (the effect that follows it). */
  const [following, setFollowing] = useState(false);
  const [providers, setProviders] = useState<ProviderInstance[]>([]);
  /** The providers shown are those last read on the machine, which is offline (choices.ts). */
  const [providersStale, setProvidersStale] = useState(false);
  const [maxCeiling, setMaxCeiling] = useState<RuntimeMode>(RuntimeMode.FULL_ACCESS);

  // Each machine's projects as last read, for when it is offline (choices.ts); a machine that
  // answers has its own, live.
  useEffect(() => {
    const answered = st.machines.filter((m) => (m.online || m.local) && st.projects.some((p) => p.machineId === m.machineId));
    keepProjects(st.projects, answered.map((m) => m.machineId));
  }, [st.projects, st.machines]);
  const projectsOn = (mid: string): { list: Project[]; stale: boolean } => {
    const live = st.projects.filter((p) => p.machineId === mid);
    const m = st.machines.find((x) => x.machineId === mid);
    if (!m || m.online || m.local) return { list: live, stale: false };
    const list = live.length > 0 ? live : lastProjects(mid);
    return { list, stale: list.length > 0 };
  };

  // A new session's where: as the entry asked (a project group's ＋), else as last time (choices.ts).
  const [machineId, setMachineId] = useState<string>("");
  const [projectId, setProjectId] = useState<string>(SCRATCH_PROJECT);
  // Where it works and on which branch (§16): only this session's, never remembered.
  const [place, setPlace] = useState<Place>({ worktree: true, base: "", on: "", fromRemote: false });
  const [branches, setBranches] = useState<Branches | undefined>();

  // Composer controls
  const [modelSel, setModelSel] = useState<ModelSelection>(() =>
    create(ModelSelectionSchema, { instanceId: "", model: "", options: {} }),
  );
  const [runtimeMode, setRuntimeMode] = useState<RuntimeMode>(RuntimeMode.AUTO);
  const [interactionMode, setInteractionMode] = useState<InteractionMode>(InteractionMode.CODE);
  const [prompt, setPrompt] = useState<string>("");
  const [attachments, setAttachments] = useState<AttachmentRef[]>([]);
  const [slashOpen, setSlashOpen] = useState<boolean>(false);
  const [busySend, setBusySend] = useState<boolean>(false);
  const [stopRequestedRun, setStopRequestedRun] = useState<string>("");

  // Queue edit state
  const [editingRunId, setEditingRunId] = useState<string>("");
  const [editingText, setEditingText] = useState<string>("");
  const [selectedBgTaskId, setSelectedBgTaskId] = useState<string>("");

  // Each time a new session opens (and once the machines are known): the machine and project its entry
  // named, else those used last; a machine that is gone falls back to this Mac.
  const askedMachine = sel.kind === "newSession" ? sel.machineId : undefined;
  const askedProject = sel.kind === "newSession" ? sel.projectId : undefined;
  const machinesKnown = st.machines.length > 0;
  useEffect(() => {
    if (sel.kind !== "newSession" || !machinesKnown) return;
    const last = remembered();
    const known = (id?: string) => !!id && st.machines.some((m) => m.machineId === id);
    const mid = [askedMachine, last.machineId].find(known) ?? st.machines.find((m) => m.local)?.machineId ?? st.machines[0]!.machineId;
    const pid = askedProject ?? projectOnMachine(projectsOn(mid).list, mid, "", last.byMachine[mid]?.projectId);
    setMachineId(mid);
    pickProject(mid, pid);
    const mine = last.byMachine[mid];
    if (mine?.mode) setRuntimeMode(mine.mode);
    setInteractionMode(InteractionMode.CODE);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sel.kind, askedMachine, askedProject, machinesKnown]);

  /** A project, and whether a session of it gets its own worktree: as the project works. */
  const pickProject = (mid: string, pid: string) => {
    setProjectId(pid);
    const pr = projectsOn(mid).list.find((p) => p.projectId === pid);
    setPlace({ worktree: pr?.workspace === Workspace.WORKTREE, base: "", on: "", fromRemote: false });
  };
  const pickMachine = (mid: string) => {
    setMachineId(mid);
    pickProject(mid, projectOnMachine(projectsOn(mid).list, mid, projectId, remembered().byMachine[mid]?.projectId));
  };

  const currentSession = activeSessionId && projection?.session?.sessionId === activeSessionId ? projection.session : listedSession;
  const effectiveMachineId = activeSessionId ? currentSession?.machineId ?? machineId : machineId;
  const effectiveProjectId = activeSessionId ? currentSession?.projectId ?? projectId : projectId;
  const targetMachine = st.machines.find((m) => m.machineId === effectiveMachineId);
  const machineOnline = targetMachine ? targetMachine.online || targetMachine.local : true;

  // A new session's project's branches, read from its folder on the machine (only the owner's devices can): read
  // again as the page opens or the project changes, since a branch switched on the machine isn't sent here.
  const projectPath = !activeSessionId ? projectsOn(effectiveMachineId).list.find((p) => p.projectId === effectiveProjectId && p.repo)?.path : undefined;
  useEffect(() => {
    setBranches(undefined);
    if (!projectPath || !machineOnline) return;
    let cancelled = false;
    local
      .listMachineFolders({ machineId: effectiveMachineId, path: projectPath })
      .then((res) => {
        if (cancelled || !res.repo) return;
        setBranches({ current: res.branch, default: res.defaultBranch, all: res.branches, inWorktrees: res.worktreeBranches });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [effectiveMachineId, projectPath, machineOnline, sel.kind]);

  // Load providers for target machine & project
  useEffect(() => {
    if (!effectiveMachineId) return;
    let cancelled = false;
    sessionApi
      .listProviders({ machineId: effectiveMachineId, projectId: effectiveProjectId === SCRATCH_PROJECT ? "" : effectiveProjectId })
      .then((res) => {
        if (cancelled) return;
        keepProviders(effectiveMachineId, res);
        setProvidersStale(false);
        return res;
      })
      .catch(() => {
        const last = cancelled ? undefined : lastProviders(effectiveMachineId);
        if (last) setProvidersStale(true);
        return last;
      })
      .then((res) => {
        if (cancelled || !res) return;
        setProviders(res.providers);
        if (res.maxRuntimeMode !== RuntimeMode.UNSPECIFIED) {
          setMaxCeiling(res.maxRuntimeMode);
        }
        if (activeSessionId) return; // an existing session keeps its own (from GetSession)
        const usable = res.providers.filter((p) => providerUnusable(p, []) === "");
        const last = remembered().byMachine[effectiveMachineId]?.sel;
        const lastInst = last && usable.find((p) => p.instanceId === last.instanceId);
        setModelSel((prev) => {
          if (lastInst && lastInst.models.some((m) => m.slug === last.model)) return create(ModelSelectionSchema, last);
          if (prev.instanceId && usable.some((p) => p.instanceId === prev.instanceId)) return prev;
          const first = ["claude", "codex", "gemini"].map((id) => usable.find((p) => p.instanceId === id)).find(Boolean) ?? usable[0] ?? findProviderInstance(res.providers);
          return first ? defaultModelSelectionFor(first) : prev;
        });
      });
    return () => {
      cancelled = true;
    };
  }, [effectiveMachineId, effectiveProjectId, activeSessionId]);

  // The page's machine can be reached now: this Mac reaches the relay, and the machine is online and answers.
  const reachable = !targetMachine || targetMachine.local || (st.status?.relayConnected === true && targetMachine.online && !targetMachine.unreachable);

  // The session's projection, then its events as they come; read and subscribed again after the subscription ended or
  // failed (in 1 s, then up to every 30 s), and at once when its machine can be reached again.
  useEffect(() => {
    setProjection(activeSessionId ? keptProjections.get(activeSessionId) ?? null : null);
    if (!activeSessionId) {
      return;
    }
    const ctl = new AbortController();
    const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
    (async () => {
      let failures = 0;
      let applied = false;
      while (!ctl.signal.aborted) {
        let opened: ReturnType<typeof setTimeout> | undefined;
        let open = false;
        try {
          const init = await sessionApi.getSession({ sessionId: activeSessionId }, { signal: ctl.signal });
          if (init.projection) {
            const held = keptProjections.get(activeSessionId);
            const nextProj = init.projection.items.length === 0 && held && held.items.length > 0
              ? { ...held, session: init.projection.session ?? held.session }
              : init.projection;
            rememberProjection(activeSessionId, nextProj);
            setProjection(nextProj);
            setFollowing(true);
            const s = nextProj.session;
            if (!applied && s) {
              applied = true;
              if (s.modelSelection) setModelSel(s.modelSelection);
              if (s.runtimeMode) setRuntimeMode(s.runtimeMode);
              if (s.interactionMode) setInteractionMode(s.interactionMode);
              if (s.maxRuntimeMode) setMaxCeiling(s.maxRuntimeMode);
            }
          }
          opened = setTimeout(() => {
            open = true;
          }, 1000);
          for await (const ev of sessionApi.subscribeSession(
            { sessionId: activeSessionId, afterSeq: init.projection?.session?.progress?.lastEventSeq ?? 0n },
            { signal: ctl.signal },
          )) {
            setProjection((prev) => {
              const next = applySessionEvent(prev, ev);
              rememberProjection(activeSessionId, next);
              return next;
            });
            if (ev.session?.interactionMode) setInteractionMode(ev.session.interactionMode);
          }
        } catch (e) {
          if (e instanceof ConnectError && e.code === Code.NotFound) {
            if (!ctl.signal.aborted) st.set({ selection: { kind: "overview" } });
            return;
          }
          // Ended, or the machine can't be reached: tried again below.
        }
        clearTimeout(opened);
        setFollowing(false);
        if (open) failures = 0;
        if (ctl.signal.aborted) return;
        failures++;
        await wait(Math.min(30_000, 1000 * 2 ** Math.min(5, failures - 1)));
      }
    })();
    return () => {
      ctl.abort();
      setFollowing(false);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeSessionId, reachable]);

  const activeProjection = activeSessionId
    ? (projection?.session?.sessionId === activeSessionId ? projection : keptProjections.get(activeSessionId) ?? null)
    : null;
  const activeInst = findProviderInstance(providers, modelSel.instanceId);
  const clampedMode = clampRuntimeMode(runtimeMode, maxCeiling);
  const queuedRuns = useMemo(() => queuedRunsOf(activeProjection), [activeProjection]);
  const pendingReq = useMemo(() => pendingRequestOf(activeProjection), [activeProjection]);
  const allBgTasks = useMemo(() => backgroundTasksOf(activeProjection), [activeProjection]);
  const activeBgTasks = useMemo(() => activeBackgroundTasksOf(activeProjection), [activeProjection]);
  const activeAwaitedBgTasks = useMemo(() => activeBgTasks.filter((bt) => bt.awaited), [activeBgTasks]);
  const running =
    isSessionRunningOrWaiting(currentSession) &&
    (currentSession?.status !== SessionStatus.WAITING_INPUT || currentSession.activeRunId !== "");

  // Shift+Cmd/Ctrl+E cycles how hard it thinks.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "e") {
        e.preventDefault();
        setModelSel((prev) => {
          const next = cycleReasoningSelection(providers, prev);
          if (activeSessionId) {
            sessionApi.updateSession({ sessionId: activeSessionId, modelSelection: next }).catch(() => {});
          }
          return next;
        });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [providers, activeSessionId]);

  const applyConfigUpdate = async (patch: {
    sel?: ModelSelection;
    rm?: RuntimeMode;
    im?: InteractionMode;
  }) => {
    const nextSel = patch.sel ?? modelSel;
    const nextRm = patch.rm ?? runtimeMode;
    const nextIm = patch.im ?? interactionMode;
    if (patch.sel) setModelSel(patch.sel);
    if (patch.rm) setRuntimeMode(patch.rm);
    if (patch.im) setInteractionMode(patch.im);
    if (activeSessionId) {
      try {
        const res = await sessionApi.updateSession({
          sessionId: activeSessionId,
          modelSelection: nextSel,
          runtimeMode: nextRm,
          interactionMode: nextIm,
        });
        if (res.session) {
          setProjection((p) => (p ? { ...p, session: res.session } : p));
        }
      } catch (e) {
        st.show(errorText(e));
      }
    }
  };

  // Messages to a session go out one after another, in the order sent: the box is free at once, and an
  // Enter while the last one is still on its way to the machine is not lost.
  const sends = useRef<Promise<unknown>>(Promise.resolve());
  const submitMessage = async (explicitIntent?: DispatchIntent) => {
    const text = prompt.trim();
    if (!text && attachments.length === 0) return;
    if (activeSessionId) {
      const sent = attachments;
      setPrompt("");
      setAttachments([]);
      const req = {
        sessionId: activeSessionId,
        text,
        attachments: sent,
        intent: explicitIntent ?? DispatchIntent.QUEUE,
        modelSelection: modelSel.instanceId ? modelSel : undefined,
        runtimeMode: clampedMode,
        interactionMode,
      };
      const job = sends.current.then(() => sessionApi.dispatchMessage(req));
      sends.current = job.catch(() => {});
      try {
        await job;
      } catch (e) {
        st.show(errorText(e));
        setPrompt((p) => p || text);
        setAttachments((a) => (a.length ? a : sent));
      }
      return;
    }
    setBusySend(true);
    try {
      const res = await sessionApi.startSession({
        machineId: effectiveMachineId,
        projectId,
        modelSelection: modelSel.instanceId ? modelSel : undefined,
        runtimeMode: clampedMode,
        interactionMode,
        workspace: create(WorkspaceStrategySchema, workspaceFor(projectId === SCRATCH_PROJECT ? undefined : place, branches)),
        initialPrompt: text,
        attachments,
      });
      remember(effectiveMachineId, { projectId, sel: modelSel, mode: clampedMode });
      setPrompt("");
      setAttachments([]);
      if (res.session) {
        st.set({ selection: { kind: "session", id: res.session.sessionId } });
      }
    } catch (e) {
      st.show(errorText(e));
    } finally {
      setBusySend(false);
    }
  };

  const handleStop = async () => {
    if (!activeSessionId || !currentSession) return;
    const force = stopRequestedRun === currentSession.activeRunId && currentSession.activeRunId !== "";
    setStopRequestedRun(currentSession.activeRunId);
    await st.run(
      () => sessionApi.interruptSession({ sessionId: activeSessionId, force }),
      ts("actions.interrupted", { title: displayTitle(currentSession) }),
    );
  };

  const handleStopBackgroundTask = async (taskId = "") => {
    if (!activeSessionId) return;
    try {
      await sessionApi.stopBackgroundTask({ sessionId: activeSessionId, taskId });
    } catch (e) {
      st.show(errorText(e));
    }
  };

  const handleAttachFiles = async (files: FileList | null) => {
    if (!files?.length) return;
    for (const f of Array.from(files)) {
      try {
        const buf = new Uint8Array(await f.arrayBuffer());
        const up = await sessionApi.uploadBlob({
          machineId: effectiveMachineId,
          name: f.name,
          mimeType: f.type || "application/octet-stream",
          data: buf,
        });
        if (up.attachment) setAttachments((prev) => [...prev, up.attachment!]);
      } catch (e) {
        st.show(errorText(e));
      }
    }
  };

  const slashSuggestions = useMemo(() => {
    if (!slashOpen) return [];
    const q = prompt.startsWith("/") ? prompt.slice(1).toLowerCase() : "";
    const cmds = (activeInst?.slashCommands ?? []).map((c) => ({ label: c.name, desc: c.description }));
    const skills = (activeInst?.skills ?? []).map((s) => ({ label: `/${s.name}`, desc: s.description || s.path }));
    return [...cmds, ...skills].filter((x) => x.label.toLowerCase().includes(q));
  }, [slashOpen, prompt, activeInst]);

  const slashRow = slashSuggestions.length > 0 && (
    <div className="th-slash">
      {slashSuggestions.slice(0, 6).map((x) => (
        <button
          key={x.label}
          type="button"
          className="btn"
          onClick={() => {
            setPrompt(x.label + " ");
            setSlashOpen(false);
          }}
        >
          {x.label}
        </button>
      ))}
    </div>
  );

  // What runs where and with what (Composer.tsx): the model and how hard it thinks in the box; a new session's
  // working copy and branch over it (for a repository).
  const projectsHere = [...st.projects.filter((p) => p.machineId !== effectiveMachineId), ...projectsOn(effectiveMachineId).list];
  const project = projectsHere.find((p) => p.machineId === effectiveMachineId && p.projectId === effectiveProjectId);
  const choice = { machineId: effectiveMachineId, projectId: effectiveProjectId, worktree: place.worktree, sel: modelSel, mode: clampedMode, plan: false };
  const modelButtons = (
    <ModelButtons
      providers={providers}
      sel={modelSel}
      stale={providersStale}
      machineName={targetMachine?.machineName ?? effectiveMachineId}
      projectAgents={project?.agents ?? []}
      onModel={(next) => applyConfigUpdate({ sel: next })}
      onNotice={(text) => st.show(text)}
    />
  );
  const composerProps = {
    mode: clampedMode,
    maxCeiling,
    onMode: (rm: RuntimeMode) => applyConfigUpdate({ rm }),
    model: modelButtons,
  };
  const placeRow = !activeSessionId && (project?.repo || project?.workspace === Workspace.WORKTREE) && <ComposerPlace place={place} branches={branches} onPlace={setPlace} />;
  const ask = (
    <LaunchAsk
      machines={st.machines}
      projects={projectsHere}
      projectsStale={projectsOn(effectiveMachineId).stale}
      choice={choice}
      onMachine={pickMachine}
      onProject={(pid) => pickProject(machineId, pid)}
      after={busySend && <WorkLine text={t("work.starting")} />}
      onAddProject={() =>
        st.set({
          sheet: {
            kind: "addProject",
            machineId,
            then: (pid, wt) => {
              setProjectId(pid);
              setPlace({ worktree: wt, base: "", on: "", fromRemote: false });
            },
          },
        })
      }
    />
  );

  const session = useMemo(() => {
    if (!currentSession) return undefined;
    const s = clone(SessionSchema, currentSession);
    s.branch = currentSession.branch || currentSession.workspaceStrategy?.branch || "";
    return s;
  }, [currentSession]);
  const { card } = layoutOf(st);
  const info = card !== "hidden";

  const requestCard = activeSessionId && session && pendingReq && <SessionRequest key={pendingReq.requestId} s={session} req={pendingReq} />;
  const openBackgroundDetails = (taskId?: string) => {
    if (!info) setCard(st, true);
    if (taskId) setSelectedBgTaskId(taskId);
    else if (activeBgTasks[0]) setSelectedBgTaskId(activeBgTasks[0].taskId);
  };
  const backgroundCard = activeSessionId && activeBgTasks.length > 0 && (
    <BackgroundTasksStrip
      tasks={activeBgTasks}
      onView={() => openBackgroundDetails(activeBgTasks[0]?.taskId)}
      onStop={() => handleStopBackgroundTask(activeBgTasks.length === 1 ? activeBgTasks[0]!.taskId : "")}
    />
  );
  const queueCard = activeSessionId && (queuedRuns.length > 0 || currentSession?.queueHeld) && (
    <div className="knote" data-testid="queued-runs-strip">
      <div className="knote-h">
        <Icon name="clock" className="knote-i" />
        <b>{t("queue.heading", { count: queuedRuns.length })}</b>
        {currentSession?.queueHeld && (
          <>
            <span className="qx-tag">{t("queue.held")}</span>
            <span className="appr-sp" />
            <button className="btn th-qbtn" data-testid="queue-resume-btn" onClick={() => sessionApi.manageQueue({ sessionId: activeSessionId, operation: QueueOperation.RESUME_QUEUE })}>
              {t("queue.resume")}
            </button>
          </>
        )}
      </div>
      {queuedRuns.map((qr, idx) => (
        <div key={qr.runId} className="th-qrow" data-testid={`queued-run-${idx}`}>
          {editingRunId === qr.runId ? (
            <>
              <input className="field" value={editingText} onChange={(e) => setEditingText(e.target.value)} data-testid="queue-edit-input" />
              <button
                className="btn primary th-qbtn"
                data-testid="queue-edit-save"
                onClick={async () => {
                  await sessionApi.manageQueue({ sessionId: activeSessionId, operation: QueueOperation.EDIT, runId: qr.runId, text: editingText });
                  setEditingRunId("");
                }}
              >
                {t("queue.save")}
              </button>
            </>
          ) : (
            <>
              <span className="th-qtext sel-text" title={qr.prompt}>{qr.prompt}</span>
              <button className="btn th-qbtn" data-testid={`queue-promote-${idx}`} onClick={() => sessionApi.manageQueue({ sessionId: activeSessionId, operation: QueueOperation.PROMOTE_TO_STEER, runId: qr.runId })}>
                {t("queue.steerNow")}
              </button>
              {idx > 0 && (
                <button className="btn th-qbtn" onClick={() => sessionApi.manageQueue({ sessionId: activeSessionId, operation: QueueOperation.REORDER, runId: qr.runId, beforeRunId: queuedRuns[idx - 1]!.runId })}>
                  {t("queue.moveUp")}
                </button>
              )}
              <button
                className="btn th-qbtn"
                data-testid={`queue-edit-${idx}`}
                onClick={() => {
                  setEditingRunId(qr.runId);
                  setEditingText(qr.prompt);
                }}
              >
                {t("queue.edit")}
              </button>
              <button className="btn th-qbtn" data-testid={`queue-cancel-${idx}`} onClick={() => sessionApi.manageQueue({ sessionId: activeSessionId, operation: QueueOperation.CANCEL, runId: qr.runId })}>
                {t("queue.cancel")}
              </button>
            </>
          )}
        </div>
      ))}
    </div>
  );

  // The conversation, reduced from SessionProjection (core transcript.ts) and drawn with unified conversation rows.
  const steps = useMemo(() => groupSteps(sessionTranscript(activeProjection)), [activeProjection]);
  const ends = useMemo(() => replyEnds(steps, running), [steps, running]);
  const failure = sessionFailure(currentSession);
  const activeRun = activeProjection?.runs.find((r) => r.runId === currentSession?.activeRunId);
  const workLine =
    currentSession?.status === SessionStatus.PREPARING ? <WorkLine text={t("work.starting")} />
    : currentSession?.status === SessionStatus.QUEUED ? <WorkLine text={statusTitle(SessionStatus.QUEUED)} />
    : currentSession?.status === SessionStatus.RUNNING ? <WorkLine text={t(sessionToolRunning(activeProjection) ? "work.working" : "work.thinking")} since={dateOf(activeRun?.startedAt)} />
    : null;
  const writing = useMemo(() => sessionLiveReplies(activeProjection), [activeProjection]);
  const rows = useMemo(
    () => rowsOf(steps, ends, failure).map((r) => (writing.has(r.it.seq) ? { ...r, live: true } : r)),
    [steps, ends, failure?.message, writing],
  );
  const grant = session ? grantLabel(st, session) : "";
  const archived = currentSession?.status === SessionStatus.ARCHIVED;
  const unread = steps.length === 0 && (currentSession?.progress?.lastEventSeq ?? 0n) > 0n && !currentSession?.offlineQueued;
  const behind = (activeProjection?.session?.progress?.lastEventSeq ?? 0n) < (listedSession?.progress?.lastEventSeq ?? 0n);
  const interrupted = isInterruption(currentSession?.progress?.lastFailure) || rows.at(-1)?.it.kind === "turnEnd" || rows.at(-1)?.it.kind === "error";

  return (
    <>
      <div className="ctool drag">
        <div className="ctitle">
          <h2 data-testid="thread-title">{activeSessionId ? (currentSession ? displayTitle(currentSession) : "…") : t("launch.title")}</h2>
          {activeSessionId && currentSession && (
            <span className="th-where" data-testid="session-where">
              <Icon name={targetMachine?.local ? "laptop" : "server"} />
              {targetMachine?.machineName ?? currentSession.machineId} · {currentSession.projectId === SCRATCH_PROJECT ? t("launch.scratch") : currentSession.projectId}
            </span>
          )}
          {currentSession?.offlineQueued && activeSessionId && (
            <>
              <span className="th-badge" data-testid="offline-badge">
                {t("header.offlineQueued")}
              </span>
              <button
                className="btn th-qbtn"
                data-testid="cancel-offline-session-btn"
                onClick={() =>
                  void st.run(async () => {
                    await sessionApi.deleteSession({ sessionId: activeSessionId });
                    st.set({ selection: { kind: "overview" } });
                  })
                }
              >
                {t("request.cancel")}
              </button>
            </>
          )}
        </div>
        {activeSessionId && activeBgTasks.length > 0 && (
          <button
            type="button"
            className="th-bgbadge"
            data-testid="toolbar-background-badge"
            onClick={() => openBackgroundDetails(activeBgTasks[0]?.taskId)}
          >
            <span className="mini-spin" />
            <span>{t("background.badge", { count: activeBgTasks.length })}</span>
          </button>
        )}
        {activeSessionId && (
          <button
            className={`tb ${info ? "on" : ""}`}
            title={ts(info ? "page.hideInfo" : "page.showInfo", { keys: keys(ownKeys.toggleInspector) })}
            aria-label={ts("page.info")}
            aria-pressed={info}
            onClick={() => setCard(st, !info)}
          >
            <Icon name="infoCard" />
          </button>
        )}
      </div>
      {!activeSessionId ? (
        <div className="dwrap th" data-testid="thread-page">
          <div className="dmain" data-testid="thread-launch-card">
            {ask}
            <div className="cmp-zone">
              <div className="cmp-col">
                <ConnectionCapsule machine={targetMachine} linkUp={reachable} />
                {slashRow}
                {placeRow}
                <ThreadComposer
                  {...composerProps}
                  value={prompt}
                  onChange={(v) => {
                    setPrompt(v);
                    setSlashOpen(v.startsWith("/") && !v.includes(" "));
                  }}
                  onSend={() => submitMessage()}
                  running={false}
                  sending={busySend}
                  placeholder={t("launch.promptPlaceholder")}
                  attachments={attachments}
                  onAttach={handleAttachFiles}
                  onRemove={(sha) => setAttachments((prev) => prev.filter((a) => a.blobSha256 !== sha))}
                  offline={!machineOnline}
                />
              </div>
            </div>
          </div>
        </div>
      ) : (
        <div className={`dwrap th ${card === "docked" ? "dock" : ""}`} data-testid="thread-page">
          <div className="dmain">
            <ContentScope value={activeSessionId}>
              <ChatFrame
                key={activeSessionId}
                rows={rows}
                initial={rowHeights}
                label={ts("chat.log")}
                follow={currentSession?.status}
                unread={unread && session ? <Unread s={session} /> : undefined}
                row={(r) => <Row it={r.it} rowKey={r.key} end={r.end} live={r.live} sessionId={activeSessionId} clients={st.clients} starter={session?.createdBy ?? "owner"} previous={r.previous} />}
                head={
                  archived && session && (
                    <div className="anote">
                      <Icon name="archive" className="sm" />
                      {ts("chat.archived")}
                      <button className="link" onClick={() => unarchive(st, session)}>{ts("chat.unarchive")}</button>
                    </div>
                  )
                }
                foot={
                  session && (
                    <>
                      {workLine}
                      <Stale s={session} behind={behind} />
                      {failure && <Failed s={session} failure={failure} grant={grant} />}
                      {/* A turn that went well needs no line; one whose background tasks still run says it waits on them. */}
                      {currentSession?.status === SessionStatus.IDLE && !failure && !interrupted && rows.length > 0 &&
                        (currentSession.activeBackgroundCount > 0 || activeAwaitedBgTasks.length > 0) && (
                          <WorkLine
                            text={t("background.waitingDone", {
                              count: Math.max(currentSession.activeBackgroundCount, activeAwaitedBgTasks.length),
                            })}
                            since={dateOf(activeAwaitedBgTasks[0]?.startedAt)}
                          />
                        )}
                    </>
                  )
                }
                control={archived ? undefined : (onHeight) => (
                  <ComposerZone onHeight={onHeight}>
                    <ConnectionCapsule machine={targetMachine} linkUp={following && reachable} />
                    {requestCard}
                    {backgroundCard}
                    {queueCard}
                    {slashRow}
                    <ThreadComposer
                      {...composerProps}
                      value={prompt}
                      onChange={(v) => {
                        setPrompt(v);
                        setSlashOpen(v.startsWith("/") && !v.includes(" "));
                      }}
                      onSend={() => submitMessage()}
                      onStop={handleStop}
                      running={running}
                      sending={busySend}
                      placeholder={running ? t("composer.placeholderRunning") : t("composer.placeholderIdle")}
                      attachments={attachments}
                      onAttach={handleAttachFiles}
                      onRemove={(sha) => setAttachments((prev) => prev.filter((a) => a.blobSha256 !== sha))}
                      offline={!machineOnline}
                    />
                  </ComposerZone>
                )}
              />
            </ContentScope>
          </div>
          {info && session && (
            <Inspector
              s={session}
              providers={providers}
              backgroundTasks={allBgTasks}
              selectedTaskId={selectedBgTaskId}
              onSelectTask={setSelectedBgTaskId}
              onStopBackgroundTask={handleStopBackgroundTask}
            />
          )}
        </div>
      )}
    </>
  );
}

function BackgroundTasksStrip({
  tasks,
  onView,
  onStop,
}: {
  tasks: readonly BackgroundTask[];
  onView: () => void;
  onStop: () => void;
}) {
  const { t } = useTranslation("thread");
  const now = useNow(1000);
  const awaited = tasks.filter((x) => x.awaited);
  const primary = awaited[0] ?? tasks[0]!;
  const primaryLabel = primary.title || primary.command || primary.taskId;
  const since = dateOf(primary.startedAt);
  const summary =
    awaited.length === 1
      ? t("background.runningOne", { title: primaryLabel })
      : awaited.length > 1
        ? t("background.runningMany", { count: awaited.length })
        : tasks.length === 1
          ? t("background.daemonOne", { title: primaryLabel })
          : t("background.daemonMany", { count: tasks.length });

  return (
    <div className="knote th-bgstrip" data-testid="background-tasks-strip">
      <div className="knote-h">
        {awaited.length > 0 ? <Mark size={15} variant="seq" /> : <Icon name="bolt" className="knote-i" />}
        <span className="th-qtext" title={primary.command || primaryLabel}>{summary}</span>
        {since && <span className="th-bgdur">{clockText(elapsedSeconds(since, now))}</span>}
        <button type="button" className="btn th-qbtn" data-testid="background-strip-view" onClick={onView}>
          {t("background.view")}
        </button>
        <button type="button" className="btn th-qbtn" data-testid="background-strip-stop" onClick={onStop}>
          {tasks.length > 1 ? t("background.stopAll") : t("background.stop")}
        </button>
      </div>
    </div>
  );
}

/** A session at work, in a line at the end of its conversation (as the iPhone's): the mark, its beams lighting up in turn, what it does and, while the agent works, for how long. */
function WorkLine({ text, since }: { text: string; since?: Date }) {
  const now = useNow(1000);
  return (
    <div className="th-work" data-testid="thread-working">
      <Mark size={16} variant="seq" />
      <span>{text}</span>
      {since && <span className="el">{clockText(elapsedSeconds(since, now))}</span>}
    </div>
  );
}

/** The box at the bottom of the conversation, as Her's (her.css .cmp-zone), telling the conversation how much of it it covers. */
function ComposerZone({ onHeight, children }: { onHeight: (h: number) => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => onHeight(el.offsetHeight));
    ro.observe(el);
    return () => {
      ro.disconnect();
      onHeight(0);
    };
  }, [onHeight]);
  return (
    <div className="cmp-zone" ref={ref}>
      <div className="cmp-col">{children}</div>
    </div>
  );
}
