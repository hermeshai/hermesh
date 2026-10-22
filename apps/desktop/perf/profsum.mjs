// Self time by function in a .cpuprofile: node perf/profsum.mjs <file> [top]
import fs from "node:fs";

const p = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const top = Number(process.argv[3] ?? 30);
const byId = new Map(p.nodes.map((n) => [n.id, n]));
const self = new Map();
const dt = p.timeDeltas;
let total = 0;
p.samples.forEach((id, i) => {
  const n = byId.get(id);
  const f = n.callFrame;
  const key = `${f.functionName || "(anon)"} ${(f.url.split("/").pop() || "").slice(0, 30)}:${f.lineNumber + 1}`;
  self.set(key, (self.get(key) ?? 0) + (dt[i] ?? 0));
  total += dt[i] ?? 0;
});
const idle = [...self].filter(([k]) => k.startsWith("(idle)") || k.startsWith("(program)")).reduce((n, [, v]) => n + v, 0);
console.log(`total ${(total / 1000).toFixed(0)} ms, busy ${((total - idle) / 1000).toFixed(0)} ms`);
for (const [k, v] of [...self].sort((a, b) => b[1] - a[1]).slice(0, top)) console.log(`${(v / 1000).toFixed(1).padStart(8)} ms  ${k}`);
