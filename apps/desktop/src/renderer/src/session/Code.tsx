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
export const Code = memo(function Code({ text, lang }: { text: string; lang: string }) {
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
  if (!valid) return text;
  return <span className="hl">{result.tokens!.map((line, i) => <TokenLine key={i} line={line} newline={i > 0} />)}{text.slice(result.text.length)}</span>;
});

// Worker messages clone arrays. Compare the small visible token representation
// so completed lines do not rebuild thousands of React spans on every delta.
const TokenLine = memo(function TokenLine({ line, newline }: { line: ThemedToken[]; newline: boolean }) {
  return <span>{newline && "\n"}{line.map((token, j) => <span key={j} style={token.htmlStyle as CSSProperties}>{token.content}</span>)}</span>;
}, (a, b) => a.newline === b.newline && a.line.length === b.line.length && a.line.every((token, i) => token.content === b.line[i].content && JSON.stringify(token.htmlStyle) === JSON.stringify(b.line[i].htmlStyle)));
