# her-slide

PowerPoint decks: rendering slides, exporting Markdown, finding and replacing text, and surgical saves (only the edited parts change, and the save checks itself afterwards). The engine is a fork of BetterOffice (`betteroffice-pptx` in the root `Cargo.toml`, pinned by rev). Fonts come from this machine; the only built-in ones are Carlito / Caladea (metric-compatible fonts for Calibri / Cambria, SIL OFL, in `fonts/` with `OFL-*.txt`, compiled in with `include_bytes!`; from `@betteroffice/fonts` 0.4.2). Fonts on this machine and Office's fonts come first, and these two are used only when Calibri/Cambria are missing, so line wrapping matches PowerPoint. See `src/fonts.rs`. In the frontend, `apps/desktop/electron.vite.config.ts` provides the same fonts.

Interactive editing in the desktop app uses BetterOffice's React/WASM editor, which draws directly on a Canvas in the frontend; this crate's PNG interface is for offline rendering and comparison tests. The font provider also serves the browser editor; see [PPTX editor design](../../docs/desktop/presentation-editor-design.md).

## Developing against a local fork

Create `.cargo/config.toml` in the repository root (it is gitignored; do not commit it):

```toml
[patch."https://github.com/Eliseowzy/betteroffice"]
betteroffice-pptx = { path = "../betteroffice/crates/betteroffice-pptx" }
```

After that, `cargo build` uses the local checkout, so you can try changes on the fork without pushing them. While the patch is in place, Cargo removes the git source of these packages from `Cargo.lock`; do not commit such a `Cargo.lock`. Remove the patch and build once more, and the lock file is restored. When the fork has new commits that you want to reference for real, delete this file, set `rev` in the root `Cargo.toml` to the full commit hash from `git -C <fork> rev-parse origin/finx/0.5`, then run `cargo update -p betteroffice-pptx`.

The fork pins `libm` at `=0.2.15`, so this repository's `Cargo.lock` uses 0.2.15 too.

## Tests

```bash
cargo test -p her-slide
```

For integration tests with real samples, see `tools/ppt-corpus/README.md`.
