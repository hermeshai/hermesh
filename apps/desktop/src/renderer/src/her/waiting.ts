import type { HerWaiting } from "@hermesh/proto/local";

// Messages written to an assistant while its machine was offline: this device
// keeps them and delivers them once the machine is back (HerSend,
// ListHerWaiting, docs/her-server.md §5.5). The conversation shows them after
// what was said, as waiting.

/** A message waiting to be delivered, as the conversation shows it. */
export type WaitingItem = { kind: "waiting"; key: string; id: string; text: string; files: string[]; ts: number; error: string };

type Waiting = Pick<HerWaiting, "id" | "botId" | "text" | "localPaths" | "createdAt" | "error">;

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

/**
 * An assistant's waiting messages, oldest first, but those its conversation
 * shows already (`said`: the owner's messages there): delivered while the
 * list was asked for again. A delivered message has the same words, sent at
 * or after it was written.
 */
export function waitingFor(list: Waiting[], botId: string, said: { text: string; ts: number }[]): WaitingItem[] {
  const out: WaitingItem[] = [];
  const left = said.map((s) => ({ text: splitAttached(s.text).text.trim(), ts: s.ts }));
  for (const w of [...list].filter((w) => w.botId === botId).sort((a, b) => millis(a.createdAt) - millis(b.createdAt))) {
    const ts = millis(w.createdAt);
    const i = left.findIndex((s) => s.text === w.text.trim() && s.ts >= ts - 60_000);
    if (i >= 0) {
      left.splice(i, 1);
      continue;
    }
    out.push({ kind: "waiting", key: `waiting:${w.id}`, id: w.id, text: w.text, files: w.localPaths.map(fileName), ts, error: w.error });
  }
  return out;
}
