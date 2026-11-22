import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { create } from "@bufbuild/protobuf";
import { RuntimeMode, Workspace } from "@hermesh/proto/identity";
import type { Project } from "@hermesh/proto/local";
import {
  ApprovalDecision,
  DispatchIntent,
  InteractionMode,
  ModelSelectionSchema,
  QueueOperation,
  QuestionAnswerSchema,
  RuntimeRequestKind,
  ThreadStatus,
  TurnItemKind,
  WorkspaceStrategySchema,
  type AttachmentRef,
  type Checkpoint,
  type CheckpointFile,
  type ModelSelection,
  type ProviderInstance,
  type RuntimeRequest,
  type ThreadProjection,
} from "@hermesh/proto/thread";
import {
  SCRATCH_PROJECT,
  applyThreadEvent,
  clampRuntimeMode,
  cycleReasoningSelection,
  defaultModelSelectionFor,
  findProviderInstance,
  isThreadActive,
  pendingRequestOf,
  projectOnMachine,
  providerUnusable,
  queuedRunsOf,
  toggleInteractionMode,
} from "@hermesh/core";
import { threads as threadApi } from "../api";
import { Icon } from "../icons";
import { errorText, useStore } from "../store";
import { ComposerOptions, ThreadComposer } from "./Composer";
import { keepProjects, keepProviders, lastProjects, lastProviders, remembered, remember } from "./choices";
import "./thread.css";

export function ThreadPage() {
  const { t } = useTranslation("thread");
  const st = useStore();
  const sel = st.selection;
  const activeThreadId = sel.kind === "thread" ? sel.id : undefined;

  const [projection, setProjection] = useState<ThreadProjection | null>(null);
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
  const [worktree, setWorktree] = useState<boolean>(true);

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

  // Inspector controls
  const [inspectorOpen, setInspectorOpen] = useState<boolean>(true);
  const [inspTab, setInspTab] = useState<"diff" | "checkpoints" | "info">("diff");
  const [diffText, setDiffText] = useState<string>("");
  const [diffFiles, setDiffFiles] = useState<CheckpointFile[]>([]);
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([]);

  //Queue edit state
  const [editingRunId, setEditingRunId] = useState<string>("");
  const [editingText, setEditingText] = useState<string>("");

  // Request response local state
  const [declineFeedback, setDeclineFeedback] = useState<string>("");
  const [secretInput, setSecretInput] = useState<string>("");
  const [questionChoices, setQuestionChoices] = useState<Record<string, string[]>>({});
  const [questionCustom, setQuestionCustom] = useState<Record<string, string>>({});


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
    setWorktree(pr?.workspace === Workspace.WORKTREE);
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

  // Load & subscribe to active thread projection + live events!
  const refreshInspector = useCallback(
    (tid: string) => {
      threadApi
        .getThreadDiff({ threadId: tid })
        .then((r) => {
          setDiffText(r.unifiedDiff);
          setDiffFiles(r.files);
        })
        .catch(() => {});
      threadApi
        .listCheckpoints({ threadId: tid })
        .then((r) => setCheckpoints(r.checkpoints))
        .catch(() => {});
    },
    [],
  );

  useEffect(() => {
    if (!activeThreadId) {
      setProjection(null);
      setDiffText("");
      setDiffFiles([]);
      setCheckpoints([]);
      return;
    }
    const ctl = new AbortController();
    let afterSeq = 0n;
    (async () => {
      try {
        const init = await threadApi.getThread({ threadId: activeThreadId }, { signal: ctl.signal });
        if (init.projection) {
          setProjection(init.projection);
          afterSeq = init.projection.thread?.lastSeq ?? 0n;
          if (init.projection.thread?.modelSelection) {
            setModelSel(init.projection.thread.modelSelection);
          }
          if (init.projection.thread?.runtimeMode) {
            setRuntimeMode(init.projection.thread.runtimeMode);
          }
          if (init.projection.thread?.interactionMode) {
            setInteractionMode(init.projection.thread.interactionMode);
          }
          if (init.projection.thread?.maxRuntimeMode) {
            setMaxCeiling(init.projection.thread.maxRuntimeMode);
          }
          setCheckpoints(init.projection.checkpoints);
        }
        refreshInspector(activeThreadId);
        for await (const ev of threadApi.subscribeThread({ threadId: activeThreadId, afterSeq }, { signal: ctl.signal })) {
          setProjection((prev) => applyThreadEvent(prev, ev));
          if (ev.thread?.interactionMode) setInteractionMode(ev.thread.interactionMode);
          if (ev.type === "run.completed" || ev.type === "thread.rolled_back") {
            refreshInspector(activeThreadId);
          }
        }
      } catch {
        // Stream aborted or offline
      }
    })();
    return () => ctl.abort();
  }, [activeThreadId, refreshInspector]);

  const currentThread = projection?.thread;
  const activeInst = findProviderInstance(providers, modelSel.instanceId);
  const clampedMode = clampRuntimeMode(runtimeMode, maxCeiling);
  const queuedRuns = useMemo(() => queuedRunsOf(projection), [projection]);
  const pendingReq = useMemo(() => pendingRequestOf(projection), [projection]);
  const running = isThreadActive(currentThread);

  // Keyboard shortcuts: Shift+Tab toggles Plan/Code, Shift+Cmd/Ctrl+E cycles reasoning!
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Tab" && e.shiftKey) {
        e.preventDefault();
        const next = toggleInteractionMode(interactionMode);
        setInteractionMode(next);
        if (activeThreadId) {
          threadApi.updateThreadConfig({ threadId: activeThreadId, interactionMode: next }).catch(() => {});
        }
      } else if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "e") {
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
  }, [interactionMode, providers, activeThreadId]);

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
        workspace: create(WorkspaceStrategySchema, {
          mode: projectId !== SCRATCH_PROJECT && worktree ? Workspace.WORKTREE : Workspace.IN_PLACE,
        }),
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
          className="th-pill-btn"
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

  // What runs where and with what, under the box (Composer.tsx): a new session's to choose; a session's
  // machine and project fixed, its model and mode for the next turn.
  const options = (fixed: boolean) => (
    <ComposerOptions
      fixed={fixed}
      below={!fixed}
      machines={st.machines}
      projects={[...st.projects.filter((p) => p.machineId !== effectiveMachineId), ...projectsOn(effectiveMachineId).list]}
      projectsStale={projectsOn(effectiveMachineId).stale}
      providersStale={providersStale}
      choice={{ machineId: effectiveMachineId, projectId: effectiveProjectId, worktree, sel: modelSel, mode: clampedMode, plan: interactionMode === InteractionMode.PLAN }}
      providers={providers}
      maxCeiling={maxCeiling}
      onMachine={pickMachine}
      onProject={(pid) => pickProject(machineId, pid)}
      onWorktree={setWorktree}
      onModel={(next) => applyConfigUpdate({ sel: next })}
      onMode={(rm) => applyConfigUpdate({ rm })}
      onPlan={(on) => applyConfigUpdate({ im: on ? InteractionMode.PLAN : InteractionMode.CODE })}
      onAddProject={() =>
        st.set({
          sheet: {
            kind: "addProject",
            machineId,
            then: (pid, wt) => {
              setProjectId(pid);
              setWorktree(wt);
            },
          },
        })
      }
      onNotice={(text) => st.show(text)}
    />
  );

  const resolveRequest = async (req: RuntimeRequest, opts: {
    decision?: ApprovalDecision;
    feedback?: string;
    approvePlan?: boolean;
    secret?: string;
    answers?: Record<string, { selected: string[]; custom?: string }>;
  }) => {
    if (!activeThreadId) return;
    try {
      const protoAnswers: Record<string, ReturnType<typeof create<typeof QuestionAnswerSchema>>> = {};
      if (opts.answers) {
        for (const [qid, val] of Object.entries(opts.answers)) {
          protoAnswers[qid] = create(QuestionAnswerSchema, {
            selectedOptions: val.selected,
            customText: val.custom ?? "",
          });
        }
      }
      await threadApi.respondRuntimeRequest({
        threadId: activeThreadId,
        requestId: req.requestId,
        decision: opts.decision ?? ApprovalDecision.ACCEPT_ONCE,
        feedback: opts.feedback ?? "",
        approvePlan: opts.approvePlan ?? false,
        secretValue: opts.secret ?? "",
        answers: protoAnswers,
      });
      setDeclineFeedback("");
      setSecretInput("");
      setQuestionChoices({});
      setQuestionCustom({});
    } catch (e) {
      st.show(errorText(e));
    }
  };

  return (
    <div className="th-shell" data-testid="thread-page">
      {/* Top Toolbar */}
      <div className="ctool drag">
        <div className="ctitle">
          <h2 data-testid="thread-title">
            {activeThreadId ? currentThread?.title || "..." : t("launch.title")}
          </h2>
          {currentThread?.offlineQueued && (
            <span className="th-badge" data-testid="offline-badge">
              {t("header.offlineQueued")}
            </span>
          )}
        </div>
        {activeThreadId && currentThread && (
          <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
            <button
              className="tb"
              title={t("header.fork")}
              data-testid="thread-fork-btn"
              onClick={async () => {
                try {
                  const r = await threadApi.forkThread({ sourceThreadId: activeThreadId });
                  if (r.thread) st.set({ selection: { kind: "thread", id: r.thread.threadId } });
                } catch (e) {
                  st.show(errorText(e));
                }
              }}
            >
              <Icon name="plus" />
            </button>
            <button
              className={`tb ${inspectorOpen ? "on" : ""}`}
              title={t("header.inspector")}
              aria-label={t("header.inspector")}
              data-testid="thread-inspector-toggle"
              onClick={() => setInspectorOpen((v) => !v)}
            >
              <Icon name="infoCard" />
            </button>
          </div>
        )}
      </div>

      <div className="th-body">
        <div className="th-main">
          {!activeThreadId && (
            <div className="th-new" data-testid="thread-launch-card">
              <h1>{t("launch.title")}</h1>
              <ThreadComposer
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
              {options(false)}
              {needsUpdate && (
                <p className="th-note" role="status" data-testid="launch-needs-update">
                  {t("launch.needsUpdate", { machine: targetMachine?.machineName ?? "" })}
                </p>
              )}
              {slashRow}
            </div>
          )}

          {/* Conversation & Step Timeline */}
          <div className="th-timeline" data-testid="thread-timeline">
            {(projection?.items ?? []).map((it) => {
              switch (it.kind) {
                case TurnItemKind.USER_MESSAGE:
                  return (
                    <div key={it.itemId} className="th-item th-item-user" data-testid="turn-item-user">
                      {it.status === "steered" && (
                        <div style={{ fontSize: 11, opacity: 0.75, marginBottom: 4 }}>{t("timeline.steered")}</div>
                      )}
                      {it.text}
                    </div>
                  );

                case TurnItemKind.ASSISTANT_MESSAGE:
                  return (
                    <div key={it.itemId} className="th-item th-item-assistant" data-testid="turn-item-assistant">
                      {it.text}
                    </div>
                  );

                case TurnItemKind.REASONING:
                  return (
                    <details key={it.itemId} className="th-item th-item-step">
                      <summary style={{ cursor: "pointer" }}>{t("timeline.reasoning")}</summary>
                      <div style={{ marginTop: 6, opacity: 0.85 }}>{it.text}</div>
                    </details>
                  );

                case TurnItemKind.COMMAND_EXECUTION:
                case TurnItemKind.TOOL_CALL:
                  return (
                    <details key={it.itemId} className="th-item th-item-step" data-testid="turn-item-tool">
                      <summary style={{ cursor: "pointer" }}>
                        [{it.status}] {it.toolName} {it.command ? `— ${it.command}` : ""}
                      </summary>
                      {it.output && <pre className="th-diff-pre" style={{ marginTop: 6 }}>{it.output}</pre>}
                    </details>
                  );

                case TurnItemKind.FILE_CHANGE:
                  return (
                    <div key={it.itemId} className="th-item th-item-step" data-testid="turn-item-file">
                      ✎ {it.filePath} (+{it.additions} −{it.deletions})
                    </div>
                  );

                case TurnItemKind.PROPOSED_PLAN:
                  return (
                    <div key={it.itemId} className="th-item th-item-plan" data-testid="turn-item-plan">
                      <strong>{t("request.planTitle", { title: it.planTitle || "Plan" })}</strong>
                      <div style={{ marginTop: 8 }}>{it.text}</div>
                    </div>
                  );

                case TurnItemKind.HANDOFF:
                  return (
                    <div key={it.itemId} className="th-divider" data-testid="turn-item-handoff">
                      <span>{t("timeline.handoff", { strategy: it.handoffStrategy })}</span>
                    </div>
                  );

                case TurnItemKind.MODEL_SWITCH:
                  return (
                    <div key={it.itemId} className="th-divider">
                      <span>{t("timeline.modelSwitch", { model: it.toSelection?.model ?? "" })}</span>
                    </div>
                  );

                case TurnItemKind.CHECKPOINT: {
                  const cp = checkpoints.find((c) => c.checkpointId === it.checkpointId);
                  if (!cp) return null;
                  return (
                    <div key={it.itemId} className="th-divider" data-testid={`checkpoint-divider-${cp.runOrdinal}`}>
                      <span>{t("timeline.checkpoint", { ordinal: cp.runOrdinal })}</span>
                      {currentThread?.status === ThreadStatus.IDLE && (
                        <button
                          className="link"
                          data-testid={`rewind-files-btn-${cp.runOrdinal}`}
                          onClick={async () => {
                            try {
                              await threadApi.rollbackCheckpoint({
                                threadId: activeThreadId!,
                                checkpointId: cp.checkpointId,
                                restoreFiles: true,
                              });
                              refreshInspector(activeThreadId!);
                            } catch (e) {
                              st.show(errorText(e));
                            }
                          }}
                        >
                          {t("timeline.rewind")}
                        </button>
                      )}
                    </div>
                  );
                }

                case TurnItemKind.ERROR:
                case TurnItemKind.SYSTEM_NOTICE:
                  return (
                    <div key={it.itemId} className="th-divider">
                      <span>{it.text}</span>
                    </div>
                  );

                default:
                  return null;
              }
            })}
            {/* Why it stopped: a launch or message the machine rejected (docs/thread-composer.md §5.3), unless the timeline says it already. */}
            {currentThread?.lastError && !(projection?.items ?? []).some((it) => it.text === currentThread.lastError) && (
              <div className="th-divider" data-testid="thread-last-error">
                <span>{currentThread.lastError}</span>
              </div>
            )}
          </div>

          {/* Polymorphic Composer + Queue Strip + Pending Request Banners */}
          <div className="th-composer-wrap">
            {/* Queued Follow-ups Strip */}
            {(queuedRuns.length > 0 || currentThread?.queueHeld) && (
              <div className="th-queue-strip" data-testid="queued-runs-strip">
                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                  <strong>{t("queue.heading", { count: queuedRuns.length })}</strong>
                  {currentThread?.queueHeld && (
                    <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                      <span className="th-badge">{t("queue.held")}</span>
                      <button
                        className="btn sm"
                        data-testid="queue-resume-btn"
                        onClick={() =>
                          threadApi.manageQueue({
                            threadId: activeThreadId!,
                            operation: QueueOperation.RESUME_QUEUE,
                          })
                        }
                      >
                        {t("queue.resume")}
                      </button>
                    </div>
                  )}
                </div>
                {queuedRuns.map((qr, idx) => (
                  <div key={qr.runId} className="th-queue-row" data-testid={`queued-run-${idx}`}>
                    {editingRunId === qr.runId ? (
                      <div style={{ display: "flex", gap: 6, flex: 1 }}>
                        <input
                          value={editingText}
                          onChange={(e) => setEditingText(e.target.value)}
                          style={{ flex: 1 }}
                          data-testid="queue-edit-input"
                        />
                        <button
                          className="btn sm"
                          data-testid="queue-edit-save"
                          onClick={async () => {
                            await threadApi.manageQueue({
                              threadId: activeThreadId!,
                              operation: QueueOperation.EDIT,
                              runId: qr.runId,
                              text: editingText,
                            });
                            setEditingRunId("");
                          }}
                        >
                          {t("queue.save")}
                        </button>
                      </div>
                    ) : (
                      <>
                        <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                          {qr.prompt}
                        </span>
                        <div style={{ display: "flex", gap: 4 }}>
                          <button
                            className="th-pill-btn"
                            data-testid={`queue-promote-${idx}`}
                            onClick={() =>
                              threadApi.manageQueue({
                                threadId: activeThreadId!,
                                operation: QueueOperation.PROMOTE_TO_STEER,
                                runId: qr.runId,
                              })
                            }
                          >
                            {t("queue.steerNow")}
                          </button>
                          {idx > 0 && (
                            <button
                              className="th-pill-btn"
                              onClick={() =>
                                threadApi.manageQueue({
                                  threadId: activeThreadId!,
                                  operation: QueueOperation.REORDER,
                                  runId: qr.runId,
                                  beforeRunId: queuedRuns[idx - 1]!.runId,
                                })
                              }
                            >
                              {t("queue.moveUp")}
                            </button>
                          )}
                          <button
                            className="th-pill-btn"
                            data-testid={`queue-edit-${idx}`}
                            onClick={() => {
                              setEditingRunId(qr.runId);
                              setEditingText(qr.prompt);
                            }}
                          >
                            {t("queue.edit")}
                          </button>
                          <button
                            className="th-pill-btn"
                            data-testid={`queue-cancel-${idx}`}
                            onClick={() =>
                              threadApi.manageQueue({
                                threadId: activeThreadId!,
                                operation: QueueOperation.CANCEL,
                                runId: qr.runId,
                              })
                            }
                          >
                            {t("queue.cancel")}
                          </button>
                        </div>
                      </>
                    )}
                  </div>
                ))}
              </div>
            )}

            {/* Live RuntimeRequest Banner */}
            {pendingReq && pendingReq.kind === RuntimeRequestKind.TOOL_APPROVAL && (
              <div className="th-banner" data-testid="pending-approval-banner">
                <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                  <strong>{t("request.approvalTitle", { tool: pendingReq.approval?.toolName ?? "Tool" })}</strong>
                  {pendingReq.approval?.outsideWorkspace && (
                    <span className="th-badge">{t("request.outsideWorkspace")}</span>
                  )}
                </div>
                <div style={{ fontFamily: "monospace", fontSize: 12 }}>
                  {pendingReq.approval?.command || pendingReq.approval?.targetPath}
                </div>
                {pendingReq.approval?.reason && <div style={{ opacity: 0.8 }}>{pendingReq.approval.reason}</div>}
                <div className="th-banner-actions">
                  <button
                    className="btn sm"
                    data-testid="approve-once-btn"
                    onClick={() => resolveRequest(pendingReq, { decision: ApprovalDecision.ACCEPT_ONCE })}
                  >
                    {t("request.approveOnce")}
                  </button>
                  <button
                    className="btn sm"
                    data-testid="approve-thread-btn"
                    onClick={() => resolveRequest(pendingReq, { decision: ApprovalDecision.ACCEPT_FOR_THREAD })}
                  >
                    {t("request.approveThread")}
                  </button>
                  <input
                    placeholder={t("request.declinePlaceholder")}
                    value={declineFeedback}
                    onChange={(e) => setDeclineFeedback(e.target.value)}
                    data-testid="decline-feedback-input"
                    style={{ flex: 1, minWidth: 160, padding: "4px 8px", borderRadius: 6, border: "1px solid rgba(128,128,128,0.3)", background: "transparent", color: "inherit" }}
                  />
                  <button
                    className="th-pill-btn"
                    data-testid="decline-btn"
                    onClick={() =>
                      resolveRequest(pendingReq, {
                        decision: ApprovalDecision.DECLINE,
                        feedback: declineFeedback,
                      })
                    }
                  >
                    {t("request.decline")}
                  </button>
                  <button
                    className="th-pill-btn"
                    onClick={() => resolveRequest(pendingReq, { decision: ApprovalDecision.CANCEL_TURN })}
                  >
                    {t("request.cancelTurn")}
                  </button>
                </div>
              </div>
            )}

            {pendingReq && pendingReq.kind === RuntimeRequestKind.PLAN_REVIEW && (
              <div className="th-banner" data-testid="pending-plan-banner">
                <strong>{t("request.planTitle", { title: pendingReq.planReview?.title ?? "Plan" })}</strong>
                <div className="th-banner-actions">
                  <button
                    className="btn sm"
                    data-testid="approve-plan-btn"
                    onClick={() => resolveRequest(pendingReq, { approvePlan: true })}
                  >
                    {t("request.approvePlan")}
                  </button>
                  <input
                    placeholder={t("request.planFeedbackPlaceholder")}
                    value={declineFeedback}
                    onChange={(e) => setDeclineFeedback(e.target.value)}
                    style={{ flex: 1, padding: "4px 8px", borderRadius: 6, border: "1px solid rgba(128,128,128,0.3)", background: "transparent", color: "inherit" }}
                  />
                  <button
                    className="th-pill-btn"
                    onClick={() => resolveRequest(pendingReq, { approvePlan: false, feedback: declineFeedback })}
                  >
                    {t("request.revisePlan")}
                  </button>
                </div>
              </div>
            )}

            {pendingReq && pendingReq.kind === RuntimeRequestKind.USER_INPUT && (
              <div className="th-banner" data-testid="pending-question-banner">
                <strong>{t("request.questionTitle")}</strong>
                {(pendingReq.userInput?.questions ?? []).map((q) => {
                  const chosen = questionChoices[q.id] ?? [];
                  return (
                    <div key={q.id} style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                      <div>{q.question}</div>
                      <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
                        {q.options.map((o) => {
                          const on = chosen.includes(o.label);
                          return (
                            <button
                              key={o.id}
                              type="button"
                              className={`th-pill-btn ${on ? "on" : ""}`}
                              data-testid={`question-opt-${o.label}`}
                              onClick={() =>
                                setQuestionChoices((prev) => ({
                                  ...prev,
                                  [q.id]: q.multiSelect
                                    ? on
                                      ? chosen.filter((x) => x !== o.label)
                                      : [...chosen, o.label]
                                    : [o.label],
                                }))
                              }
                            >
                              {o.label}
                            </button>
                          );
                        })}
                      </div>
                      {q.allowCustom && (
                        <input
                          placeholder={t("request.customAnswerPlaceholder")}
                          value={questionCustom[q.id] ?? ""}
                          onChange={(e) => setQuestionCustom((prev) => ({ ...prev, [q.id]: e.target.value }))}
                          style={{ padding: "4px 8px", borderRadius: 6, border: "1px solid rgba(128,128,128,0.3)", background: "transparent", color: "inherit" }}
                        />
                      )}
                    </div>
                  );
                })}
                <div className="th-banner-actions">
                  <button
                    className="btn sm"
                    data-testid="submit-question-btn"
                    onClick={() => {
                      const answers: Record<string, { selected: string[]; custom?: string }> = {};
                      for (const q of pendingReq.userInput?.questions ?? []) {
                        answers[q.id] = {
                          selected: questionChoices[q.id] ?? [],
                          custom: questionCustom[q.id],
                        };
                      }
                      resolveRequest(pendingReq, { answers });
                    }}
                  >
                    {t("request.submitAnswer")}
                  </button>
                </div>
              </div>
            )}

            {pendingReq && pendingReq.kind === RuntimeRequestKind.SECRET_INPUT && (
              <div className="th-banner" data-testid="pending-secret-banner">
                <strong>{t("request.secretTitle", { label: pendingReq.secretInput?.label ?? "Secret" })}</strong>
                <div style={{ fontSize: 11.5, opacity: 0.78 }}>{t("request.secretZeroPersistence")}</div>
                <div className="th-banner-actions">
                  <input
                    type="password"
                    value={secretInput}
                    onChange={(e) => setSecretInput(e.target.value)}
                    placeholder={pendingReq.secretInput?.placeholder || "••••••••"}
                    data-testid="secret-value-input"
                    style={{ flex: 1, padding: "4px 8px", borderRadius: 6, border: "1px solid rgba(128,128,128,0.3)", background: "transparent", color: "inherit" }}
                  />
                  <button
                    className="btn sm"
                    data-testid="submit-secret-btn"
                    onClick={() => resolveRequest(pendingReq, { secret: secretInput })}
                  >
                    {t("request.sendSecret")}
                  </button>
                </div>
              </div>
            )}

            {activeThreadId && (
              <>
                {slashRow}
                <ThreadComposer
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
                {options(true)}
              </>
            )}
          </div>
        </div>

        {/* Right Inspector Drawer: Unified Git Diff, Checkpoints & Runtime Info */}
        {activeThreadId && inspectorOpen && (
          <aside className="th-inspector" data-testid="thread-inspector">
            <div className="th-insp-tabs">
              <button
                type="button"
                className={inspTab === "diff" ? "on" : ""}
                data-testid="insp-tab-diff"
                onClick={() => setInspTab("diff")}
              >
                {t("inspector.diffTab")} ({diffFiles.length})
              </button>
              <button
                type="button"
                className={inspTab === "checkpoints" ? "on" : ""}
                data-testid="insp-tab-checkpoints"
                onClick={() => setInspTab("checkpoints")}
              >
                {t("inspector.checkpointsTab")} ({checkpoints.length})
              </button>
              <button
                type="button"
                className={inspTab === "info" ? "on" : ""}
                data-testid="insp-tab-info"
                onClick={() => setInspTab("info")}
              >
                {t("inspector.infoTab")}
              </button>
            </div>

            <div className="th-insp-body">
              {inspTab === "diff" && (
                <>
                  {diffFiles.length === 0 ? (
                    <div style={{ opacity: 0.68 }}>{t("inspector.noDiff")}</div>
                  ) : (
                    <>
                      {diffFiles.map((f) => (
                        <div key={f.path} style={{ display: "flex", justifyContent: "space-between", fontFamily: "monospace", fontSize: 11.5 }}>
                          <span>{f.status} {f.path}</span>
                          <span>+{f.additions} −{f.deletions}</span>
                        </div>
                      ))}
                      {diffText && <pre className="th-diff-pre">{diffText}</pre>}
                    </>
                  )}
                </>
              )}

              {inspTab === "checkpoints" && (
                <>
                  {checkpoints.length === 0 ? (
                    <div style={{ opacity: 0.68 }}>{t("inspector.noCheckpoints")}</div>
                  ) : (
                    checkpoints.map((cp) => (
                      <div
                        key={cp.checkpointId}
                        data-testid={`insp-ckpt-${cp.runOrdinal}`}
                        style={{ padding: 8, borderRadius: 7, border: "1px solid rgba(128,128,128,0.2)", display: "flex", flexDirection: "column", gap: 6 }}
                      >
                        <div style={{ display: "flex", justifyContent: "space-between" }}>
                          <strong>{t("timeline.checkpoint", { ordinal: cp.runOrdinal })}</strong>
                          <span style={{ fontFamily: "monospace", fontSize: 11, opacity: 0.7 }}>
                            {cp.gitTreeOid.slice(0, 8)}
                          </span>
                        </div>
                        {currentThread?.status === ThreadStatus.IDLE && (
                          <div style={{ display: "flex", gap: 6 }}>
                            <button
                              type="button"
                              className="th-pill-btn"
                              onClick={async () => {
                                await threadApi.rollbackCheckpoint({
                                  threadId: activeThreadId,
                                  checkpointId: cp.checkpointId,
                                  restoreFiles: false,
                                });
                                refreshInspector(activeThreadId);
                              }}
                            >
                              {t("inspector.rewindChat")}
                            </button>
                            <button
                              type="button"
                              className="th-pill-btn"
                              data-testid={`insp-rewind-files-${cp.runOrdinal}`}
                              onClick={async () => {
                                await threadApi.rollbackCheckpoint({
                                  threadId: activeThreadId,
                                  checkpointId: cp.checkpointId,
                                  restoreFiles: true,
                                });
                                refreshInspector(activeThreadId);
                              }}
                            >
                              {t("inspector.rewindFiles")}
                            </button>
                          </div>
                        )}
                      </div>
                    ))
                  )}
                </>
              )}

              {inspTab === "info" && (
                <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
                  <div>
                    <div style={{ opacity: 0.65 }}>{t("inspector.contextWindow")}</div>
                    <strong>
                      {Number(currentThread?.usage?.contextUsedTokens ?? 0n).toLocaleString()} /{" "}
                      {Number(currentThread?.usage?.contextLimitTokens ?? 200000n).toLocaleString()} tokens
                    </strong>
                  </div>
                  {(projection?.providerThreads ?? []).map((pt) => (
                    <div key={pt.providerThreadId}>
                      <div style={{ opacity: 0.65 }}>{t("inspector.providerSession")} ({pt.instanceId})</div>
                      <div style={{ fontFamily: "monospace", fontSize: 11 }}>{pt.nativeSessionId || pt.providerThreadId}</div>
                      <span className="th-badge">{pt.processAlive ? t("inspector.alive") : t("inspector.reaped")}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </aside>
        )}
      </div>
    </div>
  );
}
