import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { create } from "@bufbuild/protobuf";
import { RuntimeMode, Workspace } from "@hermesh/proto/identity";
import {
  ApprovalDecision,
  DispatchIntent,
  InteractionMode,
  ModelSelectionSchema,
  OptionDescriptorType,
  OptionValueSchema,
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
  type Thread,
  type ThreadProjection,
} from "@hermesh/proto/thread";
import {
  ORDERED_RUNTIME_MODES,
  activeOptionDescriptors,
  applyThreadEvent,
  clampRuntimeMode,
  composerPrimaryAction,
  cycleReasoningSelection,
  defaultModelSelectionFor,
  findModelEntry,
  findProviderInstance,
  isRuntimeModeAllowed,
  pendingRequestOf,
  queuedRunsOf,
  toggleInteractionMode,
} from "@hermesh/core";
import { threads as threadApi } from "../api";
import { Icon } from "../icons";
import { errorText, useStore } from "../store";
import "./thread.css";

export function ThreadPage() {
  const { t } = useTranslation("thread");
  const st = useStore();
  const sel = st.selection;
  const activeThreadId = sel.kind === "thread" ? sel.id : undefined;

  const [threadList, setThreadList] = useState<Thread[]>([]);
  const [projection, setProjection] = useState<ThreadProjection | null>(null);
  const [providers, setProviders] = useState<ProviderInstance[]>([]);
  const [maxCeiling, setMaxCeiling] = useState<RuntimeMode>(RuntimeMode.FULL_ACCESS);

  // Draft launch controls
  const defaultMachineId = useMemo(() => {
    if (sel.kind === "newThread" && sel.machineId) return sel.machineId;
    return st.machines.find((m) => m.local)?.machineId ?? st.machines[0]?.machineId ?? "";
  }, [sel, st.machines]);

  const [machineId, setMachineId] = useState<string>(defaultMachineId);
  const [projectId, setProjectId] = useState<string>(
    sel.kind === "newThread" && sel.projectId ? sel.projectId : "scratch",
  );
  const [wsMode, setWsMode] = useState<Workspace>(Workspace.IN_PLACE);
  const [baseRef, setBaseRef] = useState<string>("");

  // Composer controls
  const [modelSel, setModelSel] = useState<ModelSelection>(() =>
    create(ModelSelectionSchema, { instanceId: "", model: "", options: {} }),
  );
  const [runtimeMode, setRuntimeMode] = useState<RuntimeMode>(RuntimeMode.AUTO);
  const [interactionMode, setInteractionMode] = useState<InteractionMode>(InteractionMode.CODE);
  const [prompt, setPrompt] = useState<string>("");
  const [attachments, setAttachments] = useState<AttachmentRef[]>([]);
  const [pickerOpen, setPickerOpen] = useState<boolean>(false);
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

  const fileInputRef = useRef<HTMLInputElement>(null);

  // Sync machineId when machines load if empty
  useEffect(() => {
    if (!machineId && defaultMachineId) setMachineId(defaultMachineId);
  }, [machineId, defaultMachineId]);

  const effectiveMachineId = activeThreadId ? projection?.thread?.machineId ?? machineId : machineId;
  const effectiveProjectId = activeThreadId ? projection?.thread?.projectId ?? projectId : projectId;
  const targetMachine = st.machines.find((m) => m.machineId === effectiveMachineId);
  const machineOnline = targetMachine ? targetMachine.online : true;

  const machineProjects = useMemo(
    () => st.projects.filter((p) => !effectiveMachineId || p.machineId === effectiveMachineId),
    [st.projects, effectiveMachineId],
  );

  // Subscribe to thread list while ThreadPage is mounted (released cleanly on unmount).
  useEffect(() => {
    const ctl = new AbortController();
    (async () => {
      try {
        for await (const msg of threadApi.subscribeThreads({ includeArchived: st.archived }, { signal: ctl.signal })) {
          if (msg.snapshot) {
            setThreadList(msg.threads);
          } else {
            setThreadList((prev) => {
              const deleted = new Set(msg.deletedThreadIds);
              const map = new Map(prev.filter((x) => !deleted.has(x.threadId)).map((x) => [x.threadId, x]));
              for (const u of msg.threads) map.set(u.threadId, u);
              return [...map.values()].sort(
                (a, b) => Number(b.pinned) - Number(a.pinned) || Number((b.updatedAt?.seconds ?? 0n) - (a.updatedAt?.seconds ?? 0n)),
              );
            });
          }
        }
      } catch {
        // Stream cancelled or daemon unavailable
      }
    })();
    return () => ctl.abort();
  }, [st.archived]);

  // Load providers for target machine & project
  useEffect(() => {
    let cancelled = false;
    threadApi
      .listProviders({ machineId: effectiveMachineId, projectId: effectiveProjectId === "scratch" ? "" : effectiveProjectId })
      .then((res) => {
        if (cancelled) return;
        setProviders(res.providers);
        if (res.maxRuntimeMode !== RuntimeMode.UNSPECIFIED) {
          setMaxCeiling(res.maxRuntimeMode);
        }
        setModelSel((prev) => {
          if (prev.instanceId && res.providers.some((p) => p.instanceId === prev.instanceId)) return prev;
          const first = findProviderInstance(res.providers);
          return first ? defaultModelSelectionFor(first) : prev;
        });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [effectiveMachineId, effectiveProjectId]);

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
  const activeModel = findModelEntry(providers, modelSel);
  const optionDescriptors = activeOptionDescriptors(providers, modelSel);
  const supportsSteer = activeInst?.capabilities?.midTurnSteer ?? true;
  const clampedMode = clampRuntimeMode(runtimeMode, maxCeiling);
  const queuedRuns = useMemo(() => queuedRunsOf(projection), [projection]);
  const pendingReq = useMemo(() => pendingRequestOf(projection), [projection]);
  const primaryAction = composerPrimaryAction(currentThread, machineOnline, prompt.trim().length > 0 || attachments.length > 0, supportsSteer);

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

  const submitMessage = async (explicitIntent?: DispatchIntent) => {
    const text = prompt.trim();
    if (!text && attachments.length === 0) return;
    setBusySend(true);
    try {
      if (!activeThreadId) {
        const res = await threadApi.launchThread({
          machineId: effectiveMachineId,
          projectId,
          modelSelection: modelSel.instanceId ? modelSel : undefined,
          runtimeMode: clampedMode,
          interactionMode,
          workspace: create(WorkspaceStrategySchema, {
            mode: projectId === "scratch" ? Workspace.IN_PLACE : wsMode,
            baseRef: baseRef.trim(),
          }),
          initialPrompt: text,
          attachments,
        });
        setPrompt("");
        setAttachments([]);
        if (res.thread) {
          st.set({ selection: { kind: "thread", id: res.thread.threadId } });
        }
      } else {
        const intent =
          explicitIntent ??
          (primaryAction === "steer"
            ? DispatchIntent.STEER
            : primaryAction === "queue"
              ? DispatchIntent.QUEUE
              : DispatchIntent.AUTO);
        await threadApi.dispatchMessage({
          threadId: activeThreadId,
          text,
          attachments,
          intent,
          modelSelection: modelSel.instanceId ? modelSel : undefined,
          runtimeMode: clampedMode,
          interactionMode,
        });
        setPrompt("");
        setAttachments([]);
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

      {/* Quick Thread Switcher Strip */}
      {threadList.length > 0 && (
        <div className="th-subbar" role="tablist" aria-label={t("nav.threads")}>
          <button
            className={`th-tab ${!activeThreadId ? "active" : ""}`}
            onClick={() => st.set({ selection: { kind: "newThread" } })}
          >
            + {t("nav.newThread")}
          </button>
          {threadList.map((th) => (
            <button
              key={th.threadId}
              className={`th-tab ${activeThreadId === th.threadId ? "active" : ""}`}
              data-testid={`thread-tab-${th.threadId}`}
              onClick={() => st.set({ selection: { kind: "thread", id: th.threadId } })}
            >
              <span>{th.title}</span>
              {th.status === ThreadStatus.WAITING_REQUEST && <span className="th-badge">!</span>}
            </button>
          ))}
        </div>
      )}

      <div className="th-body">
        <div className="th-main">
          {/* Draft Launch Bar when starting a new thread */}
          {!activeThreadId && (
            <div className="th-launch-card" data-testid="thread-launch-card">
              <h3>{t("launch.title")}</h3>
              <p>{t("launch.subtitle")}</p>
              <div className="th-launch-grid">
                <label className="th-field">
                  <span>{t("launch.machine")}</span>
                  <select
                    data-testid="launch-machine-select"
                    value={machineId}
                    onChange={(e) => {
                      setMachineId(e.target.value);
                      setProjectId("scratch");
                    }}
                  >
                    {st.machines.map((m) => (
                      <option key={m.machineId} value={m.machineId}>
                        {m.machineName} ({m.online ? t("launch.online") : t("launch.offline")})
                      </option>
                    ))}
                  </select>
                </label>

                <label className="th-field">
                  <span>{t("launch.project")}</span>
                  <select
                    data-testid="launch-project-select"
                    value={projectId}
                    onChange={(e) => {
                      const pid = e.target.value;
                      setProjectId(pid);
                      const pr = machineProjects.find((x) => x.projectId === pid);
                      if (pr) setWsMode(pr.workspace);
                    }}
                  >
                    <option value="scratch">{t("launch.scratch")}</option>
                    {machineProjects.map((p) => (
                      <option key={p.projectId} value={p.projectId}>
                        {p.projectId}
                      </option>
                    ))}
                  </select>
                </label>

                {projectId !== "scratch" && (
                  <div className="th-field">
                    <span>Workspace</span>
                    <div style={{ display: "flex", gap: 6 }}>
                      <div className="th-seg">
                        <button
                          type="button"
                          className={wsMode === Workspace.IN_PLACE ? "on" : ""}
                          onClick={() => setWsMode(Workspace.IN_PLACE)}
                        >
                          {t("launch.inPlace")}
                        </button>
                        <button
                          type="button"
                          className={wsMode === Workspace.WORKTREE ? "on" : ""}
                          onClick={() => setWsMode(Workspace.WORKTREE)}
                        >
                          {t("launch.worktree")}
                        </button>
                      </div>
                      {wsMode === Workspace.WORKTREE && (
                        <input
                          placeholder={t("launch.basePlaceholder")}
                          value={baseRef}
                          onChange={(e) => setBaseRef(e.target.value)}
                          style={{ width: 120 }}
                        />
                      )}
                    </div>
                  </div>
                )}
              </div>
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

            {/* Main Composer Box */}
            <div className="th-composer">
              {/* Docked Model + Traits Picker Popover */}
              {pickerOpen && (
                <div className="th-popover" data-testid="model-traits-popover">
                  <div className="th-pop-col">
                    <h4>{t("picker.providers")}</h4>
                    {providers.map((p) => (
                      <button
                        key={p.instanceId}
                        type="button"
                        className={`th-pop-item ${activeInst?.instanceId === p.instanceId ? "on" : ""}`}
                        data-testid={`picker-provider-${p.instanceId}`}
                        onClick={() => applyConfigUpdate({ sel: defaultModelSelectionFor(p) })}
                      >
                        {p.displayName}
                      </button>
                    ))}
                  </div>
                  <div className="th-pop-col">
                    <h4>{t("picker.models")}</h4>
                    {(activeInst?.models ?? []).map((m) => (
                      <button
                        key={m.slug}
                        type="button"
                        className={`th-pop-item ${activeModel?.slug === m.slug ? "on" : ""}`}
                        data-testid={`picker-model-${m.slug}`}
                        onClick={() => {
                          if (!activeInst) return;
                          applyConfigUpdate({ sel: defaultModelSelectionFor(activeInst, m.slug) });
                        }}
                      >
                        {m.name} {m.badge ? `(${m.badge})` : ""}
                      </button>
                    ))}
                  </div>
                  <div className="th-pop-col">
                    <h4>{t("picker.options")}</h4>
                    {optionDescriptors.length === 0 ? (
                      <span style={{ opacity: 0.65 }}>{t("picker.noOptions")}</span>
                    ) : (
                      optionDescriptors.map((d) => {
                        const optKind = modelSel.options[d.id]?.kind;
                        if (d.type === OptionDescriptorType.BOOLEAN) {
                          const cur = optKind?.case === "boolValue" ? optKind.value : false;
                          return (
                            <label key={d.id} style={{ display: "flex", alignItems: "center", gap: 6, padding: "4px 2px" }}>
                              <input
                                type="checkbox"
                                checked={cur}
                                data-testid={`picker-opt-${d.id}`}
                                onChange={(e) => {
                                  const next = create(ModelSelectionSchema, {
                                    instanceId: modelSel.instanceId,
                                    model: modelSel.model,
                                    options: {
                                      ...modelSel.options,
                                      [d.id]: create(OptionValueSchema, {
                                        kind: { case: "boolValue", value: e.target.checked },
                                      }),
                                    },
                                  });
                                  applyConfigUpdate({ sel: next });
                                }}
                              />
                              <span>{d.label}</span>
                            </label>
                          );
                        }
                        const curStr = optKind?.case === "stringValue" ? optKind.value : "";
                        return (
                          <label key={d.id} className="th-field">
                            <span>{d.label}</span>
                            <select
                              value={curStr}
                              data-testid={`picker-opt-${d.id}`}
                              onChange={(e) => {
                                const next = create(ModelSelectionSchema, {
                                  instanceId: modelSel.instanceId,
                                  model: modelSel.model,
                                  options: {
                                    ...modelSel.options,
                                    [d.id]: create(OptionValueSchema, {
                                      kind: { case: "stringValue", value: e.target.value },
                                    }),
                                  },
                                });
                                applyConfigUpdate({ sel: next });
                              }}
                            >
                              {d.choices.map((c) => (
                                <option key={c.id} value={c.id}>
                                  {c.label}
                                </option>
                              ))}
                            </select>
                          </label>
                        );
                      })
                    )}
                  </div>
                </div>
              )}

              {/* Slash Command / Skill Suggestions */}
              {slashSuggestions.length > 0 && (
                <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                  {slashSuggestions.slice(0, 6).map((s) => (
                    <button
                      key={s.label}
                      type="button"
                      className="th-pill-btn"
                      onClick={() => {
                        setPrompt(s.label + " ");
                        setSlashOpen(false);
                      }}
                    >
                      {s.label}
                    </button>
                  ))}
                </div>
              )}

              {attachments.length > 0 && (
                <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                  {attachments.map((a) => (
                    <span key={a.blobSha256} className="th-badge">
                      📎 {a.name}
                    </span>
                  ))}
                </div>
              )}

              <textarea
                data-testid="thread-composer-input"
                placeholder={activeThreadId ? t("composer.placeholder") : t("launch.promptPlaceholder")}
                value={prompt}
                onChange={(e) => {
                  const v = e.target.value;
                  setPrompt(v);
                  setSlashOpen(v.startsWith("/") && !v.includes(" "));
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                    e.preventDefault();
                    submitMessage();
                  }
                }}
              />

              <div className="th-composer-foot">
                <div className="th-pills">
                  <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    style={{ display: "none" }}
                    onChange={(e) => handleAttachFiles(e.target.files)}
                  />
                  <button
                    type="button"
                    className="th-pill-btn"
                    title={t("composer.attach")}
                    onClick={() => fileInputRef.current?.click()}
                  >
                    📎
                  </button>

                  {/* Code | Plan Segmented Mode Pill */}
                  <div className="th-seg" data-testid="interaction-mode-toggle">
                    <button
                      type="button"
                      className={interactionMode !== InteractionMode.PLAN ? "on" : ""}
                      data-testid="mode-code-btn"
                      onClick={() => applyConfigUpdate({ im: InteractionMode.CODE })}
                    >
                      {t("mode.code")}
                    </button>
                    <button
                      type="button"
                      className={interactionMode === InteractionMode.PLAN ? "on" : ""}
                      data-testid="mode-plan-btn"
                      onClick={() => applyConfigUpdate({ im: InteractionMode.PLAN })}
                    >
                      {t("mode.plan")}
                    </button>
                  </div>

                  {/* Model + Traits Picker Trigger */}
                  <button
                    type="button"
                    className={`th-pill-btn ${pickerOpen ? "on" : ""}`}
                    data-testid="model-picker-trigger"
                    onClick={() => setPickerOpen((v) => !v)}
                  >
                    {activeInst?.displayName ?? "Agent"} · {activeModel?.shortName || activeModel?.name || modelSel.model || "default"}
                  </button>

                  {/* 4-Level Runtime Permission Selector */}
                  <div className="th-seg" data-testid="runtime-mode-selector">
                    {ORDERED_RUNTIME_MODES.map((m) => {
                      const allowed = isRuntimeModeAllowed(m, maxCeiling);
                      const labelKey =
                        m === RuntimeMode.SUPERVISED
                          ? "mode.supervised"
                          : m === RuntimeMode.AUTO_EDITS
                            ? "mode.autoEdits"
                            : m === RuntimeMode.AUTO
                              ? "mode.auto"
                              : "mode.fullAccess";
                      const hintKey = `${labelKey}Hint` as const;
                      return (
                        <button
                          key={m}
                          type="button"
                          disabled={!allowed}
                          title={allowed ? t(hintKey) : t("mode.ceilingLocked")}
                          className={clampedMode === m ? "on" : ""}
                          data-testid={`runtime-mode-${m}`}
                          onClick={() => applyConfigUpdate({ rm: m })}
                        >
                          {t(labelKey)}
                        </button>
                      );
                    })}
                  </div>
                </div>

                {/* Adaptive Primary Action Cluster */}
                <div style={{ display: "flex", gap: 6 }}>
                  {primaryAction === "stop" ? (
                    <button
                      type="button"
                      className="btn sm"
                      data-testid="composer-stop-btn"
                      onClick={handleStop}
                    >
                      {stopRequestedRun === currentThread?.activeRunId && currentThread?.activeRunId
                        ? t("composer.forceStop")
                        : t("composer.stop")}
                    </button>
                  ) : primaryAction === "steer" ? (
                    <>
                      <button
                        type="button"
                        className="th-pill-btn"
                        data-testid="composer-queue-btn"
                        disabled={busySend}
                        onClick={() => submitMessage(DispatchIntent.QUEUE)}
                      >
                        {t("composer.queue")}
                      </button>
                      <button
                        type="button"
                        className="btn sm"
                        data-testid="composer-steer-btn"
                        disabled={busySend}
                        onClick={() => submitMessage(DispatchIntent.STEER)}
                      >
                        {t("composer.steer")}
                      </button>
                    </>
                  ) : (
                    <button
                      type="button"
                      className="btn sm"
                      data-testid="composer-submit-btn"
                      disabled={busySend || (!prompt.trim() && attachments.length === 0)}
                      onClick={() => submitMessage()}
                    >
                      {primaryAction === "start"
                        ? t("composer.start")
                        : primaryAction === "queue"
                          ? t("composer.queue")
                          : primaryAction === "queue_offline"
                            ? t("composer.queueOffline")
                            : t("composer.send")}
                    </button>
                  )}
                </div>
              </div>
            </div>
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
