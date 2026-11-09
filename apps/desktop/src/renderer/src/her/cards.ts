import type { SessionView } from "@hermesh/her-client";

// Secure cards (docs/her-secure-cards.md §3, §4): what her made for the owner to answer, as
// send_to_user's result carries it, how each stands, and the answer that goes back. Read by their
// fields until her-protocol's generated types have them. What the owner types goes into the
// answer and nowhere else.

/** A console call the card makes when the owner agrees, exactly as the console sends it; a value goes where a string is "<value:FIELD_ID>". */
export type CardOp = { method: string; params: unknown };

/** A value the owner types into the card: a header's, for a connector. */
export type CardField = { id: string; label: string; secret: boolean };

/** A `connect` card's: a connector added by its address, or one signed in to again (`reconnect`, no ops). */
export type ConnectBody = { name: string; url: string; host: string; reconnect: boolean; sign_in: "oauth" | "none"; server_name?: string; fields: CardField[] };

export type Card = {
  id: string;
  kind: string;
  /** her's English words, for a kind the app doesn't know. */
  title: string;
  /** "her": nothing the owner said began the task that asks. */
  asked_by: "owner" | "her";
  expires_at: string;
  ops: CardOp[];
  body: ConnectBody;
};

export type CardState = "open" | "working" | "sign_in" | "retry" | "done" | "declined" | "failed" | "expired" | "replaced";

/** The answer to a card, by cards/answer. */
export type CardAnswer = { decision: "approve"; ops: CardOp[] } | { decision: "decline" };

/** How a card stands, with her's words: a refusal for `retry`, the outcome once it ended. */
export type CardStatus = { state: CardState; note?: string };

/** A card's state: as her said it while the conversation was open, as its history ended it, or as answered here; open until it expires. */
export type CardNow = CardStatus & { live: boolean };

const ended = new Set<string>(["done", "declined", "failed", "expired", "replaced"]);

/** The card a send_to_user result carries, or null for none. */
export function cardOf(raw: unknown): Card | null {
  const c = raw as Partial<Card> | null;
  return c && typeof c.id === "string" && typeof c.kind === "string" && typeof c.expires_at === "string" && Array.isArray(c.ops) && typeof c.body === "object" && c.body !== null ? (c as Card) : null;
}

/** A `connect` card's body, when it has what the card draws. */
export function connectBody(card: Card): ConnectBody | null {
  const b = card.body;
  return card.kind === "connect" && typeof b.name === "string" && typeof b.host === "string" && Array.isArray(b.fields) ? b : null;
}

/** The card's ops with each "<value:ID>" string filled with the owner's value for that field: a copy, the card's own left as they are. */
export function filled(ops: CardOp[], values: Record<string, string>): CardOp[] {
  const fill = (v: unknown): unknown => {
    if (typeof v === "string") {
      const id = /^<value:([^>]+)>$/.exec(v)?.[1];
      return id !== undefined && id in values ? values[id] : v;
    }
    if (Array.isArray(v)) return v.map(fill);
    if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, fill(x)]));
    return v;
  };
  return fill(ops) as CardOp[];
}

/** How the history ended a card (its `card_resolved` item), or null. */
function resolved(v: SessionView, id: string): CardStatus | null {
  for (const e of v.entries) {
    const r = e as { type: string; card_id?: unknown; outcome?: unknown; note?: unknown };
    if (r.type === "card_resolved" && r.card_id === id && typeof r.outcome === "string") return { state: r.outcome as CardState, ...(typeof r.note === "string" && { note: r.note }) };
  }
  return null;
}

/**
 * How a card stands now: as her last said it on this view, else as the history ended it, else as
 * her answered this device; open until then. One not ended by its expiry is expired.
 */
export function cardNow(card: Card, v: SessionView, answered: CardStatus | undefined, now: number): CardNow {
  const change = v.cards[card.id];
  const s: CardNow = change ? { ...(change as CardStatus), live: true } : { ...(resolved(v, card.id) ?? answered ?? { state: "open" }), live: false };
  return !ended.has(s.state) && Date.parse(card.expires_at) <= now ? { state: "expired", live: s.live } : s;
}

/** The tools her listed once a connector connected, from its note ("Notion connected: 14 tools (…)"). */
export function toolCount(note: string | undefined): number | undefined {
  const n = note && /(\d+) tools?\b/.exec(note);
  return n ? Number(n[1]) : undefined;
}

/**
 * What a card shows on this device: its form (with her's refusal after a retry, waiting while it
 * connects), the wait while the owner signs in here, or one line once it ended. `here`: this
 * device answered it; one another device answered shows it was handled there.
 */
export type CardShown =
  | { kind: "form"; note?: string; busy: boolean }
  | { kind: "signIn" }
  | { kind: "ended"; outcome: "done" | "declined" | "failed" | "expired" | "replaced" | "elsewhere"; tools?: number; note?: string };

export function cardShown(s: CardNow, here: boolean): CardShown {
  switch (s.state) {
    case "open":
      return { kind: "form", busy: false };
    case "retry":
      return { kind: "form", note: s.note, busy: false };
    case "working":
      return here ? { kind: "form", busy: true } : { kind: "ended", outcome: "elsewhere" };
    case "sign_in":
      return here ? { kind: "signIn" } : { kind: "ended", outcome: "elsewhere" };
    case "done":
      return { kind: "ended", outcome: "done", tools: toolCount(s.note) };
    case "declined":
    case "failed":
      // Ended while this device looked on, by another's answer.
      return s.live && !here ? { kind: "ended", outcome: "elsewhere" } : { kind: "ended", outcome: s.state, note: s.note };
    default:
      return { kind: "ended", outcome: s.state === "replaced" ? "replaced" : "expired" };
  }
}
