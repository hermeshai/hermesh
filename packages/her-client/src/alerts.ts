import type { BotSummary, SessionState, SessionSummary } from "@hermesh/her-protocol";
import { lastText } from "./bots.ts";
import { displayTitle } from "./sessions.ts";

// What is worth telling the owner when they are not looking (docs/desktop/notification-design.md
// §4), worked out from a list row before and after a change: the one place the rule is written,
// and free of any window or notification system, so it can move into the service with the phone.

/** The fields of a session's list row the rule reads. */
export type AlertSession = Pick<SessionSummary, "session_id" | "archived" | "title" | "preview" | "no_project" | "cwd"> & { state?: SessionState };

export type Alert = {
  /** Unique among what is showing: a later alert with this id replaces the earlier one. */
  id: string;
  /** What it stacks with, and what a click opens (`targetOf`). */
  group: string;
} & (
  /** text is empty for files alone: images counts them, file names the first other one. */
  | { kind: "bot_message"; text: string; name: string; images?: number; file?: string }
  | { kind: "bot_approval"; text: string; name: string }
  | { kind: "bot_failed"; name: string }
  | { kind: "session_approval"; title: string }
  | { kind: "session_failed"; title: string }
  | { kind: "session_ended"; title: string }
);

/** What changed about what is showing: new alerts, and those that no longer hold. */
export type AlertChange = { alerts: Alert[]; clear: string[]; clearGroups: string[] };

export type AlertTarget = { kind: "bot" | "session"; id: string };

const botGroup = (id: string) => `bot:${id}`;
const sessionGroup = (id: string) => `session:${id}`;

/** What an alert's group opens. */
export function targetOf(group: string): AlertTarget | null {
  const at = group.indexOf(":");
  const kind = group.slice(0, at);
  const id = group.slice(at + 1);
  return (kind === "bot" || kind === "session") && id ? { kind, id } : null;
}

/** Tells messages of the same second apart (`ts` has whole seconds): each stays its own notification. */
function hash(text: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 0x01000193);
  return (h >>> 0).toString(36);
}

/** The group of everything about a bot that was deleted. */
export const deletedBotGroup = botGroup;
/** The group of everything about a session that was deleted. */
export const deletedSessionGroup = sessionGroup;

/**
 * A bot's row changing. `before` is undefined for a bot first seen now; the list's first arrival
 * says what already was and never comes here.
 */
export function botAlerts(before: BotSummary | undefined, after: BotSummary): AlertChange {
  const group = botGroup(after.id);
  const approval = `${group}:approval`;
  const failed = `${group}:failed`;
  const alerts: Alert[] = [];
  const clear: string[] = [];
  const clearGroups: string[] = [];

  // By the message itself, not the unread count: a read arriving with it would hide the rise.
  const last = after.last;
  const said = (l: BotSummary["last"]) => l && `${l.text}${l.file ?? ""}${l.images ? `:${l.images}` : ""}`;
  const fresh = last && (last.ts !== before?.last?.ts || said(last) !== said(before?.last));
  if (fresh && last.from === "bot") {
    const { text } = lastText(last);
    alerts.push({ kind: "bot_message", id: `${group}:${last.ts}:${hash(said(last)!)}`, group, name: after.name, text: text || last.text.trim(), ...(last.images ? { images: last.images } : {}), ...(last.file ? { file: last.file } : {}) });
  }
  if (after.waiting !== before?.waiting) {
    if (before?.waiting) clear.push(approval);
    if (after.waiting) alerts.push({ kind: "bot_approval", id: approval, group, name: after.name, text: after.waiting });
  }
  // A failure comes out of work. A conversation loading with an old failure in it is no news.
  if (after.failed && !before?.failed && before?.typing) alerts.push({ kind: "bot_failed", id: failed, group, name: after.name });
  if (before?.failed && !after.failed && after.typing) clear.push(failed);
  if ((before?.unread ?? 0) > 0 && !after.unread) clearGroups.push(group);

  return { alerts: after.muted ? [] : alerts, clear, clearGroups };
}

/** A session's row changing. Sessions have no unread count: what they show goes when it no longer holds. */
export function sessionAlerts(before: AlertSession | undefined, after: AlertSession): AlertChange {
  const group = sessionGroup(after.session_id);
  const approval = `${group}:approval`;
  const ended = `${group}:ended`;
  const alerts: Alert[] = [];
  const clear: string[] = [];
  const state = after.state;
  const was = before?.state;
  if (state !== was) {
    if (was === "awaiting_approval") clear.push(approval);
    if (state === "running" || state === "awaiting_approval") clear.push(ended);
    if (!after.archived) {
      const title = displayTitle(after);
      if (state === "awaiting_approval") alerts.push({ kind: "session_approval", id: approval, group, title });
      if (state === "failed" && (was === "running" || was === "awaiting_approval")) alerts.push({ kind: "session_failed", id: ended, group, title });
      if (state === "idle" && was === "running") alerts.push({ kind: "session_ended", id: ended, group, title });
    }
  }
  return { alerts, clear, clearGroups: [] };
}

/**
 * The number on the app's icon: what the owner has not seen from bots they did not mute (a bot's
 * approvals count in its unread), and the sessions waiting for them.
 */
export function badgeCount(bots: Iterable<BotSummary>, sessions: Iterable<AlertSession>): number {
  let count = 0;
  for (const b of bots) if (!b.muted) count += b.unread ?? 0;
  for (const s of sessions) if (s.state === "awaiting_approval" && !s.archived) count += 1;
  return count;
}
