import react from "@vitejs/plugin-react";
import { readFileSync } from "node:fs";
import path from "node:path";
import { defineConfig } from "vite";

// The console is served from wherever hermeshd puts it (/her/console/ on the machine, or the
// Mac app's proxy under /her/<machine_id>/<token>/), so every address in it is relative. It
// ships with her, so her's version is the workspace's.
const cargo = readFileSync(path.join(import.meta.dirname, "../../Cargo.toml"), "utf8");
const version = /\[workspace\.package\][^[]*?\nversion\s*=\s*"([^"]+)"/.exec(cargo)?.[1] ?? "";

export default defineConfig({
  base: "./",
  plugins: [react()],
  define: { __HER_VERSION__: JSON.stringify(version) },
  // One React: @hermesh/ui links its own copy in its node_modules.
  resolve: { dedupe: ["react", "react-dom"] },
  // One bundle, loaded once from the machine: nothing to gain from splitting it.
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 1024 },
});
