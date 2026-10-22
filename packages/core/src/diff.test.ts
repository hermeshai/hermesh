import { describe, expect, test } from "vitest";
import { parseUnifiedDiff } from "./diff";

describe("unified diff", () => {
  test("files, hunks and line numbers", () => {
    const text = [
      "diff --git a/src/a.ts b/src/a.ts",
      "index 1..2 100644",
      "--- a/src/a.ts",
      "+++ b/src/a.ts",
      "@@ -18,4 +18,5 @@ export function f() {",
      "   return {",
      '-    sameSite: "none",',
      '+    sameSite: "lax",',
      "+    secure: true,",
      '     path: "/",',
      "\\ No newline at end of file",
      "diff --git a/img.png b/img.png",
      "Binary files a/img.png and b/img.png differ",
      "diff --git a/new.txt b/new.txt",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/new.txt",
      "@@ -0,0 +1 @@",
      "+hello",
    ].join("\n");
    const files = parseUnifiedDiff(text);
    expect(files.map((f) => f.path)).toEqual(["src/a.ts", "img.png", "new.txt"]);
    expect([files[0].insertions, files[0].deletions]).toEqual([2, 1]);
    const lines = files[0].hunks[0].lines;
    expect(lines.map((l) => l.kind)).toEqual(["context", "removed", "added", "added", "context", "note"]);
    expect([lines[0].old, lines[0].new]).toEqual([18, 18]);
    expect([lines[1].old, lines[1].new]).toEqual([19, undefined]);
    expect([lines[3].new, lines[4].old, lines[4].new]).toEqual([20, 20, 21]);
    expect(files[1].binary).toBe(true);
    expect(files[1].hunks).toEqual([]);
    expect(files[2].insertions).toBe(1);
    expect(files[2].hunks[0].lines[0].new).toBe(1);
  });

  test("an empty diff", () => {
    expect(parseUnifiedDiff("")).toEqual([]);
  });
});
