import mermaid from "mermaid";
import { MAX_DIAGRAM_LENGTH, prepareDiagram, validDiagram } from "./diagram-source";

const cache = new Map<string, string>();
let queue: Promise<unknown> = Promise.resolve();
let serial = 0;

export type DiagramTheme = "light" | "dark" | "black";

export function renderDiagram(source: string, mode: DiagramTheme | boolean): Promise<string> {
  if (!validDiagram(source)) return Promise.reject(new Error("unsupported diagram"));
  const theme: DiagramTheme = typeof mode === "boolean" ? (mode ? "dark" : "light") : mode;
  const dark = theme !== "light";
  const black = theme === "black";
  const key = `${theme}:${source}`;
  const found = cache.get(key);
  if (found) return Promise.resolve(found);
  const result = queue.then(async () => {
    mermaid.initialize({
      startOnLoad: false, securityLevel: "strict", suppressErrorRendering: true,
      theme: "base", maxTextSize: MAX_DIAGRAM_LENGTH, maxEdges: 400,
      htmlLabels: false, fontFamily: '"Google Sans Flex", "Google Sans", system-ui, "PingFang SC", sans-serif',
      themeVariables: {
        darkMode: dark, background: black ? "#0D0D11" : dark ? "#161619" : "#FFFFFF",
        primaryColor: black ? "#15151A" : dark ? "#242428" : "#F4F5F7", primaryTextColor: black ? "#FAFAFA" : dark ? "#DEDEDB" : "#202127",
        primaryBorderColor: dark ? "#8AB4F8" : "#285FB8", lineColor: black ? "#87888F" : dark ? "#999993" : "#60636D",
        secondaryColor: black ? "#131317" : dark ? "#2A2A2D" : "#EBEDF2", tertiaryColor: black ? "#1F1F24" : dark ? "#303034" : "#E3E7EE",
        fontSize: "14px", edgeLabelBackground: black ? "#0D0D11" : dark ? "#161619" : "#FFFFFF",
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
