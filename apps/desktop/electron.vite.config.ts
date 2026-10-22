import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "electron-vite";
import path from "node:path";

// HERMESH_DESKTOP_OUT builds somewhere other than out/ (tests building side by side).
const out = process.env.HERMESH_DESKTOP_OUT || path.join(import.meta.dirname, "out");

export default defineConfig({
  main: {
    // Everything bundled: the packaged app ships no node_modules.
    build: { externalizeDeps: false, outDir: path.join(out, "main") },
    // Where updates come from, baked in for test builds only (main/updater.ts).
    define: {
      __HERMESH_RELEASE_VERSION__: JSON.stringify(process.env.VERSION ?? ""),
      __HERMESH_UPDATE_FEED__: JSON.stringify(process.env.HERMESH_UPDATE_FEED ?? ""),
      __HERMESH_TEST_DATA_DIR__: JSON.stringify(process.env.HERMESH_TEST_DATA_DIR ?? ""),
    },
  },
  preload: {
    // A sandboxed preload must be CommonJS.
    build: { outDir: path.join(out, "preload"), rollupOptions: { output: { format: "cjs", entryFileNames: "[name].cjs" } } },
  },
  renderer: {
    plugins: [react(), tailwindcss()],
    // One React: @hermesh/ui's own node_modules may link another copy (the
    // hermesh-cloud workspace installs the package too).
    resolve: { dedupe: ["react", "react-dom"] },
    build: { outDir: path.join(out, "renderer") },
  },
});
