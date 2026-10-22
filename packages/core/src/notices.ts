import { create } from "@bufbuild/protobuf";
import { PairingKind } from "@hermesh/proto/identity";
import type { PairingDetails } from "@hermesh/proto/local";
import { FailureSchema, SessionStatus, type Session } from "@hermesh/proto/session";
import { codingAgentTitle } from "./agents";
import { t } from "./i18n";
import { displayTitle, isFailed, pendingQuestion } from "./sessions";
import { failureAdvice, failureTitle } from "./text";
import { isInterruption } from "./transcript";

// What a session's change reaches the owner as: a question, a failure, a
// finished turn. Kept apart from the notification center so it can be checked.

/** A session as it was at the last look. */
export interface NoticeSnapshot {
  status: SessionStatus;
  turns: number;
  question: string;
  failed: boolean;
}

export function noticeSnapshot(s: Session): NoticeSnapshot {
  return {
    status: s.status,
    turns: s.progress?.turnCount ?? 0,
    question: pendingQuestion(s)?.questionId ?? "",
    failed: isFailed(s),
  };
}

/** Each session's snapshot by its id; the first of a repeated id wins. */
export function noticeSnapshots(sessions: Session[]): Map<string, NoticeSnapshot> {
  const out = new Map<string, NoticeSnapshot>();
  for (const s of sessions) if (!out.has(s.sessionId)) out.set(s.sessionId, noticeSnapshot(s));
  return out;
}

export type NoticeKind = "question" | "failed" | "turnDone";

export interface NoticeItem {
  kind: NoticeKind;
  sessionId: string;
  title: string;
  body: string;
}

/**
 * The notices since the last look (previous: each session as it was).
 * A session seen for the first time only tells about a question.
 */
export function noticeDiff(
  previous: ReadonlyMap<string, NoticeSnapshot>,
  sessions: Session[],
  grant: (s: Session) => string,
): NoticeItem[] {
  const out: NoticeItem[] = [];
  for (const s of sessions) {
    const before = previous.get(s.sessionId);
    const q = pendingQuestion(s);
    if (q && q.questionId !== before?.question) {
      const text = q.items[0]?.text ?? "";
      out.push({
        kind: "question",
        sessionId: s.sessionId,
        title: t("notice.questionTitle", { grant: grant(s) }),
        body: t("notice.questionBody", { title: displayTitle(s), text }),
      });
    }
    if (!before) continue;
    const turns = s.progress?.turnCount ?? 0;
    if (isFailed(s) && !before.failed) {
      const f = s.progress?.lastFailure ?? create(FailureSchema);
      out.push({
        kind: "failed",
        sessionId: s.sessionId,
        title: t("notice.failedTitle", { title: displayTitle(s), failure: failureTitle(f.kind) }),
        body: t("notice.failedBody", { grant: grant(s), agent: codingAgentTitle(s.agent), project: s.projectId, advice: failureAdvice(f) }).trimEnd(),
      });
    } else if (
      s.status === SessionStatus.IDLE &&
      !isFailed(s) &&
      !isInterruption(s.progress?.lastFailure) &&
      turns > before.turns &&
      before.status !== SessionStatus.IDLE
    ) {
      const d = s.progress?.diffStat;
      const files = d?.filesChanged ?? 0;
      out.push({
        kind: "turnDone",
        sessionId: s.sessionId,
        title: t("notice.turnDoneTitle", { title: displayTitle(s), turn: turns }),
        body:
          files === 0
            ? t("notice.noChanges")
            : t("notice.changes", { count: files, insertions: d?.insertions ?? 0, deletions: d?.deletions ?? 0 }),
      });
    }
  }
  return out;
}

/** The notices the owner can switch off in settings, by their settings key. */
export interface NoticePrefs {
  notifyQuestion: boolean;
  notifyFailed: boolean;
  notifyTurnDone: boolean;
}

/**
 * The notices the owner wants. A preference not set yet counts as on for
 * questions and failures, off for finished turns.
 */
export function filterNotices(items: NoticeItem[], prefs: Partial<NoticePrefs>): NoticeItem[] {
  return items.filter((item) => {
    switch (item.kind) {
      case "question":
        return prefs.notifyQuestion ?? true;
      case "failed":
        return prefs.notifyFailed ?? true;
      case "turnDone":
        return prefs.notifyTurnDone ?? false;
    }
  });
}

/** A pending pairing request, as the menu bar and its notification name it. */
export function pairingTitle(p: PairingDetails): string {
  switch (p.kind) {
    case PairingKind.MACHINE:
      return t("pairing.machine", { label: p.label });
    case PairingKind.DEVICE:
      return t("pairing.device", { label: p.label });
    default:
      return t("pairing.client", { label: p.label });
  }
}

/** The code the owner checks against the requester's (its pairing code when there is no verification code). */
export function pairingCheckCode(p: PairingDetails): string {
  return p.verificationCode || p.code;
}

/** The notification of a pending pairing request. */
export function pairingNotice(p: PairingDetails): { title: string; body: string } {
  return { title: pairingTitle(p), body: t("pairing.body", { code: pairingCheckCode(p) }) };
}
