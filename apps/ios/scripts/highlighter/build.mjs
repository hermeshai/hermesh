// Builds the code highlighter for the iPhone app from the desktop app's
// Shiki, so code reads in the same colours on both:
//   highlighter.js    entry.js and Shiki, one script for JavaScriptCore
//   langs/<id>.json   each language's grammar, read when first shown
//   langs/index.json  each language's id by its id and aliases
// Usage: node build.mjs <out dir>
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { realpathSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const desktop = join(here, "../../../desktop");
const out = process.argv[2];
if (!out) throw new Error("usage: node build.mjs <out dir>");

const { build } = createRequire(join(desktop, "package.json"))("vite");
const shiki = realpathSync(join(desktop, "node_modules/shiki"));
const { bundledLanguagesInfo } = await import(pathToFileURL(join(shiki, JSON.parse(await readFile(join(shiki, "package.json"), "utf8")).exports["./langs"])).href);

await rm(out, { recursive: true, force: true });
await mkdir(join(out, "langs"), { recursive: true });

await build({
  configFile: false,
  logLevel: "warn",
  plugins: [
    {
      // Shiki as the desktop app has it.
      name: "desktop-shiki",
      resolveId(id, importer) {
        return importer?.startsWith(here) && id.startsWith("shiki") ? this.resolve(id, join(desktop, "index.js"), { skipSelf: true }) : null;
      },
    },
  ],
  build: {
    outDir: out,
    emptyOutDir: false,
    minify: true,
    target: "es2022",
    lib: { entry: join(here, "entry.js"), formats: ["iife"], name: "Highlighter", fileName: () => "highlighter.js" },
  },
});

const index = {};
for (const info of bundledLanguagesInfo) {
  const regs = (await info.import()).default;
  // The language comes last, after the ones it embeds (each written as its own).
  await writeFile(join(out, "langs", `${info.id}.json`), JSON.stringify(regs.at(-1)));
  for (const name of [info.id, ...(info.aliases ?? [])]) index[name.toLowerCase()] = info.id;
}
await writeFile(join(out, "langs", "index.json"), JSON.stringify(index));
console.log(`› ${out}: ${bundledLanguagesInfo.length} languages`);
