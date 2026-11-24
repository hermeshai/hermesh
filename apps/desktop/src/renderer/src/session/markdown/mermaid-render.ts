import mermaid from "mermaid";
import { MAX_DIAGRAM_LENGTH, prepareDiagram, validDiagram } from "./diagram-source";

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
      htmlLabels: false, fontFamily: '"Google Sans Flex", "Google Sans", system-ui, "PingFang SC", sans-serif',
      themeVariables: {
        darkMode: dark, background: dark ? "#161619" : "#FFFFFF",
        primaryColor: dark ? "#242428" : "#F4F5F7", primaryTextColor: dark ? "#DEDEDB" : "#202127",
        primaryBorderColor: dark ? "#8AB4F8" : "#285FB8", lineColor: dark ? "#999993" : "#60636D",
        secondaryColor: dark ? "#2A2A2D" : "#EBEDF2", tertiaryColor: dark ? "#303034" : "#E3E7EE",
        fontSize: "14px", edgeLabelBackground: dark ? "#161619" : "#FFFFFF",
      },
      flowchart: { htmlLabels: false, useMaxWidth: false, padding: 16 },
      sequence: { useMaxWidth: false },
    });
    const { svg } = await mermaid.render(`hermesh-diagram-${++serial}`, prepareDiagram(source));
    cache.set(key, svg);
    while (cache.size > 30) cache.delete(cache.keys().next().value!);
    return svg;
  });
  queue = result.catch(() => {});
  return result;
}
