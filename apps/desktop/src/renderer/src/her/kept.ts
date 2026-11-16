import { fromSnapshot, type SessionView } from "@hermesh/her-client";
import type { BotSummary, DeliveriesListResult, SessionSnapshot } from "@hermesh/her-protocol";
import type { GetHerConversationResponse } from "@hermesh/proto/local";

// What this Mac keeps of an environment's Her (GetHerConversation,
// docs/her-server.md §7): Her, and the end of Her's conversation as the page
// last followed it here, after the app restarts too. The page shows it until
// her answers, and while the machine is offline, read-only.

/** deliveries: the first page of the files Her sent, as deliveries/list last answered it here (docs/her-deliverables.md §4.4). */
export type Kept = { bot: BotSummary; session: string | null; view: SessionView | null; deliveries: DeliveriesListResult | null };

const decoder = new TextDecoder();

/** What hermeshd keeps, as the page shows it, or null for nothing. Her as last seen works at nothing now. */
export function keptFrom(r: Pick<GetHerConversationResponse, "bot" | "snapshot"> & { deliveries?: Uint8Array }): Kept | null {
  if (r.bot.length === 0) return null;
  const bot = JSON.parse(decoder.decode(r.bot)) as BotSummary;
  const snapshot = r.snapshot.length > 0 ? (JSON.parse(decoder.decode(r.snapshot)) as SessionSnapshot) : null;
  const deliveries = r.deliveries && r.deliveries.length > 0 ? (JSON.parse(decoder.decode(r.deliveries)) as DeliveriesListResult) : null;
  return { bot: { ...bot, typing: false, agents_working: 0 }, session: snapshot?.session.session_id ?? null, view: snapshot && fromSnapshot(snapshot), deliveries };
}
