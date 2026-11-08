import { createCodeHighlighter } from "./highlighter";
const highlight = createCodeHighlighter();
type Job = { request: number; id: string; text: string; lang: string };
const waiting = new Map<string, Job>();
let running = false;
self.onmessage = (event: MessageEvent<Job>) => { waiting.set(event.data.id, event.data); void drain(); };
async function drain() {
  if (running) return;
  running = true;
  while (waiting.size) {
    const job = waiting.values().next().value!;
    waiting.delete(job.id);
    try { self.postMessage({ request: job.request, tokens: await highlight(job.id, job.text, job.lang) }); }
    catch { self.postMessage({ request: job.request, tokens: null }); }
  }
  running = false;
}
