import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { create } from "@bufbuild/protobuf";
import { RuntimeMode, Workspace } from "@hermesh/proto/identity";
import type { Project } from "@hermesh/proto/local";
import { SessionStatus } from "@hermesh/proto/session";
import {
  DispatchIntent,
  InteractionMode,
  ModelSelectionSchema,
  QueueOperation,
  ThreadStatus,
  WorkspaceStrategySchema,
  type AttachmentRef,
  type ModelSelection,
  type ProviderInstance,
  type ThreadProjection,
} from "@hermesh/proto/thread";
import {
  SCRATCH_PROJECT,
  applyThreadEvent,
  clampRuntimeMode,
  clockText,
  cycleReasoningSelection,
  dateOf,
  defaultModelSelectionFor,
  elapsedSeconds,
  findProviderInstance,
  groupSteps,
  isThreadActive,
  pendingRequestOf,
  projectOnMachine,
  providerUnusable,
  queuedRunsOf,
  reasoningOf,
  replyEnds,
  selectionTitle,
  statusTitle,
  threadAsSession,
  threadFailure,
  threadLiveReplies,
  threadToolRunning,
  threadTranscript,
} from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local, threads as threadApi } from "../api";
import { ConnectionCapsule } from "../ConnectionCapsule";
import { Icon } from "../icons";
import { layoutOf, setCard } from "../layout";
import { keys, ownKeys } from "../platform";
import { grantLabel, unarchive } from "../Sidebar";
import { errorText, useNow, useStore } from "../store";
import { ChatFrame, DoneLine, Failed, Row, Stale, rowHeights, rowsOf } from "../session/Chat";
import { Inspector } from "../session/Inspector";
import { ContentScope } from "../session/scope";
import "../session/markdown/markdown.css";
import "../session/session.css";
import { ComposerPlace, LaunchAsk, ModelButtons, ThreadComposer, type Branches, type Place } from "./Composer";
import { ThreadRequest } from "./Request";
import { keepProjects, keepProviders, lastProjects, lastProviders, remembered, remember } from "./choices";
import "./thread.css";

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

// A session started here (a v2 thread): the conversation, its info cards and the
// box under it, as a session's page (SessionPage.tsx) has them; a new one, the box
// in the middle of the page.
export function ThreadPage() {
  const { t } = useTranslation("thread");
  const ts = useTranslation("session").t;
  const st = useStore();
  const sel = st.selection;
  const activeThreadId = sel.kind === "thread" ? sel.id : undefined;

  const [projection, setProjection] = useState<ThreadProjection | null>(null);
  /** The session's events are followed: its subscription has been open a moment (the effect that follows it). */
  const [following, setFollowing] = useState(false);
  const [providers, setProviders] = useState<ProviderInstance[]>([]);
  /** The providers shown are those last read on the machine, which is offline (choices.ts). */
  const [providersStale, setProvidersStale] = useState(false);
  const [maxCeiling, setMaxCeiling] = useState<RuntimeMode>(RuntimeMode.FULL_ACCESS);

  // Each machine's projects as last read, for when it is offline (choices.ts); a machine that
  // answers has its own, live.
  useEffect(() => {
    // A machine going offline may lose its projects from the list a moment before it shows offline: an
    // empty list never replaces what was kept.
    const answered = st.machines.filter((m) => (m.online || m.local) && st.projects.some((p) => p.machineId === m.machineId));
    keepProjects(st.projects, answered.map((m) => m.machineId));
  }, [st.projects, st.machines]);
  const projectsOn = (mid: string): { list: Project[]; stale: boolean } => {
    const live = st.projects.filter((p) => p.machineId === mid);
    const m = st.machines.find((x) => x.machineId === mid);
    if (!m || m.online || m.local) return { list: live, stale: false };
    // Offline: what the window still holds of it, else what this device kept; either is last known.
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

  // Each time a new session opens (and once the machines are known): the machine and project its entry
  // named, else those used last; a machine that is gone falls back to this Mac.
  const askedMachine = sel.kind === "newThread" ? sel.machineId : undefined;
  const askedProject = sel.kind === "newThread" ? sel.projectId : undefined;
  const machinesKnown = st.machines.length > 0;
  useEffect(() => {
    if (sel.kind !== "newThread" || !machinesKnown) return;
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

  const effectiveMachineId = activeThreadId ? projection?.thread?.machineId ?? machineId : machineId;
  const effectiveProjectId = activeThreadId ? projection?.thread?.projectId ?? projectId : projectId;
  const targetMachine = st.machines.find((m) => m.machineId === effectiveMachineId);
  const machineOnline = targetMachine ? targetMachine.online || targetMachine.local : true;
  // A machine from before threads (its status says so) can't take a new session: said, not hidden (§5.2).
  const needsUpdate = !activeThreadId && !!targetMachine?.status && !targetMachine.status.threads;

  // A new session's project's branches, read from its folder on the machine (only the owner's devices can): read
  // again as the page opens or the project changes, since a branch switched on the machine isn't sent here.
  const projectPath = !activeThreadId ? projectsOn(effectiveMachineId).list.find((p) => p.projectId === effectiveProjectId && p.repo)?.path : undefined;
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

  // The model a new session takes on a machine: the one used there last, while it is still offered.

  // Load providers for target machine & project
  useEffect(() => {
    let cancelled = false;
    threadApi
      .listProviders({ machineId: effectiveMachineId, projectId: effectiveProjectId === "scratch" ? "" : effectiveProjectId })
      .then((res) => {
        if (cancelled) return;
        keepProviders(effectiveMachineId, res);
        setProvidersStale(false);
        return res;
      })
      .catch(() => {
        // The machine doesn't answer: what was last read there, if anything.
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
        if (activeThreadId) return; // a session keeps its own (from GetThread)
        const usable = res.providers.filter((p) => providerUnusable(p, []) === "");
        const last = remembered().byMachine[effectiveMachineId]?.sel;
        const lastInst = last && usable.find((p) => p.instanceId === last.instanceId);
        setModelSel((prev) => {
          if (lastInst && lastInst.models.some((m) => m.slug === last.model)) return create(ModelSelectionSchema, last);
          if (prev.instanceId && usable.some((p) => p.instanceId === prev.instanceId)) return prev;
          // The coding agents most people use first, then any other that is ready (never a test-only one first).
          const first = ["claude", "codex", "gemini"].map((id) => usable.find((p) => p.instanceId === id)).find(Boolean) ?? usable[0] ?? findProviderInstance(res.providers);
          return first ? defaultModelSelectionFor(first) : prev;
        });
      });
    return () => {
      cancelled = true;
    };
  }, [effectiveMachineId, effectiveProjectId, activeThreadId]);

  // The page's machine can be reached now: this Mac reaches the relay, and the machine is online and answers.
  const reachable = !targetMachine || targetMachine.local || (st.status?.relayConnected === true && targetMachine.online && !targetMachine.unreachable);

  // The session's projection, then its events as they come; read and subscribed again after the subscription ended or
  // failed (in 1 s, then up to every 30 s), and at once when its machine can be reached again.
  useEffect(() => {
    if (!activeThreadId) {
      setProjection(null);
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
          const init = await threadApi.getThread({ threadId: activeThreadId }, { signal: ctl.signal });
          if (init.projection) {
            setProjection(init.projection);
            // Its own model and modes, once: later, the owner's own choices stay.
            const th = init.projection.thread;
            if (!applied && th) {
              applied = true;
              if (th.modelSelection) setModelSel(th.modelSelection);
              if (th.runtimeMode) setRuntimeMode(th.runtimeMode);
              if (th.interactionMode) setInteractionMode(th.interactionMode);
              if (th.maxRuntimeMode) setMaxCeiling(th.maxRuntimeMode);
            }
          }
          // Open a moment without failing: followed (a subscription that can't reach the machine fails at once).
          opened = setTimeout(() => {
            open = true;
            setFollowing(true);
          }, 1000);
          for await (const ev of threadApi.subscribeThread({ threadId: activeThreadId, afterSeq: init.projection?.thread?.lastSeq ?? 0n }, { signal: ctl.signal })) {
            setProjection((prev) => applyThreadEvent(prev, ev));
            if (ev.thread?.interactionMode) setInteractionMode(ev.thread.interactionMode);
          }
        } catch {
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
  }, [activeThreadId, reachable]);

  const currentThread = projection?.thread;
  const activeInst = findProviderInstance(providers, modelSel.instanceId);
  const clampedMode = clampRuntimeMode(runtimeMode, maxCeiling);
  const queuedRuns = useMemo(() => queuedRunsOf(projection), [projection]);
  const pendingReq = useMemo(() => pendingRequestOf(projection), [projection]);
  const running = isThreadActive(currentThread);

  // Shift+Cmd/Ctrl+E cycles how hard it thinks.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "e") {
        e.preventDefault();
        setModelSel((prev) => {
          const next = cycleReasoningSelection(providers, prev);
          if (activeThreadId) {
            threadApi.updateThreadConfig({ threadId: activeThreadId, modelSelection: next }).catch(() => {});
          }
          return next;
        });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [providers, activeThreadId]);

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
    if (activeThreadId) {
      try {
        const res = await threadApi.updateThreadConfig({
          threadId: activeThreadId,
          modelSelection: nextSel,
          runtimeMode: nextRm,
          interactionMode: nextIm,
        });
        if (res.thread) {
          setProjection((p) => (p ? { ...p, thread: res.thread } : p));
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
    if (activeThreadId) {
      const sent = attachments;
      setPrompt("");
      setAttachments([]);
      // After whatever runs, and at once when nothing does: hermeshd decides, not this window's
      // view of the thread, which may be a moment behind (steering is the queue's "steer now").
      const req = {
        threadId: activeThreadId,
        text,
        attachments: sent,
        intent: explicitIntent ?? DispatchIntent.QUEUE,
        modelSelection: modelSel.instanceId ? modelSel : undefined,
        runtimeMode: clampedMode,
        interactionMode,
      };
      const job = sends.current.then(() => threadApi.dispatchMessage(req));
      sends.current = job.catch(() => {});
      try {
        await job;
      } catch (e) {
        st.show(errorText(e));
        // Not sent: back in the box, unless something else was written there since.
        setPrompt((p) => p || text);
        setAttachments((a) => (a.length ? a : sent));
      }
      return;
    }
    setBusySend(true);
    try {
      const res = await threadApi.launchThread({
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
      if (res.thread) {
        st.set({ selection: { kind: "thread", id: res.thread.threadId } });
      }
    } catch (e) {
      st.show(errorText(e));
    } finally {
      setBusySend(false);
    }
  };

  const handleStop = async () => {
    if (!activeThreadId || !currentThread) return;
    const force = stopRequestedRun === currentThread.activeRunId && currentThread.activeRunId !== "";
    setStopRequestedRun(currentThread.activeRunId);
    try {
      await threadApi.interruptRun({ threadId: activeThreadId, force });
    } catch (e) {
      st.show(errorText(e));
    }
  };

  const handleAttachFiles = async (files: FileList | null) => {
    if (!files?.length) return;
    for (const f of Array.from(files)) {
      try {
        const buf = new Uint8Array(await f.arrayBuffer());
        const up = await threadApi.uploadBlob({
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
  const placeRow = !activeThreadId && (project?.repo || project?.workspace === Workspace.WORKTREE) && <ComposerPlace place={place} branches={branches} onPlace={setPlace} />;
  // A new session's question in the middle of the page: the project, and the environment under it.
  const ask = (
    <LaunchAsk
      machines={st.machines}
      projects={projectsHere}
      projectsStale={projectsOn(effectiveMachineId).stale}
      choice={choice}
      onMachine={pickMachine}
      onProject={(pid) => pickProject(machineId, pid)}
      // Sent, until the session opens.
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

  // The thread as a session (as the list has it), with the model and how hard it thinks as the box has them, for the info cards.
  const session = useMemo(() => {
    if (!currentThread) return undefined;
    const s = threadAsSession(currentThread);
    s.model = selectionTitle(providers, modelSel) || modelSel.model;
    s.effort = reasoningOf(providers, modelSel)?.value ?? "";
    s.branch = currentThread.workspace?.branch ?? "";
    return s;
  }, [currentThread, providers, modelSel]);
  const { card } = layoutOf(st);
  const info = card !== "hidden";

  const requestCard = activeThreadId && session && pendingReq && <ThreadRequest key={pendingReq.requestId} s={session} req={pendingReq} />;
  const queueCard = activeThreadId && (queuedRuns.length > 0 || currentThread?.queueHeld) && (
    <div className="knote" data-testid="queued-runs-strip">
      <div className="knote-h">
        <Icon name="clock" className="knote-i" />
        <b>{t("queue.heading", { count: queuedRuns.length })}</b>
        {currentThread?.queueHeld && (
          <>
            <span className="qx-tag">{t("queue.held")}</span>
            <span className="appr-sp" />
            <button className="btn th-qbtn" data-testid="queue-resume-btn" onClick={() => threadApi.manageQueue({ threadId: activeThreadId, operation: QueueOperation.RESUME_QUEUE })}>
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
                  await threadApi.manageQueue({ threadId: activeThreadId, operation: QueueOperation.EDIT, runId: qr.runId, text: editingText });
                  setEditingRunId("");
                }}
              >
                {t("queue.save")}
              </button>
            </>
          ) : (
            <>
              <span className="th-qtext sel-text" title={qr.prompt}>{qr.prompt}</span>
              <button className="btn th-qbtn" data-testid={`queue-promote-${idx}`} onClick={() => threadApi.manageQueue({ threadId: activeThreadId, operation: QueueOperation.PROMOTE_TO_STEER, runId: qr.runId })}>
                {t("queue.steerNow")}
              </button>
              {idx > 0 && (
                <button className="btn th-qbtn" onClick={() => threadApi.manageQueue({ threadId: activeThreadId, operation: QueueOperation.REORDER, runId: qr.runId, beforeRunId: queuedRuns[idx - 1]!.runId })}>
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
              <button className="btn th-qbtn" data-testid={`queue-cancel-${idx}`} onClick={() => threadApi.manageQueue({ threadId: activeThreadId, operation: QueueOperation.CANCEL, runId: qr.runId })}>
                {t("queue.cancel")}
              </button>
            </>
          )}
        </div>
      ))}
    </div>
  );

  // The conversation, made as a session's (core threadtranscript.ts) and drawn with a session's rows.
  const steps = useMemo(() => groupSteps(threadTranscript(projection)), [projection]);
  const ends = useMemo(() => replyEnds(steps, running), [steps, running]);
  const failure = threadFailure(currentThread);
  // At the end of the conversation while the session is at work, whatever its last row (the agent may be thinking,
  // which has no row): starting, waiting its turn, or, with how long, working (a tool or command of this turn runs) or
  // thinking (anything else). Waiting on the owner, the request card says so instead.
  const activeRun = projection?.runs.find((r) => r.runId === currentThread?.activeRunId);
  const workLine =
    currentThread?.status === ThreadStatus.PREPARING ? <WorkLine text={t("work.starting")} />
    : currentThread?.status === ThreadStatus.QUEUED ? <WorkLine text={statusTitle(SessionStatus.QUEUED)} />
    : currentThread?.status === ThreadStatus.RUNNING ? <WorkLine text={t(threadToolRunning(projection) ? "work.working" : "work.thinking")} since={dateOf(activeRun?.startedAt)} />
    : null;
  const writing = useMemo(() => threadLiveReplies(projection), [projection]);
  const rows = useMemo(
    () => rowsOf(steps, ends, failure).map((r) => (writing.has(r.it.seq) ? { ...r, live: true } : r)),
    [steps, ends, failure?.message, writing],
  );
  const grant = session ? grantLabel(st, session) : "";
  const archived = currentThread?.status === ThreadStatus.ARCHIVED;

  return (
    <>
      <div className="ctool drag">
        <div className="ctitle">
          <h2 data-testid="thread-title">{activeThreadId ? currentThread?.title || "…" : t("launch.title")}</h2>
          {/* Where it runs, fixed: the line under the box had it (§16). */}
          {activeThreadId && currentThread && (
            <span className="th-where" data-testid="thread-where">
              <Icon name={targetMachine?.local ? "laptop" : "server"} />
              {targetMachine?.machineName ?? currentThread.machineId} · {currentThread.projectId === SCRATCH_PROJECT ? t("launch.scratch") : currentThread.projectId}
            </span>
          )}
          {currentThread?.offlineQueued && (
            <span className="th-badge" data-testid="offline-badge">
              {t("header.offlineQueued")}
            </span>
          )}
        </div>
        {activeThreadId && (
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
      {!activeThreadId ? (
        <div className="dwrap th" data-testid="thread-page">
          {/* The question in the middle, the box at the bottom where a session's is. */}
          <div className="dmain" data-testid="thread-launch-card">
            {ask}
            <div className="cmp-zone">
              <div className="cmp-col">
                <ConnectionCapsule machine={targetMachine} linkUp={reachable} />
                {needsUpdate && (
                  <p className="th-note" role="status" data-testid="launch-needs-update">
                    {t("launch.needsUpdate", { machine: targetMachine?.machineName ?? "" })}
                  </p>
                )}
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
                  blocked={needsUpdate}
                />
              </div>
            </div>
          </div>
        </div>
      ) : (
        <div className={`dwrap th ${card === "docked" ? "dock" : ""}`} data-testid="thread-page">
          <div className="dmain">
            <ContentScope value={activeThreadId}>
              <ChatFrame
                key={activeThreadId}
                rows={rows}
                initial={rowHeights}
                label={ts("chat.log")}
                follow={currentThread?.status}
                row={(r) => <Row it={r.it} rowKey={r.key} end={r.end} live={r.live} sessionId={activeThreadId} clients={st.clients} grant={grant} previous={r.previous} />}
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
                      <Stale s={session} behind={false} />
                      {failure && <Failed s={session} failure={failure} grant={grant} />}
                      {currentThread?.status === ThreadStatus.IDLE && !failure && rows.length > 0 && rows.at(-1)?.it.kind !== "turnEnd" && <DoneLine text={ts("chat.turnDone", { grant })} />}
                    </>
                  )
                }
                // Archived, it is read-only, as a session is: no box.
                control={archived ? undefined : (onHeight) => (
                  <ComposerZone onHeight={onHeight}>
                    <ConnectionCapsule machine={targetMachine} linkUp={following && reachable} />
                    {requestCard}
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
          {info && session && <Inspector s={session} />}
        </div>
      )}
    </>
  );
}

/** A session at work, in a line at the end of its conversation (as the iPhone's): the mark, its beams lighting up in turn, what it does and, while the agent works, for how long. */
function WorkLine({ text, since }: { text: string; since?: Date }) {
  const now = useNow(1000);
  return (
    <div className="th-work" role="status" data-testid="thread-working">
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
