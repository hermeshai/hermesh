import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { clone, create, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { RuntimeMode, Workspace } from "@hermesh/proto/identity";
import type { Project } from "@hermesh/proto/local";
import {
  AttachmentRefSchema,
  DispatchIntent,
  InteractionMode,
  ModelSelectionSchema,
  QueueOperation,
  RunStatus,
  SessionProjectionSchema,
  SessionSchema,
  SessionStatus,
  TurnItemKind,
  WorkspaceStrategySchema,
  type AttachmentRef,
  type ModelSelection,
  type ProviderInstance,
  type SessionProjection,
  StartSessionRequestSchema,
  type StartSessionResponse,
} from "@hermesh/proto/session";
import {
  SCRATCH_PROJECT,
  activeBackgroundTasksOf,
  activeTodoSteps,
  applySessionEvent,
  backgroundTasksOf,
  clampRuntimeMode,
  clockText,
  crossProviderSwitchFrom,
  cycleReasoningSelection,
  dateOf,
  defaultModelSelectionFor,
  displayTitle,
  elapsedSeconds,
  findProviderInstance,
  groupSteps,
  isSessionRunningOrWaiting,
  language,
  pendingRequestOf,
  projectOnMachine,
  projectWorktree,
  providersForRole,
  providerUnusable,
  queuedRunsOf,
  reasoningOf,
  replyEnds,
  sessionFailure,
  sessionFocusText,
  sessionLiveReplies,
  sessionWork,
  sessionTranscript,
  statusTitle,
  updatedDate,
  windowTodoSteps,
  type IndexedTodoStep,
  type TodoStepStatus,
  type TodoStepView,
} from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local, sessions as sessionApi } from "./api";
import { ConnectionLine } from "./ConnectionCapsule";
import { GlassBar } from "./glass";
import { Icon } from "./icons";
import { layoutOf, setCard } from "./layout";
import { keys, ownKeys } from "./platform";
import { TitleInput, grantLabel, unarchive } from "./Sidebar";
import { errorText, useNow, useStore } from "./store";
import { Background, BackgroundLine } from "./session/Background";
import { ChatFrame, Failed, PromptNote, Row, Stale, Unread, UserBubble, rowHeights, rowsOf, type ChatRow } from "./session/Chat";
import { keepAttachmentBlob } from "./session/attachments";
import {
  NEW_SESSION_DRAFT,
  addDraftAttachment,
  clearDraft,
  forgetSessionDraft,
  moveDraft,
  readDraft,
  removeDraftAttachment,
  restoreDraft,
  sessionDraftKey,
  setDraftText,
  subscribeDrafts,
} from "./session/draftMemory";
import { ComposerPlace, LaunchAsk, ModelButtons, ThreadComposer, type Branches, type Place } from "./session/Composer";
import { Inspector } from "./session/Inspector";
import { SessionRequest } from "./session/Request";
import { forgetHer, keepProjects, keepProviders, lastProjects, lastProviders, remembered, rememberedHer, rememberedWorktree, remember, rememberHer, rememberWorktree } from "./session/choices";
import { ContentScope, SessionRunContext, type SessionCodeActions } from "./session/scope";
import { stripTerminalEscapes } from "./session/terminalText";
import { FilesHomeContext, type FilesHome } from "./session/files/files";
import { SidePanel } from "./session/files/SidePanel";
import { setOpen as setPanelOpen, usePanel } from "./session/files/panel";
import { Aurora, HerAsk, herMachine, nextWhoForKey, Presence, rememberedWho, rememberWho, type Who } from "./session/Presence";
import "./session/composer.css";

/**
 * The workspace a new session asks for (docs/thread-composer.md §16): a worktree from its base (the default branch
 * unless another was chosen; from origin when asked), or the project's own folder, switched first when another branch
 * was chosen there. The temporary workspace has none to choose.
 */
const HER = "her";

/** A new conversation with Her: the model chosen on this page, else the last Her model chosen on this machine, else Her's configured default; none known yet, hers to pick. */
function herSelection(providers: ProviderInstance[] = [], machineId = "", prev?: ModelSelection): ModelSelection {
  const her = providers.find((p) => p.instanceId === HER && providerUnusable(p, []) === "");
  if (prev?.instanceId === HER && prev.model && her?.models.some((m) => m.slug === prev.model)) return prev;
  const last = machineId ? rememberedHer(machineId) : undefined;
  if (last?.model && her?.models.some((m) => m.slug === last.model)) return create(ModelSelectionSchema, last);
  return her ? defaultModelSelectionFor(her) : create(ModelSelectionSchema, { instanceId: HER, model: "", options: {} });
}

function isDefaultHerSelection(her: ProviderInstance, sel: ModelSelection): boolean {
  const def = defaultModelSelectionFor(her);
  return sel.model === def.model && (reasoningOf([her], sel)?.value ?? "") === (reasoningOf([her], def)?.value ?? "");
}

/** An agent for a new session on the machine: as last time there, else the one already chosen, else Claude, Codex, Gemini, or the first that works. */
function agentSelection(providers: ProviderInstance[], machineId: string, prev?: ModelSelection): ModelSelection | undefined {
  const usable = providers.filter((p) => providerUnusable(p, []) === "" && p.instanceId !== HER);
  const last = remembered().byMachine[machineId]?.sel;
  const lastInst = last && last.instanceId !== HER && usable.find((p) => p.instanceId === last.instanceId);
  if (lastInst && lastInst.models.some((m) => m.slug === last.model)) return create(ModelSelectionSchema, last);
  if (prev?.instanceId && usable.some((p) => p.instanceId === prev.instanceId)) return prev;
  const first = ["claude", "codex", "gemini"].map((id) => usable.find((p) => p.instanceId === id)).find(Boolean) ?? usable[0] ?? findProviderInstance(providers.filter((p) => p.instanceId !== HER));
  return first ? defaultModelSelectionFor(first) : undefined;
}

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

/** A new session's first message, from Send until its machine answers: what was asked, when, and why it was refused. */
type Starting = { req: MessageInitShape<typeof StartSessionRequestSchema>; files: AttachmentRef[]; at: Date; refused?: string };

/** What a session's page shows until it reads the session: the session, its first run, and the message in it. */
function startedProjection(res: StartSessionResponse, st: Starting): SessionProjection {
  const runId = res.initialRun?.runId ?? "";
  return create(SessionProjectionSchema, {
    session: res.session,
    runs: res.initialRun ? [res.initialRun] : [],
    items: [{ itemId: "starting", sessionId: res.session?.sessionId, runId, seq: 1n, kind: TurnItemKind.USER_MESSAGE, status: "completed", text: st.req.initialPrompt, attachments: st.req.attachments }],
  });
}

const firstLine = (text: string) => text.split("\n").map((l) => l.trim()).find((l) => l) ?? "";

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
  // Working copy is remembered per (machine, project); branch selection applies only to this session.
  const [place, setPlace] = useState<Place>({ worktree: true, base: "", on: "", fromRemote: false });
  const placeEdited = useRef(false);
  const [branches, setBranches] = useState<Branches | undefined>();

  // Composer controls
  const [modelSel, setModelSel] = useState<ModelSelection>(() =>
    create(ModelSelectionSchema, { instanceId: "", model: "", options: {} }),
  );
  const [runtimeMode, setRuntimeMode] = useState<RuntimeMode>(RuntimeMode.AUTO);
  const [interactionMode, setInteractionMode] = useState<InteractionMode>(InteractionMode.CODE);
  const [slashOpenKey, setSlashOpenKey] = useState<string | null>(null);
  const [busySend, setBusySend] = useState<boolean>(false);
  const [starting, setStarting] = useState<Starting | null>(null);
  const [stopRequestedRun, setStopRequestedRun] = useState<string>("");
  const [spoken, setSpoken] = useState<number>(0);

  const draftKey = activeSessionId
    ? sessionDraftKey(activeSessionId)
    : starting
      ? `starting:${starting.req.commandId}`
      : NEW_SESSION_DRAFT;
  const draft = useSyncExternalStore(subscribeDrafts, () => readDraft(draftKey));
  const prompt = draft.text;
  const attachments = useMemo(
    () => draft.attachments.map((a) => create(AttachmentRefSchema, a)),
    [draft.attachments],
  );
  const slashOpen = slashOpenKey === draftKey;

  // Queue edit & in-flight action state
  const [editingRunId, setEditingRunId] = useState<string>("");
  const [editingText, setEditingText] = useState<string>("");
  const [managingRunId, setManagingRunId] = useState<string>("");
  useEffect(() => {
    setEditingRunId("");
    setEditingText("");
    setStopRequestedRun("");
  }, [activeSessionId]);
  // Top-bar session title inline rename state
  const [renamingSessionId, setRenamingSessionId] = useState<string | null>(null);
  const [pendingTitle, setPendingTitle] = useState<{ id: string; title: string } | null>(null);

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
    const her = !askedMachine && !askedProject && rememberedWho() === "her" ? herMachine(st.machines) : undefined;
    setMachineId(her?.machineId ?? mid);
    pickProject(her?.machineId ?? mid, her ? SCRATCH_PROJECT : pid);
    if (her) setModelSel(herSelection());
    const mine = last.byMachine[mid];
    if (mine?.mode) setRuntimeMode(mine.mode);
    setInteractionMode(InteractionMode.CODE);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sel.kind, askedMachine, askedProject, machinesKnown]);

  /** A project, and whether a session of it gets its own worktree: as last chosen for this project, else as the project works. */
  const findProject = (mid: string, pid: string) =>
    projectsOn(mid).list.find((p) => p.projectId === pid) ?? lastProjects(mid).find((p) => p.projectId === pid);
  const pickProject = (mid: string, pid: string) => {
    setProjectId(pid);
    placeEdited.current = false;
    const pr = findProject(mid, pid);
    setPlace({ worktree: projectWorktree(pr, rememberedWorktree(mid, pid)), base: "", on: "", fromRemote: false });
  };
  const pickMachine = (mid: string) => {
    setMachineId(mid);
    pickProject(mid, projectOnMachine(projectsOn(mid).list, mid, projectId, remembered().byMachine[mid]?.projectId));
  };

  // Who a new session goes to (docs/her-conversations.md §3.1): Her while her model is chosen, else an agent. Her is
  // there to choose while she is on somewhere.
  const herAt = herMachine(st.machines);
  const who: Who = !activeSessionId && modelSel.instanceId === HER ? "her" : "agent";
  // The headline rises once the owner switched, not as the page opens.
  const [rise, setRise] = useState(false);
  const chooseWho = (next: Who) => {
    if (activeSessionId || next === who || (next === "her" && !herAt)) return;
    rememberWho(next);
    setRise(true);
    if (next === "her") {
      setMachineId(herAt!.machineId);
      pickProject(herAt!.machineId, SCRATCH_PROJECT);
      setModelSel(herSelection(herAt!.machineId === effectiveMachineId ? providers : [], herAt!.machineId));
      return;
    }
    const last = remembered();
    const known = (id?: string) => !!id && st.machines.some((m) => m.machineId === id);
    const mid = [last.machineId].find(known) ?? st.machines.find((m) => m.local)?.machineId ?? st.machines[0]!.machineId;
    setMachineId(mid);
    pickProject(mid, projectOnMachine(projectsOn(mid).list, mid, "", last.byMachine[mid]?.projectId));
    setModelSel((mid === effectiveMachineId && agentSelection(providers, mid)) || create(ModelSelectionSchema, { instanceId: "", model: "", options: {} }));
  };
  const whoNow = useRef(chooseWho);
  whoNow.current = chooseWho;
  const stateNow = useRef({ who, prompt, searching: st.searching, sheet: st.sheet });
  stateNow.current = { who, prompt, searching: st.searching, sheet: st.sheet };
  // On the new session's page: Tab and ←/→ switch while the box is empty; ⇧Tab and ⌘N switch anytime.
  const canSwitch = !activeSessionId && !starting && !!herAt;
  useEffect(() => {
    if (!canSwitch) return;
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const inComposer = !!el && el.matches("textarea.cmp-in");
      const inOtherEditable =
        !!el && !inComposer && (el.matches("input,textarea,select,[contenteditable]") || !!el.closest("input,textarea,select,[contenteditable]"));
      const cur = stateNow.current;
      const overlayOpen = cur.searching || !!cur.sheet || !!document.querySelector("[role=menu],[role=dialog]");
      const next = nextWhoForKey(e, {
        who: cur.who,
        promptEmpty: cur.prompt === "",
        inComposer,
        inOtherEditable,
        overlayOpen,
      });
      if (!next) return;
      e.preventDefault();
      e.stopPropagation();
      whoNow.current(next);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [canSwitch]);

  const projSession = activeSessionId && projection?.session?.sessionId === activeSessionId ? projection.session : undefined;
  const listedAhead =
    projSession &&
    listedSession &&
    ((listedSession.progress?.lastEventSeq ?? 0n) > (projSession.progress?.lastEventSeq ?? 0n) ||
      ((listedSession.progress?.lastEventSeq ?? 0n) === (projSession.progress?.lastEventSeq ?? 0n) &&
        updatedDate(listedSession) > updatedDate(projSession)));
  const currentSession = projSession ? (listedAhead ? listedSession : projSession) : listedSession;
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
        // To Her: the model chosen on this page, else the last Her model chosen on this machine, else Her's default.
        setModelSel((prev) => (prev.instanceId === HER ? herSelection(res.providers, effectiveMachineId, prev) : agentSelection(res.providers, effectiveMachineId, prev) ?? prev));
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
    const initial = activeSessionId ? keptProjections.get(activeSessionId) ?? null : null;
    setProjection(initial);
    if (!activeSessionId) {
      return;
    }
    if (initial?.session) {
      if (initial.session.modelSelection) setModelSel(initial.session.modelSelection);
      if (initial.session.runtimeMode) setRuntimeMode(initial.session.runtimeMode);
      if (initial.session.interactionMode) setInteractionMode(initial.session.interactionMode);
      if (initial.session.maxRuntimeMode) setMaxCeiling(initial.session.maxRuntimeMode);
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
            if (!init.fromCache) setFollowing(true);
            const s = nextProj.session;
            if (!applied && s) {
              applied = true;
              if (s.modelSelection) setModelSel(s.modelSelection);
              if (s.runtimeMode) setRuntimeMode(s.runtimeMode);
              if (s.interactionMode) setInteractionMode(s.interactionMode);
              if (s.maxRuntimeMode) setMaxCeiling(s.maxRuntimeMode);
            }
          }
          if (init.fromCache) {
            throw new Error("cached");
          }
          opened = setTimeout(() => {
            open = true;
          }, 1000);
          for await (const ev of sessionApi.subscribeSession(
            { sessionId: activeSessionId, afterSeq: init.projection?.items.length ? (init.projection?.session?.progress?.lastEventSeq ?? 0n) : 0n },
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
            forgetSessionDraft(activeSessionId);
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
  const running =
    isSessionRunningOrWaiting(currentSession) &&
    (currentSession?.status !== SessionStatus.WAITING_INPUT || currentSession.activeRunId !== "");
  const waitingInput = currentSession?.status === SessionStatus.WAITING_INPUT;
  const canLiveSteer = Boolean(
    currentSession?.status === SessionStatus.RUNNING &&
      !currentSession.pendingRequestId &&
      activeInst?.capabilities?.midTurnSteer &&
      (!modelSel.instanceId || !currentSession.modelSelection?.instanceId || modelSel.instanceId === currentSession.modelSelection.instanceId),
  );

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
    if (patch.sel) {
      setModelSel(patch.sel);
      if (!activeSessionId && patch.sel.instanceId === HER && effectiveMachineId) {
        const her = providers.find((p) => p.instanceId === HER);
        if (her && isDefaultHerSelection(her, patch.sel)) forgetHer(effectiveMachineId);
        else rememberHer(effectiveMachineId, patch.sel);
      }
    }
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
      const targetSessionId = activeSessionId;
      const targetKey = sessionDraftKey(targetSessionId);
      const sent = attachments;
      clearDraft(targetKey);
      setSlashOpenKey(null);
      setSpoken((n) => n + 1);
      const defaultIntent = !machineOnline
        ? DispatchIntent.QUEUE
        : waitingInput
          ? DispatchIntent.AUTO
          : running
            ? (canLiveSteer ? DispatchIntent.STEER : DispatchIntent.QUEUE)
            : DispatchIntent.AUTO;
      const req = {
        sessionId: targetSessionId,
        text,
        attachments: sent,
        intent: explicitIntent ?? defaultIntent,
        modelSelection: modelSel.instanceId ? modelSel : undefined,
        runtimeMode: clampedMode,
        interactionMode,
        locale: language(),
      };
      const job = sends.current.then(() => sessionApi.dispatchMessage(req));
      sends.current = job.catch(() => {});
      try {
        const res = await job;
        if (res.queuedOffline) {
          setProjection((prev) => {
            const base = prev ?? create(SessionProjectionSchema, {});
            const runs = res.run && !base.runs.some((r) => r.runId === res.run!.runId) ? [...base.runs, res.run] : base.runs;
            const next = create(SessionProjectionSchema, { ...base, session: res.session ?? base.session, runs });
            rememberProjection(targetSessionId, next);
            return next;
          });
        }
      } catch (e) {
        st.show(errorText(e));
        restoreDraft(targetKey, { text, attachments: sent });
      }
      return;
    }
    const sent = attachments;
    const req = {
      machineId: effectiveMachineId,
      projectId,
      modelSelection: modelSel.instanceId ? modelSel : undefined,
      runtimeMode: clampedMode,
      interactionMode,
      workspace: create(WorkspaceStrategySchema, workspaceFor(projectId === SCRATCH_PROJECT ? undefined : place, branches)),
      initialPrompt: text,
      attachments: sent,
      commandId: crypto.randomUUID(),
      locale: language(),
    };
    clearDraft(NEW_SESSION_DRAFT);
    setSlashOpenKey(null);
    void begin({ req, files: sent, at: new Date() });
  };

  // Starts the session: once its machine answers (or the core keeps it for a machine offline), its page opens with the
  // message already in it; refused, the message stays with why. The same command id each time, so a retry after an
  // answer lost on the way makes no second session.
  const begin = async (start: Starting) => {
    setStarting(start);
    setBusySend(true);
    const began = Date.now();
    try {
      const res = await sessionApi.startSession(start.req);
      // An agent's choices are kept for the next session; Her's stay hers (rememberWho).
      if (start.req.modelSelection?.instanceId !== HER) {
        const mid = start.req.machineId ?? "";
        const pid = start.req.projectId ?? "";
        const pr = findProject(mid, pid);
        const canWorktree = Boolean(pr?.repo || pr?.workspace === Workspace.WORKTREE);
        remember(mid, {
          projectId: pid,
          sel: modelSel,
          mode: clampedMode,
          worktree: canWorktree ? start.req.workspace?.mode === Workspace.WORKTREE : undefined,
        });
      }
      // "Sending…", once shown (at 400 ms), stays half a second.
      const took = Date.now() - began;
      if (took > 400 && took < 900) await new Promise((r) => setTimeout(r, 900 - took));
      if (res.session) {
        rememberProjection(res.session.sessionId, startedProjection(res, start));
        if (start.req.commandId) {
          moveDraft(`starting:${start.req.commandId}`, sessionDraftKey(res.session.sessionId));
        }
        st.set({ selection: { kind: "session", id: res.session.sessionId } });
      }
      setStarting(null);
    } catch (e) {
      setStarting((cur) => (cur?.req.commandId === start.req.commandId ? { ...start, refused: errorText(e) } : cur));
    } finally {
      setBusySend(false);
    }
  };
  const editStart = () => {
    if (!starting) return;
    restoreDraft(NEW_SESSION_DRAFT, { text: starting.req.initialPrompt ?? "", attachments: starting.files });
    if (starting.req.commandId) {
      moveDraft(`starting:${starting.req.commandId}`, NEW_SESSION_DRAFT);
    }
    setStarting(null);
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
      const res = await sessionApi.stopBackgroundTask({ sessionId: activeSessionId, taskId });
      setProjection((prev) => {
        if (!prev) return prev;
        const stoppedById = new Map(res.stoppedTasks.map((t) => [t.taskId, t]));
        const stoppedItems = new Set(res.stoppedTasks.map((t) => t.itemId).filter(Boolean));
        const mergedTasks = prev.backgroundTasks.map((t) => {
          const hit = stoppedById.get(t.taskId);
          if (hit) {
            stoppedById.delete(t.taskId);
            return hit;
          }
          return t;
        });
        for (const t of stoppedById.values()) mergedTasks.push(t);
        const mergedItems = stoppedItems.size > 0
          ? prev.items.map((it) => (stoppedItems.has(it.itemId) && it.status === "running" ? { ...it, streaming: false, status: "interrupted" } : it))
          : prev.items;
        const next = create(SessionProjectionSchema, {
          ...prev,
          session: res.session ?? prev.session,
          backgroundTasks: mergedTasks,
          items: mergedItems,
        });
        rememberProjection(activeSessionId, next);
        return next;
      });
    } catch (e) {
      st.show(errorText(e));
    }
  };

  const handleAttachFiles = async (files: File[]) => {
    const targetKey = draftKey;
    const targetMachineId = effectiveMachineId;
    for (const f of files) {
      try {
        const buf = new Uint8Array(await f.arrayBuffer());
        const up = await sessionApi.uploadBlob({
          machineId: targetMachineId,
          name: f.name,
          mimeType: f.type || "application/octet-stream",
          data: buf,
        });
        // The same file twice (pasted again, say) is attached once.
        const att = up.attachment;
        if (att) {
          keepAttachmentBlob(att.blobSha256, buf, f.type || att.mimeType, att.name || f.name);
          addDraftAttachment(targetKey, att);
        }
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
            setDraftText(draftKey, x.label + " ");
            setSlashOpenKey(null);
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
  useEffect(() => {
    if (activeSessionId || !project || placeEdited.current) return;
    const want = projectWorktree(project, rememberedWorktree(effectiveMachineId, effectiveProjectId));
    setPlace((prev) => (prev.worktree === want ? prev : { ...prev, worktree: want }));
  }, [activeSessionId, effectiveMachineId, effectiveProjectId, project?.repo, project?.workspace]);
  const handlePlace = (next: Place) => {
    placeEdited.current = true;
    if (next.worktree !== place.worktree) {
      rememberWorktree(effectiveMachineId, effectiveProjectId, next.worktree);
    }
    setPlace(next);
  };
  const handlePickModel = (next: ModelSelection, afterApply?: () => void) => {
    const fromId = activeSessionId ? crossProviderSwitchFrom(activeProjection, modelSel, next) : undefined;
    if (fromId) {
      const fromName = findProviderInstance(providers, fromId)?.displayName || fromId;
      const toName = findProviderInstance(providers, next.instanceId)?.displayName || next.instanceId;
      st.set({
        sheet: {
          kind: "confirm",
          safe: true,
          title: t("picker.switchProviderTitle", { to: toName }),
          message: t(activeBgTasks.length > 0 ? "picker.switchProviderBgMessage" : "picker.switchProviderMessage", { from: fromName, to: toName }),
          button: t("picker.switchProviderConfirm", { to: toName }),
          run: async () => {
            await applyConfigUpdate({ sel: next });
            afterApply?.();
          },
        },
      });
      return;
    }
    void applyConfigUpdate({ sel: next }).then(() => afterApply?.());
  };
  const choice = { machineId: effectiveMachineId, projectId: effectiveProjectId, worktree: place.worktree, sel: modelSel, mode: clampedMode, plan: false };
  const forHer = !activeSessionId && !!herAt && who === "her";
  const modelButtons = (
    <ModelButtons
      providers={providersForRole(providers, forHer)}
      sel={modelSel}
      stale={providersStale}
      machineName={targetMachine?.machineName ?? effectiveMachineId}
      projectAgents={forHer ? [] : project?.agents ?? []}
      onModel={handlePickModel}
      onNotice={(text) => st.show(text)}
    />
  );
  const composerProps = {
    mode: clampedMode,
    maxCeiling,
    onMode: (rm: RuntimeMode) => applyConfigUpdate({ rm }),
    model: modelButtons,
  };
  const placeRow = !activeSessionId && who === "agent" && (project?.repo || project?.workspace === Workspace.WORKTREE) && <ComposerPlace place={place} branches={branches} onPlace={handlePlace} />;
  const presence = herAt && !activeSessionId ? <Presence who={who} onWho={chooseWho} /> : undefined;
  const ask = who === "her" && herAt ? (
    <HerAsk top={presence} machine={st.machines.find((m) => m.machineId === effectiveMachineId) ?? herAt} rise={rise} />
  ) : (
    <LaunchAsk
      top={presence}
      rise={rise}
      machines={st.machines}
      projects={projectsHere}
      projectsStale={projectsOn(effectiveMachineId).stale}
      choice={choice}
      onMachine={pickMachine}
      onProject={(pid) => pickProject(machineId, pid)}
      after={<ConnectionLine machine={targetMachine} linkUp={reachable} work={busySend ? { text: t("work.starting") } : undefined} delayWork />}
      onAddProject={() =>
        st.set({
          sheet: {
            kind: "addProject",
            machineId,
            then: (pid, wt) => {
              placeEdited.current = true;
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
  const background = st.background && allBgTasks.length > 0;
  const { card } = layoutOf(st);
  // The side panel takes the session's right (docs/session-files.md §3.4): the cards step aside while it is
  // open, and the info card cannot be shown until it is put away.
  const filePanel = usePanel(activeSessionId ?? "");
  const filesOpen = !!activeSessionId && filePanel.open;
  const info = card !== "hidden" && !background && !filesOpen;
  // The background card keeps the conversation clear of it as the info card does, when there is room for one.
  const dockBg = background && !filesOpen && layoutOf({ ...st, inspector: true, peek: null }).card === "docked";
  const dock = (info && card === "docked") || dockBg;

  const runQueueOp = async (req: Parameters<typeof sessionApi.manageQueue>[0]): Promise<boolean> => {
    const busyKey = req.runId || "resume";
    setManagingRunId(busyKey);
    try {
      const res = await sessionApi.manageQueue(req);
      setProjection((prev) => {
        if (!prev) return prev;
        const prevSeq = prev.session?.progress?.lastEventSeq ?? 0n;
        const resSeq = res.session?.progress?.lastEventSeq ?? 0n;
        if (resSeq > 0n && prevSeq >= resSeq && !res.session?.offlineQueued && !prev.session?.offlineQueued) {
          return prev;
        }
        const stillQueued = new Map(res.queuedRuns.map((r) => [r.runId, r]));
        const shown = new Set(prev.items.filter((it) => it.kind === TurnItemKind.USER_MESSAGE).map((it) => it.runId));
        const activeId = res.session?.activeRunId ?? prev.session?.activeRunId ?? "";
        const mergedRuns = prev.runs.map((r) => {
          const q = stillQueued.get(r.runId);
          if (q) {
            stillQueued.delete(r.runId);
            return q;
          }
          if (r.status === RunStatus.QUEUED && !shown.has(r.runId) && r.runId !== activeId) {
            return { ...r, status: RunStatus.CANCELLED };
          }
          return r;
        });
        for (const q of stillQueued.values()) mergedRuns.push(q);
        const next = create(SessionProjectionSchema, {
          ...prev,
          session: res.session ?? prev.session,
          runs: mergedRuns,
        });
        if (req.sessionId) rememberProjection(req.sessionId, next);
        return next;
      });
      return true;
    } catch (e) {
      st.show(errorText(e));
      return false;
    } finally {
      setManagingRunId((cur) => (cur === busyKey ? "" : cur));
    }
  };

  const requestCard = activeSessionId && session && pendingReq && (
    <SessionRequest key={pendingReq.requestId} s={session} req={pendingReq} onResponded={() => setSpoken((n) => n + 1)} />
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
            <button
              className="btn th-qbtn"
              data-testid="queue-resume-btn"
              disabled={managingRunId === "resume"}
              onClick={() => void runQueueOp({ sessionId: activeSessionId, operation: QueueOperation.RESUME_QUEUE })}
            >
              {t("queue.resume")}
            </button>
          </>
        )}
      </div>
      {queuedRuns.map((qr, idx) => {
        const rowBusy = managingRunId === qr.runId;
        return (
          <div key={qr.runId} className="th-qrow" data-testid={`queued-run-${idx}`}>
            {editingRunId === qr.runId ? (
              <>
                <input className="field" value={editingText} onChange={(e) => setEditingText(e.target.value)} data-testid="queue-edit-input" />
                <button
                  className="btn primary th-qbtn"
                  data-testid="queue-edit-save"
                  disabled={rowBusy}
                  onClick={async () => {
                    if (await runQueueOp({ sessionId: activeSessionId, operation: QueueOperation.EDIT, runId: qr.runId, text: editingText })) {
                      setEditingRunId("");
                    }
                  }}
                >
                  {t("queue.save")}
                </button>
              </>
            ) : (
              <>
                <span className="th-qtext sel-text" title={qr.prompt || qr.attachments.map((a) => a.name).join(", ")}>
                  {qr.prompt || qr.attachments.map((a) => a.name).join(", ")}
                </span>
                <button
                  className="btn th-qbtn"
                  data-testid={`queue-promote-${idx}`}
                  disabled={rowBusy}
                  onClick={async () => {
                    if (await runQueueOp({ sessionId: activeSessionId, operation: QueueOperation.PROMOTE_TO_STEER, runId: qr.runId })) {
                      setSpoken((n) => n + 1);
                    }
                  }}
                >
                  {t("queue.steerNow")}
                </button>
                {idx > 0 && (
                  <button
                    className="btn th-qbtn"
                    disabled={rowBusy}
                    onClick={() => void runQueueOp({ sessionId: activeSessionId, operation: QueueOperation.REORDER, runId: qr.runId, beforeRunId: queuedRuns[idx - 1]!.runId })}
                  >
                    {t("queue.moveUp")}
                  </button>
                )}
                <button
                  className="btn th-qbtn"
                  data-testid={`queue-edit-${idx}`}
                  disabled={rowBusy}
                  onClick={() => {
                    setEditingRunId(qr.runId);
                    setEditingText(qr.prompt);
                  }}
                >
                  {t("queue.edit")}
                </button>
                <button
                  className="btn th-qbtn"
                  data-testid={`queue-cancel-${idx}`}
                  disabled={rowBusy}
                  onClick={() => void runQueueOp({ sessionId: activeSessionId, operation: QueueOperation.CANCEL, runId: qr.runId })}
                >
                  {t("queue.cancel")}
                </button>
              </>
            )}
          </div>
        );
      })}
    </div>
  );

  // The conversation, reduced from SessionProjection (core transcript.ts) and drawn with unified conversation rows.
  const steps = useMemo(() => groupSteps(sessionTranscript(activeProjection)), [activeProjection]);
  const todoSteps = useMemo(() => activeTodoSteps(activeProjection), [activeProjection]);
  const ends = useMemo(() => replyEnds(steps, running), [steps, running]);
  const failure = sessionFailure(currentSession);
  const activeRun = activeProjection?.runs.find((r) => r.runId === currentSession?.activeRunId);
  const work =
    currentSession?.status === SessionStatus.PREPARING ? { text: t("work.starting") }
    : currentSession?.status === SessionStatus.QUEUED && !currentSession.offlineQueued ? { text: statusTitle(SessionStatus.QUEUED) }
    : currentSession?.status === SessionStatus.RUNNING
      ? { text: sessionFocusText(activeProjection) ?? t(`work.${sessionWork(activeProjection)}`), since: dateOf(activeRun?.startedAt) }
    : undefined;
  const handleOpenWakeTask = useCallback(
    (reason: string) => {
      const matched = allBgTasks.find((bt) => bt.title === reason || bt.command === reason);
      if (filePanel.open) setPanelOpen(filePanel, false);
      st.set({ background: true, bgTask: matched?.taskId ?? null });
    },
    [allBgTasks, filePanel, st],
  );
  const writing = useMemo(() => sessionLiveReplies(activeProjection), [activeProjection]);
  // Whose files the replies' links are (docs/session-files.md §3): the session's machine, this Mac or another.
  const sessionMachine = currentSession?.machineId ?? "";
  const filesHome = useMemo<FilesHome | null>(
    () => (activeSessionId ? { session: activeSessionId, machine: sessionMachine, local: st.machines.some((m) => m.machineId === sessionMachine && m.local) } : null),
    [activeSessionId, sessionMachine, st.machines],
  );
  const rows = useMemo(
    () => rowsOf(steps, ends, failure).map((r) => (writing.has(r.it.seq) ? { ...r, live: true } : r)),
    [steps, ends, failure?.message, writing],
  );
  const grant = session ? grantLabel(st, session) : "";
  // Discarding a session kept here for its offline machine deletes it, as Her's waiting messages are.
  const discardOffline = () => {
    if (!activeSessionId) return;
    const sid = activeSessionId;
    void st.run(async () => {
      await sessionApi.deleteSession({ sessionId: sid });
      forgetSessionDraft(sid);
      st.set({ selection: { kind: "overview" } });
    });
  };
  const archived = currentSession?.status === SessionStatus.ARCHIVED;
  const sendOutputToChat = (command: string, exitCode: number, output: string, stopped = false) => {
    if (!activeSessionId || archived) return;
    const cleanOut = stripTerminalEscapes(output).trim();
    const capped =
      cleanOut.length > 12_000
        ? `${cleanOut.slice(0, 6_000)}\n...[truncated]...\n${cleanOut.slice(-6_000)}`
        : cleanOut;
    const header = stopped
      ? ts("content.sentStoppedHeader")
      : ts("content.sentResultHeader", { code: exitCode });
    const text = capped
      ? `${header}\n\`\`\`bash\n${command}\n\`\`\`\n\`\`\`text\n${capped}\n\`\`\``
      : `${header}\n\`\`\`bash\n${command}\n\`\`\`\n${ts("content.noOutput")}`;
    setSpoken((n) => n + 1);
    const req = {
      sessionId: activeSessionId,
      text,
      attachments: [],
      intent: DispatchIntent.QUEUE,
      modelSelection: modelSel.instanceId ? modelSel : undefined,
      runtimeMode: clampedMode,
      interactionMode,
      locale: language(),
    };
    const job = sends.current.then(() => sessionApi.dispatchMessage(req));
    sends.current = job.catch(() => {});
    void job.catch((e) => st.show(errorText(e)));
  };
  const codeActions = useMemo<SessionCodeActions | null>(
    () => (activeSessionId && !archived ? { sessionId: activeSessionId, sendOutputToChat } : null),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [activeSessionId, archived, modelSel, clampedMode, interactionMode],
  );
  const unread = steps.length === 0 && (currentSession?.progress?.lastEventSeq ?? 0n) > 0n && !currentSession?.offlineQueued;
  const behind = (activeProjection?.session?.progress?.lastEventSeq ?? 0n) < (listedSession?.progress?.lastEventSeq ?? 0n);
  const shownSessionTitle =
    currentSession
      ? (pendingTitle?.id === currentSession.sessionId ? pendingTitle.title : displayTitle(currentSession))
      : "…";
  const renamingHeaderTitle = Boolean(activeSessionId && currentSession && renamingSessionId === currentSession.sessionId);

  const header = (
    <>
      <div className="ctitle">
        {renamingHeaderTitle && currentSession ? (
          <TitleInput
            key={currentSession.sessionId}
            initial={shownSessionTitle}
            className="crename"
            testId="thread-title-input"
            onDone={(title) => {
              const sid = currentSession.sessionId;
              setRenamingSessionId((cur) => (cur === sid ? null : cur));
              if (!title) return;
              setPendingTitle({ id: sid, title });
              void st
                .run(async () => {
                  const res = await sessionApi.updateSession({ sessionId: sid, title });
                  if (res.session) {
                    setProjection((prev) => {
                      if (!prev || prev.session?.sessionId !== sid) return prev;
                      const next = { ...prev, session: res.session };
                      rememberProjection(sid, next);
                      return next;
                    });
                  }
                })
                .finally(() => setPendingTitle((cur) => (cur?.id === sid ? null : cur)));
            }}
          />
        ) : activeSessionId && currentSession ? (
          <h2
            className="no-drag"
            data-testid="thread-title"
            title={shownSessionTitle}
            onDoubleClick={() => setRenamingSessionId(currentSession.sessionId)}
          >
            {shownSessionTitle}
          </h2>
        ) : (
          <h2 data-testid="thread-title">
            {activeSessionId ? "…" : starting ? (firstLine(starting.req.initialPrompt ?? "") || t("launch.title")) : t("launch.title")}
          </h2>
        )}
        {activeSessionId && currentSession && (
          <span className="th-where" data-testid="session-where">
            <Icon name={targetMachine?.local ? "laptop" : "server"} />
            {targetMachine?.machineName ?? currentSession.machineId} · {currentSession.projectId === SCRATCH_PROJECT ? t("launch.scratch") : currentSession.projectId}
          </span>
        )}
      </div>
      {activeSessionId && allBgTasks.length > 0 && (
        <button
          type="button"
          className={`tb ${background ? "on" : ""}`}
          title={t("background.title")}
          aria-label={t("background.title")}
          aria-pressed={background}
          data-testid="toolbar-background-btn"
          onClick={() => st.set({ background: !background, bgTask: null })}
        >
          <Icon name="layers" />
          {activeBgTasks.length > 0 && <span className="tb-badge" data-testid="toolbar-background-badge">{activeBgTasks.length}</span>}
        </button>
      )}
      {activeSessionId && (
        <button
          className={`tb ${info ? "on" : ""}`}
          title={ts(info ? "page.hideInfo" : "page.showInfo", { keys: keys(ownKeys.toggleInspector) })}
          aria-label={ts("page.info")}
          aria-pressed={info}
          disabled={filesOpen}
          data-testid="toolbar-info-btn"
          onClick={() => setCard(st, !info)}
        >
          <Icon name="info" />
        </button>
      )}
      {activeSessionId && !filesOpen && (
        <button
          className="tb"
          title={ts("page.showPanel")}
          aria-label={ts("panel.title")}
          aria-pressed={false}
          data-testid="toolbar-panel-btn"
          onClick={() => setPanelOpen(filePanel, true)}
        >
          <Icon name="panelRight" />
        </button>
      )}
    </>
  );

  if (!activeSessionId && !starting) {
    return (
      <>
        <div className="ctool drag">{header}</div>
        <div className={`dwrap th ${herAt ? "th-new" : ""}`} data-who={who} data-testid="thread-page">
          {herAt && <Aurora />}
          <div className="dmain" data-testid="thread-launch-card">
            {ask}
            <div className="cmp-zone">
              <div className="cmp-col">
                {slashRow}
                {placeRow}
                <ThreadComposer
                  {...composerProps}
                  value={prompt}
                  onChange={(v) => {
                    setDraftText(draftKey, v);
                    setSlashOpenKey(v.startsWith("/") && !v.includes(" ") ? draftKey : null);
                  }}
                  onSend={() => submitMessage()}
                  running={false}
                  sending={busySend}
                  placeholder={who === "her" ? t("chat.placeholder", { ns: "her", name: "Her" }) : t("launch.promptPlaceholder")}
                  attachments={attachments}
                  onAttach={handleAttachFiles}
                  onRemove={(sha) => removeDraftAttachment(draftKey, sha)}
                  offline={!machineOnline}
                />
              </div>
            </div>
          </div>
        </div>
      </>
    );
  }

  return (
    <div className={`spage ${filesOpen ? "bgopen" : ""}`}>
      <div className="scol">
        <GlassBar className="ctool drag">{header}</GlassBar>
        {!activeSessionId && starting ? (
          // From Send until the machine answers, as the session's page will be: the owner's message, faded, with where it
          // is under it ("Sending…" once that takes 400 ms), or why it was not sent, with Try Again and Edit.
          <div className="dwrap th" data-testid="thread-page">
            <div className="dmain" data-testid="session-starting">
              <ChatFrame
                key="starting"
                rows={[startingRow(starting)]}
                initial={rowHeights}
                label={ts("chat.log")}
                follow={undefined}
                row={() => (
                  <UserBubble
                    text={starting.req.initialPrompt ?? ""}
                    attachments={starting.files}
                    machineId={starting.req.machineId}
                    at={starting.at}
                    waiting={!starting.refused}
                    note={starting.refused
                      ? <PromptNote kind="refused" why={starting.refused} onRetry={() => void begin({ ...starting, refused: undefined })} onEdit={editStart} />
                      : <PromptNote kind="sending" />}
                  />
                )}
                foot={<ConnectionLine machine={targetMachine} linkUp={reachable} />}
                control={(onHeight) => (
                  <ComposerZone onHeight={onHeight}>
                    <ThreadComposer
                      {...composerProps}
                      value={prompt}
                      onChange={(v) => setDraftText(draftKey, v)}
                      onSend={() => {}}
                      running={false}
                      sending
                      placeholder={t("composer.placeholderIdle")}
                      attachments={attachments}
                      onAttach={handleAttachFiles}
                      onRemove={(sha) => removeDraftAttachment(draftKey, sha)}
                      offline={!machineOnline}
                    />
                  </ComposerZone>
                )}
              />
            </div>
          </div>
        ) : (
          <div className={`dwrap th ${dock ? "dock" : ""} ${dockBg ? "dock-bg" : ""}`} data-testid="thread-page">
            <div className="dmain">
              <ContentScope value={activeSessionId!}>
                <SessionRunContext.Provider value={codeActions}>
                <FilesHomeContext.Provider value={filesHome}>
                <ChatFrame
                  key={activeSessionId}
                  rows={rows}
                  initial={rowHeights}
                  label={ts("chat.log")}
                  follow={currentSession?.status}
                  spoken={spoken}
                  unread={unread && session ? <Unread s={session} /> : undefined}
                  row={(r) =>
                    currentSession?.offlineQueued && r.it.kind === "user" ? (
                      // Queued here while its machine is offline: faded, and said under it, with Discard.
                      <UserBubble
                        text={r.it.text}
                        attachments={r.it.attachments}
                        sessionId={activeSessionId}
                        machineId={currentSession.machineId}
                        at={r.it.at}
                        waiting
                        note={<PromptNote kind="waiting" machine={targetMachine?.machineName ?? currentSession.machineId} onDiscard={discardOffline} />}
                      />
                    ) : (
                      <Row it={r.it} rowKey={r.key} end={r.end} live={r.live} sessionId={activeSessionId!} machineId={currentSession?.machineId} clients={st.clients} starter={session?.createdBy ?? "owner"} previous={r.previous} onOpenWakeTask={handleOpenWakeTask} />
                    )
                  }
                  head={
                    archived && session && (
                      <div className="anote">
                        <Icon name="archive" className="sm" />
                        {ts(session.workspaceStrategy?.worktreeKept ? "chat.archivedKept" : "chat.archived")}
                        <button className="link" onClick={() => unarchive(st, session)}>{ts("chat.unarchive")}</button>
                      </div>
                    )
                  }
                  foot={
                    session && (
                      <>
                        {todoSteps.length > 0 && (
                          <TodoSpine steps={todoSteps} work={work} />
                        )}
                        <ConnectionLine machine={targetMachine} linkUp={archived || (following && reachable)} work={todoSteps.length > 0 ? undefined : work} />
                        <BackgroundLine tasks={activeBgTasks} />
                        <Stale s={session} behind={behind} />
                        {failure && <Failed s={session} failure={failure} grant={grant} />}
                      </>
                    )
                  }
                  control={archived ? undefined : (onHeight) => (
                    <ComposerZone onHeight={onHeight}>
                      {requestCard}
                      {queueCard}
                      {slashRow}
                      <ThreadComposer
                        {...composerProps}
                        value={prompt}
                        onChange={(v) => {
                          setDraftText(draftKey, v);
                          setSlashOpenKey(v.startsWith("/") && !v.includes(" ") ? draftKey : null);
                        }}
                        onSend={() => submitMessage()}
                        onSteer={() => submitMessage(DispatchIntent.STEER)}
                        onQueue={() => submitMessage(DispatchIntent.QUEUE)}
                        onStop={handleStop}
                        running={running}
                        liveSteer={canLiveSteer}
                        waitingInput={waitingInput}
                        sending={busySend}
                        placeholder={running ? t("composer.placeholderRunning") : t("composer.placeholderIdle")}
                        attachments={attachments}
                        onAttach={handleAttachFiles}
                        onRemove={(sha) => removeDraftAttachment(draftKey, sha)}
                        offline={!machineOnline}
                      />
                    </ComposerZone>
                  )}
                />
                </FilesHomeContext.Provider>
                </SessionRunContext.Provider>
              </ContentScope>
            </div>
            {info && session && <Inspector s={session} providers={providers} />}
            {background && !filesOpen && <Background tasks={allBgTasks} onStop={handleStopBackgroundTask} />}
          </div>
        )}
      </div>
      {activeSessionId && filesHome && <SidePanel home={filesHome} running={running} />}
    </div>
  );
}

/** The one row of a session starting: the owner's message. */
function startingRow(st: Starting): ChatRow {
  const it = { kind: "user" as const, seq: 1n, text: st.req.initialPrompt ?? "", attachments: st.files, at: st.at, asker: "owner" };
  return { key: "starting", kind: "user", it, end: false, live: false, previous: undefined };
}

const STEP_TOKEN_RE = /`([^`]+)`|(\b[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_./-]+|\b[a-zA-Z0-9_-]+\.(?:swift|tsx?|jsx?|go|rs|py|css|html|md|json|ya?ml|sql|db|app)\b)/g;

function withSlashBreaks(raw: string): ReactNode[] {
  const parts = raw.split("/");
  const out: ReactNode[] = [];
  for (let i = 0; i < parts.length; i++) {
    if (i > 0) out.push("/", <wbr key={`w:${i}`} />);
    if (parts[i]) out.push(parts[i]);
  }
  return out;
}

function renderStepText(text: string): ReactNode {
  const nodes: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(STEP_TOKEN_RE)) {
    const at = m.index ?? 0;
    if (at > last) nodes.push(text.slice(last, at));
    const token = m[1] ?? m[2] ?? m[0];
    nodes.push(
      <code key={`c:${at}`} className="th-todo-code">
        {withSlashBreaks(token)}
      </code>,
    );
    last = at + m[0].length;
  }
  if (last === 0) return text;
  if (last < text.length) nodes.push(text.slice(last));
  return nodes;
}

function TodoStepGlyph({ status }: { status: TodoStepStatus }) {
  switch (status) {
    case "in_progress":
      return <Mark size={17} variant="seq" />;
    case "completed":
      return <Icon name="check" className="th-todo-ic" />;
    case "skipped":
      return <Icon name="minus" className="th-todo-ic" />;
    case "failed":
      return <Icon name="x" className="th-todo-ic" />;
    default:
      return (
        <svg width="12" height="12" viewBox="0 0 14 14" fill="none" aria-hidden="true">
          <circle cx="7" cy="7" r="4.6" stroke="var(--l4)" strokeWidth="1.35" />
        </svg>
      );
  }
}

function TodoSpine({
  steps,
  work,
}: {
  steps: TodoStepView[];
  work?: { text: string; since?: Date };
}) {
  const { t } = useTranslation("thread");
  const now = useNow(work?.since ? 1000 : 60_000);
  const elapsed = work?.since ? clockText(elapsedSeconds(work.since, now)) : undefined;
  const win = useMemo(() => windowTodoSteps(steps, work?.text), [steps, work?.text]);
  if (!win) return null;

  const activeOrderByIndex = new Map(win.active.map((a, idx) => [a.index, idx]));
  const firstActiveIdx = win.active[0]?.index ?? 0;
  const lastActiveIdx = win.active[win.active.length - 1]?.index ?? 0;

  const segClassFor = (step: IndexedTodoStep) => {
    if (step.index < firstActiveIdx && (step.status === "completed" || step.status === "skipped")) return "traversed";
    if (step.index >= lastActiveIdx) return "future-seg";
    return "";
  };

  const renderActiveNode = (step: IndexedTodoStep, activeOrder: number) => {
    const isLastActive = step.index === lastActiveIdx;
    const showSub = isLastActive && !!win.subFocus;
    const deduplicatedWork = isLastActive && !win.subFocus && !!work;
    return (
      <div
        key={`${step.index}:${step.text}`}
        className={`th-todo-node active ${step.status} ${segClassFor(step)}`}
        data-status={step.status}
        data-testid={`thread-todo-step-${step.index}`}
      >
        <span className="th-todo-g">
          <TodoStepGlyph status={step.status} />
        </span>
        <div style={{ minWidth: 0 }}>
          <div className="th-todo-main" data-testid={deduplicatedWork ? "thread-working" : undefined}>
            <span className={deduplicatedWork ? "th-work-t" : undefined}>{renderStepText(step.text)}</span>
            <span className="th-todo-meta">
              {activeOrder === 0 ? (
                `${win.stepNumber}/${win.total}${!win.subFocus && elapsed ? ` · ${elapsed}` : ""}`
              ) : (
                <span className="th-todo-chip run">{t("todo.parallel")}</span>
              )}
            </span>
          </div>
          {showSub && (
            <div className="th-todo-sub" data-testid="thread-working">
              <span className="th-work-t shimmer" title={win.subFocus}>
                {withSlashBreaks(win.subFocus!)}
              </span>
              {elapsed && <span className="el">· {elapsed}</span>}
            </div>
          )}
        </div>
      </div>
    );
  };

  const renderPassiveNode = (step: IndexedTodoStep) => (
    <div
      key={`${step.index}:${step.text}`}
      className={`th-todo-node ${step.status} ${segClassFor(step)}`}
      data-status={step.status}
      data-testid={`thread-todo-step-${step.index}`}
    >
      <span className="th-todo-g">
        <TodoStepGlyph status={step.status} />
      </span>
      <div className="th-todo-row-txt">
        {renderStepText(step.text)}
        {step.status === "skipped" && <span className="th-todo-chip skip">{t("todo.skipped")}</span>}
      </div>
    </div>
  );

  return (
    <div className="th-todo" role="region" aria-label={t("todo.label")} data-testid="thread-todo-card">
      {win.all.map((step) => {
        const activeOrder = activeOrderByIndex.get(step.index);
        return activeOrder !== undefined ? renderActiveNode(step, activeOrder) : renderPassiveNode(step);
      })}
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
