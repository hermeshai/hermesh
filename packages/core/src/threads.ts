import { create } from "@bufbuild/protobuf";
import { RuntimeMode } from "@hermesh/proto/identity";
import {
  BackgroundTaskStatus,
  InteractionMode,
  ModelSelectionSchema,
  OptionDescriptorType,
  OptionValueSchema,
  RunStatus,
  RuntimeRequestStatus,
  SessionProjectionSchema,
  SessionStatus,
  TurnItemKind,
  type BackgroundTask,
  type ModelCatalogEntry,
  type ModelSelection,
  type OptionDescriptor,
  type ProviderInstance,
  type Run,
  type RuntimeRequest,
  type Session,
  type SessionEvent,
  type SessionProjection,
  type TurnItem,
} from "@hermesh/proto/session";

/** Ordered permission modes from narrowest to broadest. */
export const ORDERED_RUNTIME_MODES: readonly RuntimeMode[] = [
  RuntimeMode.SUPERVISED,
  RuntimeMode.AUTO_EDITS,
  RuntimeMode.AUTO,
  RuntimeMode.FULL_ACCESS,
];

export function runtimeModeRank(mode: RuntimeMode): number {
  switch (mode) {
    case RuntimeMode.SUPERVISED:
      return 1;
    case RuntimeMode.AUTO_EDITS:
      return 2;
    case RuntimeMode.AUTO:
      return 3;
    case RuntimeMode.FULL_ACCESS:
      return 4;
    default:
      return 0;
  }
}

export function normalizeRuntimeMode(mode: RuntimeMode): RuntimeMode {
  return runtimeModeRank(mode) === 0 ? RuntimeMode.AUTO : mode;
}

export function isRuntimeModeAllowed(mode: RuntimeMode, maxCeiling: RuntimeMode): boolean {
  const ceilRank = runtimeModeRank(maxCeiling);
  if (ceilRank === 0) return true;
  return runtimeModeRank(normalizeRuntimeMode(mode)) <= ceilRank;
}

export function clampRuntimeMode(requested: RuntimeMode, maxCeiling: RuntimeMode): RuntimeMode {
  const norm = normalizeRuntimeMode(requested);
  const ceilRank = runtimeModeRank(maxCeiling);
  if (ceilRank > 0 && runtimeModeRank(norm) > ceilRank) {
    return maxCeiling;
  }
  return norm;
}

export function isSessionRunningOrWaiting(s?: Session): boolean {
  if (!s) return false;
  return (
    s.status === SessionStatus.PREPARING ||
    s.status === SessionStatus.RUNNING ||
    s.status === SessionStatus.WAITING_INPUT
  );
}

/**
 * Runs waiting for a turn, in their order. One whose message is in the conversation already (a session kept on this
 * device for its offline machine shows its first message) is that bubble, not the queue's; a machine writes a queued
 * run's message only once it starts.
 */
export function queuedRunsOf(proj?: SessionProjection | null): Run[] {
  if (!proj) return [];
  const shown = new Set(proj.items.filter((it) => it.kind === TurnItemKind.USER_MESSAGE).map((it) => it.runId));
  return proj.runs
    .filter((r) => r.status === RunStatus.QUEUED && !shown.has(r.runId))
    .sort((a, b) => a.queueOrder - b.queueOrder);
}

/** Background tasks still running, awaited tasks before long-lived daemons. */
export function activeBackgroundTasksOf(proj?: SessionProjection | null): BackgroundTask[] {
  if (!proj) return [];
  return proj.backgroundTasks
    .filter((t) => t.status === BackgroundTaskStatus.RUNNING)
    .sort((a, b) => Number(b.awaited) - Number(a.awaited) || Number((a.startedAt?.seconds ?? 0n) - (b.startedAt?.seconds ?? 0n)));
}

/** All recorded background tasks of a session: running ones first, then newest finished ones. */
export function backgroundTasksOf(proj?: SessionProjection | null): BackgroundTask[] {
  if (!proj) return [];
  return [...proj.backgroundTasks].sort((a, b) => {
    const ar = a.status === BackgroundTaskStatus.RUNNING ? 0 : 1;
    const br = b.status === BackgroundTaskStatus.RUNNING ? 0 : 1;
    if (ar !== br) return ar - br;
    if (ar === 0) return Number(b.awaited) - Number(a.awaited) || Number((a.startedAt?.seconds ?? 0n) - (b.startedAt?.seconds ?? 0n));
    return Number((b.completedAt?.seconds ?? b.startedAt?.seconds ?? 0n) - (a.completedAt?.seconds ?? a.startedAt?.seconds ?? 0n));
  });
}

/** TurnItem IDs backed by a background task that is still running. */
export function activeBackgroundItemIdsOf(proj?: SessionProjection | null): Set<string> {
  const out = new Set<string>();
  for (const bt of proj?.backgroundTasks ?? []) {
    if (bt.status === BackgroundTaskStatus.RUNNING && bt.itemId !== "") out.add(bt.itemId);
  }
  return out;
}

export function pendingRequestOf(proj?: SessionProjection | null): RuntimeRequest | undefined {
  if (!proj) return undefined;
  if (proj.session?.pendingRequestId) {
    const exact = proj.requests.find(
      (r) => r.requestId === proj.session?.pendingRequestId && r.status === RuntimeRequestStatus.PENDING,
    );
    if (exact) return exact;
  }
  return proj.requests.find((r) => r.status === RuntimeRequestStatus.PENDING);
}

export type ComposerPrimaryAction =
  | "start"
  | "send"
  | "steer"
  | "queue"
  | "stop"
  | "queue_offline";

export function composerPrimaryAction(
  session: Session | undefined,
  machineOnline: boolean,
  hasInput: boolean,
  supportsLiveSteer: boolean,
): ComposerPrimaryAction {
  if (!machineOnline) return "queue_offline";
  if (!session) return "start";
  const running = session.status === SessionStatus.RUNNING || session.status === SessionStatus.PREPARING;
  if (running) {
    if (!hasInput) return "stop";
    return supportsLiveSteer ? "steer" : "queue";
  }
  if (session.status === SessionStatus.WAITING_INPUT) {
    return hasInput ? "send" : "stop";
  }
  return "send";
}

export function findProviderInstance(
  providers: readonly ProviderInstance[],
  instanceId?: string,
): ProviderInstance | undefined {
  if (!providers.length) return undefined;
  if (instanceId) {
    const hit = providers.find((p) => p.instanceId === instanceId);
    if (hit) return hit;
  }
  return providers.find((p) => p.enabled && p.installed) ?? providers[0];
}

export function findModelEntry(
  providers: readonly ProviderInstance[],
  selection?: ModelSelection,
): ModelCatalogEntry | undefined {
  const inst = findProviderInstance(providers, selection?.instanceId);
  if (!inst) return undefined;
  if (selection?.model) {
    const hit = inst.models.find((m) => m.slug === selection.model);
    if (hit) return hit;
  }
  return inst.models.find((m) => m.isDefault) ?? inst.models[0];
}

export function activeOptionDescriptors(
  providers: readonly ProviderInstance[],
  selection?: ModelSelection,
): OptionDescriptor[] {
  const inst = findProviderInstance(providers, selection?.instanceId);
  const model = findModelEntry(providers, selection);
  return [...(model?.optionDescriptors ?? []), ...(inst?.workspaceOptions ?? [])];
}

export function defaultModelSelectionFor(inst: ProviderInstance, modelSlug?: string): ModelSelection {
  const entry =
    (modelSlug ? inst.models.find((m) => m.slug === modelSlug) : undefined) ??
    inst.models.find((m) => m.isDefault) ??
    inst.models[0];
  const options: ModelSelection["options"] = {};
  for (const d of entry?.optionDescriptors ?? []) {
    if (d.currentValue) {
      options[d.id] = d.currentValue;
    }
  }
  return create(ModelSelectionSchema, {
    instanceId: inst.instanceId,
    model: entry?.slug ?? "default",
    options,
  });
}

export function cycleReasoningSelection(
  providers: readonly ProviderInstance[],
  selection: ModelSelection,
): ModelSelection {
  const descriptors = activeOptionDescriptors(providers, selection);
  const rDesc = descriptors.find(
    (d) => d.role === "reasoning" && d.type === OptionDescriptorType.SELECT && d.choices.length > 0,
  );
  if (!rDesc) return selection;
  const curVal =
    selection.options[rDesc.id]?.kind.case === "stringValue"
      ? selection.options[rDesc.id]?.kind.value
      : rDesc.currentValue?.kind.case === "stringValue"
        ? rDesc.currentValue.kind.value
        : rDesc.choices[0]?.id;
  const idx = rDesc.choices.findIndex((c) => c.id === curVal);
  const nextChoice = rDesc.choices[(idx + 1) % rDesc.choices.length];
  if (!nextChoice) return selection;
  return create(ModelSelectionSchema, {
    instanceId: selection.instanceId,
    model: selection.model,
    options: {
      ...selection.options,
      [rDesc.id]: create(OptionValueSchema, {
        kind: { case: "stringValue", value: nextChoice.id },
      }),
    },
  });
}

function upsertBy<T>(list: readonly T[], item: T, key: (x: T) => string): T[] {
  const k = key(item);
  const idx = list.findIndex((x) => key(x) === k);
  if (idx < 0) return [...list, item];
  const next = [...list];
  next[idx] = item;
  return next;
}

/** Pure immutable reducer folding a live SessionEvent onto a SessionProjection. */
export function applySessionEvent(
  prev: SessionProjection | null | undefined,
  ev: SessionEvent,
): SessionProjection {
  const base =
    prev ??
    create(SessionProjectionSchema, {
      session: ev.session,
      runs: [],
      items: [],
      requests: [],
      checkpoints: [],
      providerSessions: [],
      backgroundTasks: [],
    });

  const prevLastSeq = base.session?.progress?.lastEventSeq ?? 0n;
  let session = ev.session ?? base.session;
  if (session) {
    const curSeq = session.progress?.lastEventSeq ?? 0n;
    const maxSeq = [prevLastSeq, curSeq, ev.seq].reduce((a, b) => (a > b ? a : b), 0n);
    if (maxSeq !== curSeq) {
      session = {
        ...session,
        progress: session.progress
          ? { ...session.progress, lastEventSeq: maxSeq }
          : ({ $typeName: "hermesh.session.v1.SessionProgress", turnCount: 0, lastEventSeq: maxSeq, phase: "", currentTool: "", lastMessageExcerpt: "" } as NonNullable<Session["progress"]>),
      };
    }
  }
  let runs = base.runs;
  if (ev.run) {
    runs = upsertBy(runs, ev.run, (r) => r.runId);
  }
  let items: TurnItem[] = [...base.items];
  if (ev.type === "item.delta" && ev.targetItemId && ev.textDelta && !ev.item) {
    if (ev.seq === 0n || ev.seq !== prevLastSeq) {
      const idx = items.findIndex((it) => it.itemId === ev.targetItemId);
      if (idx >= 0) {
        const cur = items[idx]!;
        items[idx] = { ...cur, text: cur.text + ev.textDelta, streaming: true };
      }
    }
  } else if (ev.item) {
    items = upsertBy(items, ev.item, (it) => it.itemId).sort((a, b) => Number(a.seq - b.seq));
  }
  let backgroundTasks = base.backgroundTasks;
  if (ev.backgroundTask) {
    backgroundTasks = upsertBy(backgroundTasks, ev.backgroundTask, (t) => t.taskId);
  }
  if (ev.type === "session.rolled_back" && ev.checkpoint) {
    const maxOrd = ev.checkpoint.runOrdinal;
    const removedRunIds = new Set(
      runs.filter((r) => r.ordinal > maxOrd && r.status !== RunStatus.QUEUED).map((r) => r.runId),
    );
    runs = runs.map((r) => (removedRunIds.has(r.runId) ? { ...r, status: RunStatus.ROLLED_BACK } : r));
    items = items.filter((it) => !removedRunIds.has(it.runId));
    backgroundTasks = backgroundTasks.filter((bt) => !removedRunIds.has(bt.runId));
  }
  let requests = base.requests;
  if (ev.request) {
    requests = upsertBy(requests, ev.request, (r) => r.requestId);
  }
  let checkpoints = base.checkpoints;
  if (ev.checkpoint) {
    checkpoints = upsertBy(checkpoints, ev.checkpoint, (c) => c.checkpointId);
  }

  return create(SessionProjectionSchema, {
    session,
    runs,
    items,
    requests,
    checkpoints,
    providerSessions: base.providerSessions,
    backgroundTasks,
  });
}

export function toggleInteractionMode(mode: InteractionMode): InteractionMode {
  return mode === InteractionMode.PLAN ? InteractionMode.CODE : InteractionMode.PLAN;
}
