import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "electron-vite";
import type { Plugin } from "vite";
import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";

// HERMESH_DESKTOP_OUT builds somewhere other than out/ (tests building side by side).
const out = process.env.HERMESH_DESKTOP_OUT || path.join(import.meta.dirname, "out");

// pdf.js's data files, served at pdfjs/ beside the page: character maps for Chinese, Japanese
// and Korean fonts, the standard fonts a PDF may not embed, and its image decoders.
const pdfjsDir = path.dirname(createRequire(import.meta.url).resolve("pdfjs-dist/package.json"));
const pdfjsData = ["cmaps", "standard_fonts", "wasm"];
function pdfjsAssets(): Plugin {
  return {
    name: "hermesh-pdfjs-assets",
    configureServer(server) {
      server.middlewares.use("/pdfjs", (req, res, next) => {
        const rel = decodeURIComponent((req.url ?? "").split("?")[0]).replace(/^\/+/, "");
        const file = path.normalize(path.join(pdfjsDir, rel));
        if (!pdfjsData.some((d) => file.startsWith(path.join(pdfjsDir, d) + path.sep)) || !fs.existsSync(file)) return next();
        fs.createReadStream(file).pipe(res);
      });
    },
    generateBundle() {
      for (const dir of pdfjsData)
        for (const name of fs.readdirSync(path.join(pdfjsDir, dir)))
          this.emitFile({ type: "asset", fileName: `pdfjs/${dir}/${name}`, source: fs.readFileSync(path.join(pdfjsDir, dir, name)) });
    },
  };
}

export default defineConfig({
  main: {
    // Everything bundled: the packaged app ships no node_modules.
    build: { externalizeDeps: false, outDir: path.join(out, "main") },
    // Where updates come from, baked in for test builds only (main/updater.ts).
    define: {
      __HERMESH_RELEASE_VERSION__: JSON.stringify(process.env.VERSION ?? ""),
      __HERMESH_UPDATE_FEED__: JSON.stringify(process.env.HERMESH_UPDATE_FEED ?? ""),
      // The release key, which signs the Windows app's feed; test builds bring their own.
      __HERMESH_RELEASE_PUBKEY__: JSON.stringify(fs.readFileSync(process.env.HERMESH_RELEASE_PUBKEY_FILE || path.join(import.meta.dirname, "../../scripts/release-pubkey.pem"), "utf8")),
      __HERMESH_TEST_DATA_DIR__: JSON.stringify(process.env.HERMESH_TEST_DATA_DIR ?? ""),
    },
  },
  preload: {
    // A sandboxed preload must be CommonJS.
    build: { outDir: path.join(out, "preload"), rollupOptions: { output: { format: "cjs", entryFileNames: "[name].cjs" } } },
  },
  renderer: {
    plugins: [react(), tailwindcss(), pdfjsAssets()],
    // The code highlighter's worker loads each language when first shown: an ES module worker, which can split.
    worker: { format: "es" },
    // One React: @hermesh/ui's own node_modules may link another copy (the
    // hermesh-cloud workspace installs the package too).
    resolve: { dedupe: ["react", "react-dom"] },
    build: { outDir: path.join(out, "renderer") },
  },
});
