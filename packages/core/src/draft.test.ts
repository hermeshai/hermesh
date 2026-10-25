import { describe, expect, it } from "vitest";
import { splitBlocks, withoutOpenTable } from "./draft";

describe("splitBlocks", () => {
  it("cuts at blank lines before unindented text", () => {
    expect(splitBlocks("## Title\n\nOne\ntwo\n\n- a\n- b")).toEqual(["## Title\n", "One\ntwo\n", "- a\n- b"]);
  });

  it("keeps a list item's indented paragraph with it", () => {
    expect(splitBlocks("- item\n\n  more of it\n\nNext")).toEqual(["- item\n\n  more of it\n", "Next"]);
  });

  it("never cuts inside a code fence, closed or still open", () => {
    expect(splitBlocks("```go\na\n\nb\n```\n\nAfter")).toEqual(["```go\na\n\nb\n```\n", "After"]);
    expect(splitBlocks("Text\n\n~~~\na\n\nb")).toEqual(["Text\n", "~~~\na\n\nb"]);
  });

  it("puts the blocks back together as they were", () => {
    const text = "A\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\nx\n\ny\n```\n\n1. one\n\n2. two";
    expect(splitBlocks(text).join("\n")).toBe(text);
  });
});

describe("withoutOpenTable", () => {
  it("holds a table back until its delimiter row has every column", () => {
    expect(withoutOpenTable("Results:\n| a | b |")).toBe("Results:");
    expect(withoutOpenTable("| a | b |\n|---|")).toBe("");
    expect(withoutOpenTable("| a | b |\n|---|:--:|")).toBe("| a | b |\n|---|:--:|");
    expect(withoutOpenTable("| a | b |\n|---|---|\n| 1 |")).toBe("| a | b |\n|---|---|\n| 1 |");
  });

  it("leaves text without a table at its end alone", () => {
    expect(withoutOpenTable("No table | here")).toBe("No table | here");
  });
});
