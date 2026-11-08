import mermaid from "mermaid";
import { MAX_DIAGRAM_LENGTH, validDiagram } from "./diagram-source";

const cache = new Map<string, string>();
let queue: Promise<unknown> = Promise.resolve();
let serial = 0;

export function renderDiagram(source: string, dark: boolean): Promise<string> {
  if (!validDiagram(source)) return Promise.reject(new Error("unsupported diagram"));
  const key = `${dark}:${source}`;
  const found = cache.get(key);
  if (found) return Promise.resolve(found);
  const result = queue.then(async () => {
    mermaid.initialize({
      startOnLoad: false, securityLevel: "strict", suppressErrorRendering: true,
      theme: "base", maxTextSize: MAX_DIAGRAM_LENGTH, maxEdges: 400,
      htmlLabels: false, fontFamily: 'system-ui, "PingFang SC", sans-serif',
      themeVariables: {
        darkMode: dark, background: dark ? "#18181B" : "#FFFFFF",
        primaryColor: dark ? "#232833" : "#F6F7FA", primaryTextColor: dark ? "#E8ECF4" : "#0B1633",
        primaryBorderColor: dark ? "#7AB0FF" : "#1A5CE6", lineColor: dark ? "#9AA3B5" : "#626775",
        secondaryColor: dark ? "#29292D" : "#F4F4F7", tertiaryColor: dark ? "#323237" : "#EBEBF0",
        fontSize: "14px", edgeLabelBackground: dark ? "#18181B" : "#FFFFFF",
      },
      flowchart: { htmlLabels: false, useMaxWidth: false, padding: 16 },
      sequence: { useMaxWidth: false },
    });
    const { svg } = await mermaid.render(`hermesh-diagram-${++serial}`, source);
    cache.set(key, svg);
    while (cache.size > 30) cache.delete(cache.keys().next().value!);
    return svg;
  });
  queue = result.catch(() => {});
  return result;
}
