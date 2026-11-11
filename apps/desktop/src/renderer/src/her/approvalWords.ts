import type { CommandRule } from "@hermesh/her-protocol";

// An approval's words, worked out from the request alone, so they can be
// checked without a window.

/**
 * The project a `mesh <machine>/<project>` rule remembers (docs/her-mesh.md §5): the choice names it,
 * not the raw rule.
 */
export function meshProject(rule: CommandRule | null | undefined): string | undefined {
  return rule?.prefix.length === 2 && rule.prefix[0] === "mesh" ? rule.prefix[1] : undefined;
}

/**
 * A summary of several lines ("Start Codex in homemini/hermesh", then the prompt): its first line is
 * the question, the rest shows under it. Null for a summary of one line.
 */
export function summaryParts(summary: string): { title: string; body: string } | null {
  const at = summary.indexOf("\n");
  if (at < 0) return null;
  const title = summary.slice(0, at).trim();
  const body = summary.slice(at + 1).replace(/^\n+/, "").trimEnd();
  return title && body ? { title, body } : null;
}
