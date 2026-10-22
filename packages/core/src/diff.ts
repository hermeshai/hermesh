export type DiffLineKind = "context" | "added" | "removed" | "note";

export interface DiffLine {
  kind: DiffLineKind;
  /** The line's number before the change; undefined for an added line or a note. */
  old: number | undefined;
  /** The line's number after the change; undefined for a removed line or a note. */
  new: number | undefined;
  text: string;
}

export interface DiffHunk {
  header: string;
  lines: DiffLine[];
}

export interface DiffFile {
  path: string;
  insertions: number;
  deletions: number;
  binary: boolean;
  hunks: DiffHunk[];
}

/** The first number of a hunk range ("-18,4" is 18), or 0. */
function rangeStart(range: string | undefined): number {
  const n = (range ?? "").slice(1).split(",").filter((p) => p !== "")[0] ?? "";
  return /^[+-]?\d+$/.test(n) ? Number(n) : 0;
}

/** The files of a `git diff`, with numbered lines. */
export function parseUnifiedDiff(text: string): DiffFile[] {
  const files: DiffFile[] = [];
  let old = 0;
  let next = 0;
  for (const line of text.split("\n")) {
    const file = files.at(-1);
    if (line.startsWith("diff --git ")) {
      files.push({ path: line.split(" b/").at(-1)!, insertions: 0, deletions: 0, binary: false, hunks: [] });
    } else if (line.startsWith("+++ ") || line.startsWith("--- ")) {
      const p = line.slice(4);
      if (file && line.startsWith("+++ ") && p !== "/dev/null") file.path = p.startsWith("b/") ? p.slice(2) : p;
    } else if (line.startsWith("Binary files ")) {
      if (file) file.binary = true;
    } else if (line.startsWith("@@")) {
      const nums = line.split(" ").filter((p) => p !== "").slice(1, 3);
      old = rangeStart(nums[0]);
      next = rangeStart(nums[1]);
      file?.hunks.push({ header: line, lines: [] });
    } else if (file && file.hunks.length > 0) {
      const lines = file.hunks[file.hunks.length - 1].lines;
      const rest = line.slice(1);
      switch (line[0]) {
        case "+":
          lines.push({ kind: "added", old: undefined, new: next, text: rest });
          file.insertions++;
          next++;
          break;
        case "-":
          lines.push({ kind: "removed", old, new: undefined, text: rest });
          file.deletions++;
          old++;
          break;
        case " ":
          lines.push({ kind: "context", old, new: next, text: rest });
          old++;
          next++;
          break;
        case "\\":
          lines.push({ kind: "note", old: undefined, new: undefined, text: line });
          break;
      }
    }
  }
  return files;
}
