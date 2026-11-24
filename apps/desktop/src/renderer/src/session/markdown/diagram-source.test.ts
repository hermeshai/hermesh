import { test } from "node:test";
import assert from "node:assert/strict";
import { performance } from "node:perf_hooks";
import { MAX_DIAGRAM_LENGTH, prepareDiagram, validDiagram } from "./diagram-source.ts";

test("local diagram source accepts graph syntax and angle-bracket placeholders but rejects configuration and resource loading", () => {
  for (const source of [
    "flowchart LR\nA[读取]-->B{检查}",
    "sequenceDiagram\nA->>B: Hello",
    "classDiagram\nA <|-- B",
    'flowchart TB\n  subgraph LocalMachine["In-Process Local Machine (owner/profiles/<id>/machine)"]\n    K["Engine"]\n  end\n  UI <-->|"Local IPC"| LocalMachine',
    "classDiagram\n  class Service {\n    <<interface>>\n    +find(id: string) Map<string, List<Node>>\n  }",
    'flowchart LR\n  A["第一行<br/>第二行"] --> B',
  ]) {
    assert.equal(validDiagram(source), true, source);
  }
  for (const source of [
    '%%{init: {securityLevel:"loose"}}%%\nflowchart LR',
    "---\nconfig:\n---\nflowchart LR",
    'flowchart LR\nclick A "//example.com"',
    'flowchart LR\nA@{img: "//example.com/a.png"}',
    "flowchart LR\nclassDef default fill:url(//example.com)",
    'flowchart LR\nA["https://example.com"]',
    'flowchart LR\nA["javascript:alert(1)"]',
    'flowchart LR\nA["data:text/html,hi"]',
    "x".repeat(MAX_DIAGRAM_LENGTH + 1),
  ]) {
    assert.equal(validDiagram(source), false, source);
  }
});

test("prepareDiagram escapes placeholders and HTML tags while preserving Mermaid syntax", () => {
  // Fast path: returns the exact same string reference when nothing needs rewriting.
  for (const unchanged of [
    "flowchart LR\nA[读取] --> B{检查}",
    'flowchart TB\nUI <-->|"IPC"| Owner',
    "classDiagram\n  class S {\n    <<interface>>\n  }\n  A <|-- B",
    'flowchart LR\nA["a<br/>b"] --> B',
  ]) {
    assert.equal(prepareDiagram(unchanged), unchanged);
  }

  // Placeholders in quoted/unquoted nodes, subgraphs, edges and sequence messages.
  assert.equal(
    prepareDiagram('subgraph LocalMachine["In-Process Local Machine (owner/profiles/<id>/machine)"]'),
    'subgraph LocalMachine["In-Process Local Machine (owner/profiles/#lt;id#gt;/machine)"]',
  );
  assert.equal(
    prepareDiagram("flowchart LR\nA[owner/<id>/file] -- <tag> --> B{is <valid>?}"),
    "flowchart LR\nA[owner/#lt;id#gt;/file] -- #lt;tag#gt; --> B{is #lt;valid#gt;?}",
  );
  assert.equal(
    prepareDiagram("sequenceDiagram\nA->>B: GET /profiles/<id>/machine<br>200 OK"),
    "sequenceDiagram\nA->>B: GET /profiles/#lt;id#gt;/machine<br/>200 OK",
  );

  // Stereotypes stay intact while nested generics are escaped in balance.
  assert.equal(
    prepareDiagram("classDiagram\n  class S {\n    <<interface>>\n    +get() Map<string, List<Node>>\n  }"),
    "classDiagram\n  class S {\n    <<interface>>\n    +get() Map#lt;string, List#lt;Node#gt;#gt;\n  }",
  );
  assert.equal(
    prepareDiagram("stateDiagram-v2\n  state c <<choice>>\n  [*] --> c: init(<id>)"),
    "stateDiagram-v2\n  state c <<choice>>\n  [*] --> c: init(#lt;id#gt;)",
  );

  // Unclosed '<' inside or outside quotes never corrupts subsequent arrows, shapes or Windows paths.
  assert.equal(prepareDiagram('flowchart LR\nA["a <b"] --> B["ok"]'), 'flowchart LR\nA["a #lt;b"] --> B["ok"]');
  assert.equal(prepareDiagram("flowchart LR\nA[a <b] --> B>asym]"), "flowchart LR\nA[a #lt;b] --> B>asym]");
  assert.equal(prepareDiagram('flowchart LR\nA["C:\\Users\\"] --> B["<id>"]'), 'flowchart LR\nA["C:\\Users\\"] --> B["#lt;id#gt;"]');

  // Raw HTML / XSS vectors (including tags nested inside HTML comments) are neutralized, leaving zero raw tags besides <br/>.
  const xss = prepareDiagram('journey\n  title <img src="x" onerror="alert(1)">\n  section <script>alert(2)</script>\n    <!-- <svg onload="alert(3)"> --><br />next: 5: Me');
  assert.equal(
    xss,
    'journey\n  title #lt;img src="x" onerror="alert(1)"#gt;\n  section #lt;script#gt;alert(2)#lt;/script#gt;\n    #lt;!-- #lt;svg onload="alert(3)"#gt; --#gt;<br/>next: 5: Me',
  );
  assert.equal(/<(?!br\/>)[a-zA-Z/!?]/.test(xss), false);
});

test("prepareDiagram runs in linear time on 20KB pathological inputs", () => {
  const inputs = [
    "flowchart LR\n" + "A --> B\n".repeat(2400),
    "flowchart LR\n" + '  A["owner/profiles/<id>/machine"] --> B\n'.repeat(480),
    "<a".repeat(MAX_DIAGRAM_LENGTH / 2),
    "<<a ".repeat(MAX_DIAGRAM_LENGTH / 4),
    "<!--".repeat(MAX_DIAGRAM_LENGTH / 4 - 1) + "-->",
    "<!--".repeat(MAX_DIAGRAM_LENGTH / 4),
    "<" + "a".repeat(MAX_DIAGRAM_LENGTH - 1),
  ];
  for (const s of inputs) prepareDiagram(s);
  const start = performance.now();
  for (let round = 0; round < 5; round++) {
    for (const s of inputs) {
      assert.equal(validDiagram(s), true);
      prepareDiagram(s);
    }
  }
  const elapsed = performance.now() - start;
  assert.ok(elapsed < 1000, `expected 35 x 20KB calls to finish in linear time, took ${elapsed.toFixed(1)}ms`);
});

