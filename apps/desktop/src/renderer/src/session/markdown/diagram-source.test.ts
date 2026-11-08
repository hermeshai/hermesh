import { test } from "node:test";
import assert from "node:assert/strict";
import { validDiagram } from "./diagram-source.ts";
test("local diagram source accepts graph syntax but rejects configuration and resource loading", () => {
  for (const source of ['flowchart LR\nA[读取]-->B{检查}', 'sequenceDiagram\nA->>B: Hello', 'classDiagram\nA <|-- B']) assert.equal(validDiagram(source), true);
  for (const source of ['%%{init: {securityLevel:"loose"}}%%\nflowchart LR', '---\nconfig:\n---\nflowchart LR', 'flowchart LR\nclick A "//example.com"', 'flowchart LR\nA@{img: "//example.com/a.png"}', 'flowchart LR\nclassDef default fill:url(//example.com)', 'x'.repeat(20001)]) assert.equal(validDiagram(source), false);
});
