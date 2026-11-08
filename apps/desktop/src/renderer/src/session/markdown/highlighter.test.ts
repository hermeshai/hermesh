import { test } from "node:test";
import assert from "node:assert/strict";
import { createCodeHighlighter, MAX_CODE_BYTES } from "./highlighter.ts";

test("incremental highlighting matches complete highlighting, including multiline states", async () => {
  const highlight = createCodeHighlighter();
  const code = 'const a = `hello\nworld`;\n/* a comment\ncontinued */\nconst value = 12;';
  for (let i = 1; i <= code.length; i += 7) await highlight("stream", code.slice(0, i), "typescript");
  const streamed = await highlight("stream", code, "typescript");
  const fresh = await createCodeHighlighter()("fresh", code, "typescript");
  // Offsets are local to each tokenization call; rendered content/styles must match.
  const visible = (lines: typeof streamed) => lines?.map((line) => line.map(({ content, htmlStyle }) => ({ content, htmlStyle })));
  assert.deepEqual(visible(streamed), visible(fresh));
  assert.equal(streamed?.map((line) => line.map((token) => token.content).join("")).join("\n"), code);
  assert.deepEqual(visible(await highlight("stream", "const b = 5", "typescript")), visible(await highlight("other", "const b = 5", "typescript")));
});
test("unknown and oversized inputs fall back to plain text", async () => {
  const highlight = createCodeHighlighter();
  assert.equal(await highlight("a", "unknown", "invented-lang"), null);
  assert.equal(await highlight("b", "x".repeat(MAX_CODE_BYTES + 1), "js"), null);
  assert.ok(await highlight("c", "echo hello", "sh"));
});

test("first load has correct brand colors in BOTH themes", async () => {
  const result = await createCodeHighlighter()("first", 'const amount = "hello";\n// comment\nconst n = 12;', "typescript");
  const tokens = result!.flat();
  const token = (content: string) => tokens.find(t => t.content === content)!;
  assert.equal(token("const").htmlStyle?.["--shiki-light"], "#1B36D9");
  assert.equal(token('"hello"').htmlStyle?.["--shiki-light"], "#087F78");
  assert.equal(token("// comment").htmlStyle?.["--shiki-light"], "#626775");
  assert.equal(token("12").htmlStyle?.["--shiki-dark"], "#B69AFF");
});
