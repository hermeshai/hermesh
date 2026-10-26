import { FailureKind, SessionStatus, type Failure, type Session } from "@hermesh/proto/session";
import { hourMinute, isYesterday, monthDay, pad2, sameDay, weekday, withinWeek } from "./calendar";
import { t } from "./i18n";

/** s without the spaces and tabs at its ends (line breaks stay). */
export function trimWhitespace(s: string): string {
  return s.replace(/^[\t\p{Zs}]+|[\t\p{Zs}]+$/gu, "");
}

/**
 * When something last changed, in the list: "15:42" today, "Yesterday",
 * a weekday within the week, else the date.
 */
export function shortTime(d: Date, now: Date): string {
  if (sameDay(d, now)) return hourMinute(d);
  if (isYesterday(d, now)) return t("time.yesterday");
  if (withinWeek(d, now)) return weekday(d);
  return monthDay(d);
}

/**
 * Whole seconds from one time to another, as wall-clock seconds passed:
 * elapsed clocks counted this way all change together on each second
 * boundary, whatever the milliseconds of their start (see useNow).
 */
export function elapsedSeconds(from: Date, now: Date): number {
  return Math.floor(now.getTime() / 1000) - Math.floor(from.getTime() / 1000);
}

/** How long something has been running (seconds): "4:12", "1:02:03". */
export function clockText(seconds: number): string {
  const s = Math.max(0, Math.trunc(seconds));
  const m = Math.trunc(s / 60);
  return m >= 60 ? `${Math.trunc(m / 60)}:${pad2(m % 60)}:${pad2(s % 60)}` : `${m}:${pad2(s % 60)}`;
}

/** "just now", "12 min ago", "3 hr ago", "Yesterday 14:20", "Oct 15". */
export function agoText(d: Date, now: Date): string {
  const s = (now.getTime() - d.getTime()) / 1000;
  if (s < 60) return t("time.justNow");
  if (s < 3600) return t("time.minutesAgo", { count: Math.trunc(s / 60) });
  if (sameDay(d, now)) return t("time.hoursAgo", { count: Math.trunc(s / 3600) });
  if (isYesterday(d, now)) return `${t("time.yesterday")} ${hourMinute(d)}`;
  return monthDay(d);
}

/** The session list's time, short: "just now", "12 min ago", "3 hr ago" today, then "Yesterday", a weekday, or the date. */
export function listTime(d: Date, now: Date): string {
  return sameDay(d, now) ? agoText(d, now) : shortTime(d, now);
}

/** 412K, 1.8M. */
export function tokensText(n: number | bigint): string {
  const v = Number(n);
  if (v >= 1_000_000) {
    // As printf's "%.1f": an exact tie (x.25) rounds to even, where toFixed rounds it up.
    return v % 1_000_000 === 250_000 ? `${Math.trunc(v / 1_000_000)}.2M` : `${(v / 1e6).toFixed(1)}M`;
  }
  return v >= 1000 ? `${Math.trunc(v / 1000)}K` : `${v}`;
}

/** "22 min left" until a question expires. */
export function minutesLeft(until: Date, now: Date): string {
  return t("time.minutesLeft", { count: Math.max(1, Math.ceil((until.getTime() - now.getTime()) / 60_000)) });
}

export function statusTitle(s: SessionStatus): string {
  switch (s) {
    case SessionStatus.QUEUED:
      return t("status.queued");
    case SessionStatus.PREPARING:
      return t("status.preparing");
    case SessionStatus.RUNNING:
      return t("status.running");
    case SessionStatus.IDLE:
      return t("status.idle");
    case SessionStatus.ARCHIVED:
      return t("status.archived");
    case SessionStatus.CRASHED:
      return t("status.crashed");
    case SessionStatus.WAITING_INPUT:
      return t("status.waitingInput");
    default:
      return t("status.unknown");
  }
}

export function failureTitle(k: FailureKind): string {
  switch (k) {
    case FailureKind.AUTH:
      return t("failure.auth");
    case FailureKind.QUOTA:
      return t("failure.quota");
    case FailureKind.RATE_LIMIT:
      return t("failure.rateLimit");
    case FailureKind.PROVIDER_ERROR:
      return t("failure.providerError");
    case FailureKind.NETWORK:
      return t("failure.network");
    case FailureKind.CONTEXT_OVERFLOW:
      return t("failure.contextOverflow");
    case FailureKind.MODEL_UNAVAILABLE:
      return t("failure.modelUnavailable");
    case FailureKind.INVALID_REQUEST:
      return t("failure.invalidRequest");
    case FailureKind.REFUSED:
      return t("failure.refused");
    case FailureKind.LIMIT_REACHED:
      return t("failure.limitReached");
    case FailureKind.RESUME_REJECTED:
      return t("failure.resumeRejected");
    case FailureKind.AGENT_CRASHED:
      return t("failure.agentCrashed");
    case FailureKind.STALLED:
      return t("failure.stalled");
    case FailureKind.TIMEOUT:
      return t("failure.timeout");
    case FailureKind.START_FAILED:
      return t("failure.startFailed");
    default:
      return t("failure.other");
  }
}

/** What to do about a failure; the daemon's hint when there is no advice of our own. */
export function failureAdvice(f: Failure): string {
  switch (f.kind) {
    case FailureKind.AUTH:
      return t("advice.auth");
    case FailureKind.QUOTA:
      return t("advice.quota");
    case FailureKind.RATE_LIMIT:
    case FailureKind.PROVIDER_ERROR:
    case FailureKind.NETWORK:
      return t("advice.transient");
    case FailureKind.CONTEXT_OVERFLOW:
    case FailureKind.RESUME_REJECTED:
      return t("advice.newContext");
    case FailureKind.MODEL_UNAVAILABLE:
      return t("advice.modelUnavailable");
    case FailureKind.LIMIT_REACHED:
      return t("advice.limitReached");
    case FailureKind.AGENT_CRASHED:
    case FailureKind.STALLED:
      return t("advice.resume");
    case FailureKind.START_FAILED:
      return t("advice.startFailed");
    default:
      return f.hint;
  }
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * toolSummary shortens what a tool call shows: paths inside the session's
 * worktree (a directory named after the session) are made relative to it,
 * and a command run through `sh -lc '…'` is shown as the command.
 */
export function toolSummary(s: string, sessionId: string): string {
  const out = s.replace(new RegExp(String.raw`\S*/` + escapeRegExp(sessionId) + "/", "g"), "");
  return out.replace(/^(?:\/bin\/|\/usr\/bin\/)?(?:ba|z)?sh -l?c '(.*)'$/, "$1");
}

/**
 * What a session is doing now, in a few words: "Editing store_test.go",
 * "Running go test ./...", or its state.
 */
export function phaseText(s: Session): string {
  switch (s.status) {
    case SessionStatus.QUEUED:
      return t("status.queued");
    case SessionStatus.PREPARING:
      return t("phase.preparing");
    case SessionStatus.WAITING_INPUT:
      return t("status.waitingInput");
    case SessionStatus.RUNNING:
      break;
    default:
      return statusTitle(s.status);
  }
  const tool = s.progress?.currentTool ?? "";
  if (tool === "") return t("phase.thinking");
  // The tool and what it works on, split at the first ":"; empty parts are dropped.
  const name = tool.replace(/^:+/, "");
  const i = name.indexOf(":");
  const parts = (i < 0 ? [name] : [name.slice(0, i), name.slice(i + 1)]).filter((p) => p !== "").map(trimWhitespace);
  const summary = toolSummary(parts[1] ?? parts[0] ?? "", s.sessionId);
  switch (s.progress?.phase) {
    case "editing":
      return t("phase.editing", { target: summary });
    case "running_shell":
      return t("phase.running", { target: summary });
    default:
      return parts.length > 1 ? `${parts[0]} ${summary}` : summary;
  }
}
