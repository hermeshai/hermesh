// Prints benchmark results side by side, each column a run or the median of
// several: node perf/compare.mjs baseline run  or  base=a,b,c new=d,e,f
import fs from "node:fs";
import path from "node:path";

const read = (l) => JSON.parse(fs.readFileSync(path.join(import.meta.dirname, "results", `${l}.json`), "utf8")).results;
const median = (xs) => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)];
const cols = process.argv.slice(2).map((arg) => {
  const [name, list] = arg.includes("=") ? arg.split("=") : [arg, arg];
  const runs = list.split(",").map(read);
  const keys = [...new Set(runs.flatMap((r) => Object.keys(r)))];
  return { name, r: Object.fromEntries(keys.map((k) => [k, median(runs.map((r) => r[k]).filter((v) => v !== undefined))])) };
});
const keys = [...new Set(cols.flatMap((c) => Object.keys(c.r)))];
console.log(["metric".padEnd(42), ...cols.map((c) => c.name.padStart(12))].join(""));
for (const k of keys) console.log([k.padEnd(42), ...cols.map((c) => String(c.r[k] ?? "").padStart(12))].join(""));
