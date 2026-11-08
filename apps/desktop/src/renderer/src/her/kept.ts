import { fromSnapshot, type SessionView } from "@hermesh/her-client";
import type { BotSummary, SessionSnapshot } from "@hermesh/her-protocol";
import type { GetHerConversationResponse } from "@hermesh/proto/local";

// What this Mac keeps of an environment's Her (GetHerConversation,
// docs/her-server.md §7): Her, and the end of Her's conversation as the page
// last followed it here, after the app restarts too. The page shows it until
// her answers, and while the machine is offline, read-only.

export type Kept = { bot: BotSummary; session: string | null; view: SessionView | null };

const decoder = new TextDecoder();

/** What hermeshd keeps, as the page shows it, or null for nothing. Her as last seen works at nothing now. */
export function keptFrom(r: Pick<GetHerConversationResponse, "bot" | "snapshot">): Kept | null {
  if (r.bot.length === 0) return null;
  const bot = JSON.parse(decoder.decode(r.bot)) as BotSummary;
  const snapshot = r.snapshot.length > 0 ? (JSON.parse(decoder.decode(r.snapshot)) as SessionSnapshot) : null;
  return { bot: { ...bot, typing: false, agents_working: 0 }, session: snapshot?.session.session_id ?? null, view: snapshot && fromSnapshot(snapshot) };
}
