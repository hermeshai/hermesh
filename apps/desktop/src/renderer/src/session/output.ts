import { useEffect, useState } from "react";
import type { ToolResult } from "@hermesh/proto/session";
import { local } from "../api";

/** Whole outputs read lately, so a row opened again shows at once. */
const read = new Map<string, string>();
const readAtMost = 20;

/** Whether a result's event holds only part of its output: a preview, or its first 16 KiB. */
export function partialOutput(r: ToolResult): boolean {
  return r.preview || r.size > BigInt(new TextEncoder().encode(r.summary).length);
}

/**
 * A tool's output as its row shows it: what its event holds, and once the
 * row is open the whole output (ReadToolOutput, docs/state-sync.md §3.5),
 * shown as it arrives.
 */
export function useToolOutput(sessionId: string, r: ToolResult | undefined, seq: bigint | undefined, open: boolean): { text: string; reading: boolean; failed: boolean } {
  const key = `${sessionId}/${r?.callId}`;
  const partial = r !== undefined && partialOutput(r);
  const [got, setGot] = useState<{ key: string; text: string; done: boolean; failed: boolean }>(() => ({ key, text: read.get(key) ?? "", done: read.has(key), failed: false }));
  const current = got.key === key ? got : { key, text: read.get(key) ?? "", done: read.has(key), failed: false };
  const want = open && partial && !current.done && !current.failed;
  useEffect(() => {
    if (!want || !r) return;
    const stop = new AbortController();
    (async () => {
      const decoder = new TextDecoder();
      let text = "";
      try {
        for await (const c of local.readToolOutput({ sessionId, callId: r.callId, seq: seq ?? 0n }, { signal: stop.signal })) {
          text += decoder.decode(c.data, { stream: true });
          setGot({ key, text, done: false, failed: false });
        }
        text += decoder.decode();
        read.set(key, text);
        if (read.size > readAtMost) read.delete(read.keys().next().value!);
        setGot({ key, text, done: true, failed: false });
      } catch {
        if (!stop.signal.aborted) setGot({ key, text: "", done: false, failed: true });
      }
    })();
    return () => stop.abort();
  }, [want, key]);
  if (!r) return { text: "", reading: false, failed: false };
  if (!partial) return { text: r.summary, reading: false, failed: false };
  return { text: current.text !== "" ? current.text : r.summary, reading: want, failed: current.failed };
}
