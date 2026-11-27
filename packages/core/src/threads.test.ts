import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { RuntimeMode } from "@hermesh/proto/identity";
import {
  BackgroundTaskKind,
  BackgroundTaskSchema,
  BackgroundTaskStatus,
  CheckpointSchema,
  InteractionMode,
  ModelCatalogEntrySchema,
  OptionChoiceSchema,
  OptionDescriptorSchema,
  OptionDescriptorType,
  OptionValueSchema,
  ProviderInstanceSchema,
  RunSchema,
  RunStatus,
  RuntimeRequestKind,
  RuntimeRequestSchema,
  RuntimeRequestStatus,
  SessionEventSchema,
  SessionProgressSchema,
  SessionProjectionSchema,
  SessionSchema,
  SessionStatus,
  TurnItemKind,
  TurnItemSchema,
} from "@hermesh/proto/session";
import {
  activeBackgroundTasksOf,
  applySessionEvent,
  backgroundTasksOf,
  clampRuntimeMode,
  composerPrimaryAction,
  cycleReasoningSelection,
  defaultModelSelectionFor,
  isRuntimeModeAllowed,
  pendingRequestOf,
  queuedRunsOf,
  toggleInteractionMode,
} from "./threads";

describe("unified session domain helpers & event reducer", () => {
  it("clamps runtime modes monotonically against cryptographic grant ceiling", () => {
    expect(clampRuntimeMode(RuntimeMode.UNSPECIFIED, RuntimeMode.UNSPECIFIED)).toBe(RuntimeMode.AUTO);
    expect(clampRuntimeMode(RuntimeMode.FULL_ACCESS, RuntimeMode.AUTO_EDITS)).toBe(RuntimeMode.AUTO_EDITS);
    expect(clampRuntimeMode(RuntimeMode.SUPERVISED, RuntimeMode.AUTO)).toBe(RuntimeMode.SUPERVISED);
    expect(isRuntimeModeAllowed(RuntimeMode.FULL_ACCESS, RuntimeMode.AUTO)).toBe(false);
    expect(isRuntimeModeAllowed(RuntimeMode.AUTO_EDITS, RuntimeMode.AUTO)).toBe(true);
  });

  it("derives adaptive composer primary actions across online, offline, steer, and stop states", () => {
    const idle = create(SessionSchema, { sessionId: "s1", status: SessionStatus.IDLE });
    const running = create(SessionSchema, { sessionId: "s1", status: SessionStatus.RUNNING });
    const waiting = create(SessionSchema, { sessionId: "s1", status: SessionStatus.WAITING_INPUT });

    expect(composerPrimaryAction(undefined, false, true, true)).toBe("queue_offline");
    expect(composerPrimaryAction(undefined, true, true, true)).toBe("start");
    expect(composerPrimaryAction(idle, true, true, true)).toBe("send");
    expect(composerPrimaryAction(running, true, false, true)).toBe("stop");
    expect(composerPrimaryAction(running, true, true, true)).toBe("steer");
    expect(composerPrimaryAction(running, true, true, false)).toBe("queue");
    expect(composerPrimaryAction(waiting, true, true, true)).toBe("send");
  });

  it("builds default selection and cycles reasoning effort descriptors", () => {
    const inst = create(ProviderInstanceSchema, {
      instanceId: "codex",
      driver: "codex",
      enabled: true,
      installed: true,
      models: [
        create(ModelCatalogEntrySchema, {
          slug: "gpt-5.4",
          name: "GPT-5.4",
          isDefault: true,
          optionDescriptors: [
            create(OptionDescriptorSchema, {
              id: "reasoningEffort",
              role: "reasoning",
              type: OptionDescriptorType.SELECT,
              currentValue: create(OptionValueSchema, { kind: { case: "stringValue", value: "medium" } }),
              choices: [
                create(OptionChoiceSchema, { id: "low", label: "Low" }),
                create(OptionChoiceSchema, { id: "medium", label: "Medium", isDefault: true }),
                create(OptionChoiceSchema, { id: "xhigh", label: "Extra High" }),
              ],
            }),
          ],
        }),
      ],
    });
    const sel0 = defaultModelSelectionFor(inst);
    expect(sel0.model).toBe("gpt-5.4");
    expect(sel0.options.reasoningEffort?.kind.value).toBe("medium");

    const sel1 = cycleReasoningSelection([inst], sel0);
    expect(sel1.options.reasoningEffort?.kind.value).toBe("xhigh");
    const sel2 = cycleReasoningSelection([inst], sel1);
    expect(sel2.options.reasoningEffort?.kind.value).toBe("low");
    expect(toggleInteractionMode(InteractionMode.CODE)).toBe(InteractionMode.PLAN);
  });

  it("reduces streaming item updates, pending requests, queued runs, and checkpoint rollbacks", () => {
    let proj = create(SessionProjectionSchema, {
      session: create(SessionSchema, { sessionId: "s_1", status: SessionStatus.RUNNING }),
      runs: [
        create(RunSchema, { runId: "r1", ordinal: 1, status: RunStatus.COMPLETED }),
        create(RunSchema, { runId: "r2", ordinal: 2, status: RunStatus.COMPLETED }),
        create(RunSchema, { runId: "q2", status: RunStatus.QUEUED, queueOrder: 2000 }),
        create(RunSchema, { runId: "q1", status: RunStatus.QUEUED, queueOrder: 1000 }),
      ],
      items: [
        create(TurnItemSchema, { itemId: "i1", runId: "r1", seq: 1n, kind: TurnItemKind.ASSISTANT_MESSAGE, text: "Step 1" }),
        create(TurnItemSchema, { itemId: "i2", runId: "r2", seq: 2n, kind: TurnItemKind.ASSISTANT_MESSAGE, text: "Step 2" }),
      ],
    });

    expect(queuedRunsOf(proj).map((r) => r.runId)).toEqual(["q1", "q2"]);
    // A queued run whose message is in the conversation (kept on this device for an offline machine) is its bubble.
    const kept = create(SessionProjectionSchema, {
      ...proj,
      items: [...proj.items, create(TurnItemSchema, { itemId: "u", runId: "q1", seq: 3n, kind: TurnItemKind.USER_MESSAGE, text: "hi" })],
    });
    expect(queuedRunsOf(kept).map((r) => r.runId)).toEqual(["q2"]);

    const req = create(RuntimeRequestSchema, {
      requestId: "req_1",
      sessionId: "s_1",
      runId: "r2",
      kind: RuntimeRequestKind.TOOL_APPROVAL,
      status: RuntimeRequestStatus.PENDING,
    });
    proj = applySessionEvent(
      proj,
      create(SessionEventSchema, {
        sessionId: "s_1",
        seq: 5n,
        type: "request.created",
        session: create(SessionSchema, { sessionId: "s_1", status: SessionStatus.WAITING_INPUT, pendingRequestId: "req_1" }),
        request: req,
      }),
    );
    expect(pendingRequestOf(proj)?.requestId).toBe("req_1");

    proj = applySessionEvent(
      proj,
      create(SessionEventSchema, {
        sessionId: "s_1",
        seq: 6n,
        type: "session.rolled_back",
        session: create(SessionSchema, { sessionId: "s_1", status: SessionStatus.IDLE }),
        checkpoint: create(CheckpointSchema, { checkpointId: "cp_1", runOrdinal: 1 }),
      }),
    );
    expect(proj.items.map((i) => i.itemId)).toEqual(["i1"]);
    expect(queuedRunsOf(proj).map((r) => r.runId)).toEqual(["q1", "q2"]);

    const bumped = create(SessionProjectionSchema, {
      session: create(SessionSchema, {
        sessionId: "s_1",
        progress: create(SessionProgressSchema, { lastEventSeq: 20n }),
      }),
      runs: [],
      items: [],
    });
    const caughtUp = applySessionEvent(
      bumped,
      create(SessionEventSchema, {
        sessionId: "s_1",
        seq: 12n,
        type: "item.updated",
        item: create(TurnItemSchema, { itemId: "i_late", runId: "r1", seq: 11n, kind: TurnItemKind.ASSISTANT_MESSAGE, text: "Recovered" }),
      }),
    );
    expect(caughtUp.session?.progress?.lastEventSeq).toBe(20n);
    expect(caughtUp.items.map((i) => i.text)).toEqual(["Recovered"]);

    // Background task updates fold cleanly, sort awaited before daemons, and prune on rollback.
    let withBg = applySessionEvent(
      caughtUp,
      create(SessionEventSchema, {
        sessionId: "s_1",
        seq: 21n,
        type: "background_task.updated",
        backgroundTask: create(BackgroundTaskSchema, {
          taskId: "bg_daemon",
          runId: "r2",
          kind: BackgroundTaskKind.DAEMON,
          status: BackgroundTaskStatus.RUNNING,
          awaited: false,
        }),
      }),
    );
    withBg = applySessionEvent(
      withBg,
      create(SessionEventSchema, {
        sessionId: "s_1",
        seq: 22n,
        type: "background_task.updated",
        backgroundTask: create(BackgroundTaskSchema, {
          taskId: "bg_cmd",
          runId: "r1",
          kind: BackgroundTaskKind.COMMAND,
          status: BackgroundTaskStatus.RUNNING,
          awaited: true,
        }),
      }),
    );
    expect(activeBackgroundTasksOf(withBg).map((t) => t.taskId)).toEqual(["bg_cmd", "bg_daemon"]);
    expect(backgroundTasksOf(withBg).map((t) => t.taskId)).toEqual(["bg_cmd", "bg_daemon"]);
  });
});
