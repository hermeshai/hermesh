import type { ReplyQuote } from "@hermesh/her-client";
import type { HerWaiting } from "@hermesh/proto/local";

// Messages written to an assistant while its machine was offline: this device
// keeps them and delivers them once the machine is back (HerSend,
// ListHerWaiting, docs/her-server.md §5.5). The conversation shows them after
// what was said, as waiting.

/** A message waiting to be delivered, as the conversation shows it. */
export type WaitingItem = { kind: "waiting"; key: string; id: string; text: string; files: string[]; ts: number; error: string; reply?: ReplyQuote };

type Waiting = Pick<HerWaiting, "id" | "botId" | "text" | "localPaths" | "createdAt" | "error"> & { reply?: ReplyQuote };

const millis = (ts: Waiting["createdAt"]) => (ts ? Number(ts.seconds) * 1000 + Math.floor(ts.nanos / 1e6) : 0);

/** A path's last part, a Mac's or a PC's. */
const fileName = (path: string) => path.split(/[\\/]/).filter(Boolean).pop() ?? path;

const attachedMark = "Attached files:";

/**
 * A message hermeshd delivered with files (HerSend) names them after its
 * words, "Attached files:" and a line "- <path>" for each: its words, and
 * the files' paths on the machine.
 */
export function splitAttached(text: string): { text: string; files: string[] } {
  const at = text.indexOf(`${attachedMark}\n- `);
  if (at < 0 || (at > 0 && !text.slice(0, at).endsWith("\n\n"))) return { text, files: [] };
  return { text: text.slice(0, at).trimEnd(), files: text.slice(at + attachedMark.length).split("\n- ").slice(1).filter(Boolean) };
}

/** A message this app is sending (HerSend has not answered), or couldn't: `refused` says why (docs/her-sending.md §5.2). */
export type SendingItem = {
  kind: "sending";
  key: string;
  id: string;
  text: string;
  files: string[];
  images: { mime_type: string; data: string }[];
  ts: number;
  refused: string | null;
  /** What it replies to (docs/her-reply.md). */
  reply?: ReplyQuote;
};

/** What the app sent and has not heard of yet: its id (the outbox's and her's), words, files by name, pictures. */
export type Sending = Omit<SendingItem, "kind" | "key">;

/**
 * What an assistant was sent that its conversation (`said`: the owner's messages there, its queue among them, by
 * their ids) doesn't show yet, as rows after it: the messages kept in the outbox, oldest first, then those this app
 * is sending, or couldn't. A message shows once, in the conversation, the outbox or here, and keeps its id as its
 * row's key from one to the next. It is in the conversation when a message there has its id; one her gave an id of
 * her own (an older her) when a message there has the same words, sent at or after it was written (less a minute).
 */
export function outboxFor(list: Waiting[], botId: string, said: { key: string; text: string; ts: number }[], sending: Sending[] = []): (WaitingItem | SendingItem)[] {
  const ids = new Set(said.map((s) => s.key));
  const left = said.filter((s) => !s.key.startsWith("hw_")).map((s) => ({ text: splitAttached(s.text).text.trim(), ts: s.ts }));
  const shown = (id: string, text: string, ts: number) => {
    if (ids.has(id)) return true;
    const i = left.findIndex((s) => s.text === text.trim() && s.ts >= ts - 60_000);
    if (i < 0) return false;
    left.splice(i, 1);
    return true;
  };
  const out: (WaitingItem | SendingItem)[] = [];
  const kept = [...list].filter((w) => w.botId === botId).sort((a, b) => millis(a.createdAt) - millis(b.createdAt));
  for (const w of kept) {
    const ts = millis(w.createdAt);
    if (!shown(w.id, w.text, ts)) out.push({ kind: "waiting", key: w.id, id: w.id, text: w.text, files: w.localPaths.map(fileName), ts, error: w.error, ...(w.reply && { reply: { ref: w.reply.ref, own: w.reply.own, text: w.reply.text, partial: w.reply.partial } }) });
  }
  const inOutbox = new Set(kept.map((w) => w.id));
  for (const m of [...sending].sort((a, b) => a.ts - b.ts)) {
    if (!inOutbox.has(m.id) && !shown(m.id, m.text, m.ts)) out.push({ kind: "sending", key: m.id, ...m });
  }
  return out;
}

/**
 * The messages still being sent once the conversation shows `said` (docs/her-sending.md §5.2): one it shows goes, by
 * its id or her's own by its words (`outboxFor`), so that it is the conversation's from then on; and so does one her
 * took (`delivered`) that it doesn't show and won't (`lost`: a command such as /context, which her takes without it
 * joining the conversation, or the conversation opened afresh without it).
 */
export function stillSending<T extends Sending & { delivered?: boolean }>(sending: T[], said: { key: string; text: string; ts: number }[], lost: (m: T) => boolean = () => false): T[] {
  const unshown = new Set(outboxFor([], "", said, sending).map((r) => r.key));
  return sending.filter((m) => unshown.has(m.id) && !(m.delivered && lost(m)));
}
