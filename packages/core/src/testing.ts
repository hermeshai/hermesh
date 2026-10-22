// Sessions for the tests, around a fixed "now".

import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  QuestionSchema,
  SessionProgressSchema,
  SessionSchema,
  type Session,
  type SessionProgress,
  type SessionStatus,
} from "@hermesh/proto/session";

/** A Monday, 15:42 local time, so day buckets are stable. */
export const now = new Date(2026, 9, 19, 15, 42);

/** now, moved by seconds. */
export function at(seconds: number): Date {
  return new Date(now.getTime() + seconds * 1000);
}

/** A session last changed `updated` seconds from now (created a minute before, unless given). */
export function session(
  id: string,
  status: SessionStatus,
  o: {
    updated?: number;
    created?: number;
    by?: string;
    project?: string;
    agent?: string;
    machine?: string;
    title?: string;
  } = {},
): Session {
  const updated = o.updated ?? -60;
  return create(SessionSchema, {
    sessionId: id,
    status,
    updatedAt: timestampFromDate(at(updated)),
    createdAt: timestampFromDate(at(o.created ?? updated - 60)),
    createdBy: o.by ?? "c1",
    projectId: o.project ?? "app",
    agent: o.agent ?? "claude",
    machineId: o.machine ?? "m1",
    title: o.title ?? id,
  });
}

/** s's progress, made if it has none, to set fields on. */
export function progressOf(s: Session): SessionProgress {
  return (s.progress ??= create(SessionProgressSchema));
}

/** Gives s a pending question. */
export function ask(s: Session, id = "q1", text = "跳到哪里？"): void {
  progressOf(s).pendingQuestion = create(QuestionSchema, { questionId: id, items: [{ id: "i1", text }] });
}
