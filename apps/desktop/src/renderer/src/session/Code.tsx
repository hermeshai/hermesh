import { memo, useEffect, useId, useState, type CSSProperties } from "react";
import type { ThemedToken } from "shiki/core";

let worker: Worker | undefined;
let sequence = 0;
let watchdog: ReturnType<typeof setTimeout> | undefined;
function stopWorker() {
  clearTimeout(watchdog); watchdog = undefined;
  worker?.terminate(); worker = undefined;
  for (const done of pending.values()) done(null);
  pending.clear();
}
function watchWorker() {
  if (!watchdog) watchdog = setTimeout(stopWorker, 2500);
}
const pending = new Map<number, (tokens: ThemedToken[][] | null) => void>();
function engine() {
  if (worker) return worker;
  worker = new Worker(new URL("./markdown/highlight.worker.ts", import.meta.url), { type: "module" });
  worker.onmessage = ({ data }: MessageEvent<{ request: number; tokens: ThemedToken[][] | null }>) => {
    pending.get(data.request)?.(data.tokens);
    pending.delete(data.request);
    clearTimeout(watchdog); watchdog = undefined;
    if (pending.size) watchWorker();
  };
  worker.onerror = stopWorker;
  return worker;
}

/** Text is immediate; only the worker spends time on TextMate grammars. */
export const Code = memo(function Code({ text, lang, wipeFrom = Infinity }: { text: string; lang: string; wipeFrom?: number }) {
  const id = useId();
  const [result, setResult] = useState<{ text: string; lang: string; tokens: ThemedToken[][] | null }>();
  useEffect(() => {
    const request = ++sequence;
    const timer = setTimeout(() => {
      try {
        pending.set(request, (tokens) => setResult({ text, lang, tokens }));
        engine().postMessage({ request, id, text, lang });
        watchWorker();
      } catch { pending.delete(request); }
    }, 32);
    return () => { clearTimeout(timer); pending.delete(request); };
  }, [id, text, lang]);
  const valid = result?.lang === lang && text.startsWith(result.text) && result.tokens;
  if (!valid) return wipeFrom < Infinity ? <PlainWipeCode text={text} wipeFrom={wipeFrom} /> : text;
  const doneLines = result.tokens!.length;
  const tail = text.slice(result.text.length);
  return (
    <span className="hl">
      {result.tokens!.map((line, i) => <TokenLine key={i} line={line} newline={i > 0} wipe={i >= wipeFrom} />)}
      {tail && (doneLines >= wipeFrom ? <PlainWipeTail text={tail} /> : tail)}
    </span>
  );
});

function sameStyle(a: ThemedToken["htmlStyle"], b: ThemedToken["htmlStyle"]): boolean {
  if (a === b) return true;
  if (!a || !b || typeof a !== "object" || typeof b !== "object") return false;
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (let i = 0; i < ka.length; i++) {
    const k = ka[i]!;
    if (a[k] !== b[k]) return false;
  }
  return true;
}

export function PlainWipeCode({ text, wipeFrom }: { text: string; wipeFrom: number }) {
  const lines = text.split("\n");
  return (
    <>
      {lines.map((line, i) => (
        <span key={i}>
          {i > 0 && "\n"}
          {i >= wipeFrom ? <span className="md-wipe">{line}</span> : line}
        </span>
      ))}
    </>
  );
}

function PlainWipeTail({ text }: { text: string }) {
  const parts = text.split("\n");
  return (
    <>
      {parts.map((part, i) => (
        <span key={i}>
          {i > 0 && "\n"}
          {part ? <span className="md-wipe">{part}</span> : null}
        </span>
      ))}
    </>
  );
}

// Worker messages clone arrays. Compare the small visible token representation
// so completed lines do not rebuild thousands of React spans on every delta.
const TokenLine = memo(function TokenLine({ line, newline, wipe = false }: { line: ThemedToken[]; newline: boolean; wipe?: boolean }) {
  const body = line.map((token, j) => <span key={j} style={token.htmlStyle as CSSProperties}>{token.content}</span>);
  return <span>{newline && "\n"}{wipe ? <span className="md-wipe">{body}</span> : body}</span>;
}, (a, b) => a.wipe === b.wipe && a.newline === b.newline && a.line.length === b.line.length && a.line.every((token, i) => token.content === b.line[i].content && sameStyle(token.htmlStyle, b.line[i].htmlStyle)));
