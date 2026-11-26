# Inline images and blurred lightbox in coding sessions (2026-11-26)

Status: implemented across Go (`internal/kernel`, `internal/owner`, `internal/machine`), shared refs (`packages/her-client`, `packages/HermeshKit`), Mac Desktop (`apps/desktop`), and iPhone (`apps/ios`).
Prototype: [session-images-demo.html](session-images-demo.html) (switches between Mac, iPhone, and side-by-side view; covers single-shot deduplication, before/after multi-image layout, blurred lightbox with zoom/pan and pull-to-dismiss, and multi-machine online/cached/offline states).

Scope: coding sessions (`SessionPage` on Mac/Windows/Linux, `SessionView` on iPhone), across local and remote execution machines.

---

## 0. Summary

| Aspect | Today | Proposed |
|---|---|---|
| `![alt](path)` in a reply | `src` dropped; renders plain `alt` text (`Markdown.tsx`, `Markdown.swift`) | Renders inline as a proportioned image card (local/session machine paths only; external `http(s)://` stays blocked) |
| `[label](path.png)` (no `:line`) | Renders filename chip `FileTag`; **grey & unclickable** if outside `s.folder` | Deduplicated if already shown by `![]()` in the same reply; otherwise keeps inline clickable tag and renders an attached image strip below the block |
| `` `path/icon.png` `` (inline code) | `FileTag` once resolved | Stays `FileTag` (never auto-expanded to avoid flooding); clicking an image `FileTag` opens the blurred Lightbox |
| Screenshots outside workspace (`~/.gemini/.../brain/`, `/tmp/`) | Rejected by `filesScope.rel()` with `FILE_REF_KIND_OUTSIDE` | Allowed for **Owner / Her** principals when magic bytes confirm a raster image (`PNG`, `JPEG`, `GIF`, `WebP`, ≤ 10 MiB), and pinned into `blobs/` at turn end |
| Remote `ReadFile` on iPhone / secondary Mac | Re-fetches full image over relay on every session open (`owner/files.go` lacked settled-turn short-circuit on `ReadFile`) | Short-circuits settled turns from local `session_files_kept` SQLite cache (`< 2 ms`, `0` relay traffic on repeat opens and offline) |
| Clicking an image (Mac) | Opens heavy document dialog `FileView` (or nothing if outside workspace) | Immersive **blurred-backdrop Lightbox** (`backdrop-filter: blur(22px)`), wide centered header (`filename` + `W×H · size`), only `⋯` (Save / Reveal in Finder / Copy Path) and `✕` on the right; silent double-click/wheel zoom + pan |
| Tapping an image (iPhone) | Pushes `SessionFilePage` + `QLPreviewController` | Full-screen **blurred Lightbox overlay** with symmetric top bar (`✕` · wide centered `filename` & `W×H · size` · `⋯` menu for Save to Photos / Share / Copy Path); silent double-tap/pinch zoom, horizontal swipe, and pull-down to dismiss |

---

## 1. Motivating case (measured from local session `s_kouddhys_01m8az1ya…`)

In session **「iOS 模拟器启动截图测试」** (`antigravity` on `hermesh`), the agent captured a simulator screenshot (`1206 × 2622` PNG, `238 KB`) to `/Users/yipengfei/.gemini/antigravity-cli/brain/<provider_ref>/ios_simulator_screenshot.png` (outside `/Users/yipengfei/CodeSpace/hermesh` so the git working tree stayed clean) and replied:

```markdown
![Hermesh iOS 模拟器截图](/Users/yipengfei/.gemini/antigravity-cli/brain/97c3c6b3-1bb2-4381-a89e-c6e9f8ebfa66/ios_simulator_screenshot.png)

截图文件：[ios_simulator_screenshot.png](file:///Users/yipengfei/.gemini/antigravity-cli/brain/97c3c6b3-1bb2-4381-a89e-c6e9f8ebfa66/ios_simulator_screenshot.png)
```

1. The first line rendered only plain text `Hermesh iOS 模拟器截图`.
2. The second line became a greyed-out `.fref.gone` chip (`FILE_REF_KIND_OUTSIDE`) that did nothing on click.

Similarly, Codex and Claude sessions routinely write verification screenshots to `/tmp/.../*.png` and link them as `[有迷你条](/tmp/shots/with-bar.png)、[关闭后](/tmp/shots/without-bar.png)`.

---

## 2. Content-area recognition & deduplication rules

Shared between TypeScript (`packages/her-client/src/refs.ts`) and Swift (`HermeshSessions/FileRefs.swift`):

1. **Cross-OS path parsing**: `fileOfLink` recognizes Windows drive paths (`^[A-Za-z]:[\\/]`) regardless of the viewer device's OS, so an iPhone or Mac viewing a session on a Windows machine parses `![shot](C:\Temp\shot.png)` identically.
2. **External URLs stay blocked**: `![alt](https://...)` and `data:` never auto-fetch in `Markdown` (preserves Tracking-Pixel / SSRF safety).
3. **Four tiers of image references**:
   - **Tier 1 — `![alt](path)`**: Unambiguous display intent. Renders inline right where written once its AST slice settles (`!draft`).
   - **Tier 2 — `[label](path.ext)`** (raster image extension `.png/.jpg/.jpeg/.gif/.webp`, or `.svg` inside workspace, with `line === undefined`):
     - Normalize `path` within the reply. If `path` is **already rendered by a `![...](path)` in the same reply**, `[label](path)` stays a compact clickable `FileTag` (opens the same Lightbox) — **never renders the same image twice**.
     - Otherwise, `[label](path)` stays an inline clickable tag so prose/list flow is unbroken, and its unique image(s) render in an **image strip** immediately below the enclosing paragraph or list.
   - **Tier 3 — `` `path/to/img.png` `` (inline code)**: Never auto-renders inline (avoids flooding chat during directory/asset discussions). Remains a `FileTag` once confirmed to exist; clicking an image `FileTag` opens the Lightbox.
   - **Tier 4 — Missing / unreadable / > 10 MiB**: Gracefully falls back to a muted `FileTag` (`.fref.gone` or size note), never a broken `<img>` icon.
4. **Agent guidance (`internal/machine/messagemcp.go`)**: Update the single MCP instruction sentence to tell coding agents to use `![caption](path)` when showing a screenshot or picture directly to the owner.

---

## 3. Interaction design: Mac & iPhone

### 3.1 Mac Desktop

- **Inline presentation**:
  - Single image: left-aligned in the prose column, `max-width: min(460px, 100%)`, `max-height: 380px`, `object-fit: contain`, `border-radius: var(--r-lg)` (`10px`), `0.5px` inner border (`var(--sep)`), `var(--fill)` ground for transparent PNGs, `cursor: zoom-in`. A `1206 × 2622` portrait phone screenshot sits at `~175 × 380 px` instead of dominating 1,000 px of vertical scroll.
  - Multiple images in one block/strip: side-by-side flex row (`gap: 8px`, shared row height up to `300px`, weighted by aspect ratio as in `her/Messages.tsx` `ImageGroup`), with subtle captions when `alt`/label is descriptive.
- **Blurred Lightbox**:
  - Full-window portal with dark translucent veil + Gaussian blur (`backdrop-filter: blur(22px) saturate(1.28)`), paired with `html.vibrant:has(..., .lightbox)` so macOS sidebar vibrancy turns opaque and never bleeds sharp text through the blur.
  - **Symmetric top bar (`96px minmax(0, 1fr) 96px`)**:
    - Left `96px`: clears macOS window traffic lights.
    - Center `1fr` (up to `640px` wide): centered two-line title — filename on top, quiet metadata (`1 / N · W×H · size · turn`) below.
    - Right `96px`: **only two icon buttons** — `⋯` (dropdown menu: `存储到“下载”`, `在访达中显示` when `filesHome.local === true`, `拷贝路径`; no desktop Copy Image action) and `✕` (`关闭`). No zoom button or gesture hint text.
  - **Silent gestures & keyboard**: Click backdrop or `Esc` closes; double-click image or scroll wheel / trackpad pinch zooms (`1×` ↔ `2.4×`–`4×`) with drag-to-pan; `←` / `→` keys and side chevrons step across multiple images in the same reply.

### 3.2 iPhone

- **Inline presentation**:
  - Single portrait screenshot: capped at `maxHeight: 260pt`, `maxWidth: 100%`, continuous `cornerRadius: 14pt`, `0.5pt` `Palette.separator` stroke. Keeps surrounding chat readable on a `393pt`–`402pt` screen.
  - 2 images in one block/strip (e.g. Before / After): **2-column side-by-side row** (`HStack(spacing: 8)`, height `220pt`) with compact bottom-left corner badges (`修复前` / `修复后`); 3+ images use a horizontal view-aligned snap strip.
  - Long-press (`.contextMenu`): native lift preview + `查看大图`、`存储到相册`、`分享…`、`拷贝路径`.
- **Full-screen Blurred Lightbox (replaces pushing `SessionFilePage` for images)**:
  - Overlays the session with darkened `.ultraThinMaterial` blur + spring zoom transition.
  - **Symmetric top bar (`38pt 1fr 38pt`)**:
    - Left: circular glass `✕` close button.
    - Center: wide centered two-line header (`filename` + `1 / N · W×H · size`).
    - Right: circular glass `⋯` menu button (`存储到相册`、`分享…`、`拷贝路径`).
    - No bottom toolbar, zoom badge, or gesture hint text — leaving the entire screen below the header unobstructed for the image.
  - **Silent one-handed gestures**:
    - **Double-tap & pinch-to-zoom** (`1×` ↔ `2.4×`–`4×`) with pan bounds.
    - **Vertical drag-down to dismiss** (at `1×` zoom): image scales down with finger delta while backdrop blur fades out interactively.
    - **Horizontal paged swipe** across images in the same reply.

---

## 4. Multi-machine & remote iPhone architecture

### 4.1 Safe reading of screenshots outside the workspace (`internal/kernel/files.go`)

Coding sessions run under two kinds of principals:
- **Owner / Her** (`s.owner == true`): the owner's local Mac, another of the owner's Macs, Her, or the owner's iPhone over an authenticated E2E `PrincipalOwner` peer tunnel (`internal/machine/services.go`).
- **Scoped external client grants** (`s.owner == false`): restricted strictly to `s.folder` (unchanged).

When `s.owner == true` and an asked path resolves outside `s.folder`:
1. Evaluate symlinks and stat the target; require a regular file with `0 < size <= maxFileImage` (`10 MiB`) and a raster image extension (`.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`).
2. Read the first 16 bytes and require **`imageMime(head) != ""`** (valid PNG, JPEG, GIF, or WebP magic header) in **both `ResolveFiles` and `ReadFile`**.
3. If any check fails, return `FILE_REF_KIND_OUTSIDE` / `errFileOutside` (`PermissionDenied`) — identical to any other outside path. Neither file contents nor file-existence differences for non-image files (e.g. `~/.ssh/id_rsa`, `/etc/passwd`, `.env`) are ever exposed.

### 4.2 Turn-end blob pinning for ephemeral `/tmp` & external screenshots (`internal/kernel/engine.go`)

Files inside `s.folder` are already preserved per turn by `vcs.CaptureCheckpoint`. Screenshots in `/tmp/` or `~/.gemini/.../brain/` are outside Git and could be overwritten by a later turn or cleaned on reboot before an iPhone opens the session:

- When a run finishes (`captureTurnCheckpoint`), scan that run's completed `ASSISTANT_MESSAGE` items for local image paths not present in the checkpoint tree.
- For each valid raster image (`<= 10 MiB`, `imageMime(data) != ""`, up to 16 images per turn), store the bytes in the engine's existing content-addressed blob store (`e.st.PutBlob` → `blobs/<sha256>`) and record `(session_id, run_id, asked_path, blob_sha256, size)` in `sessions_v2.db` (cleaned up on `DeleteSession` alongside checkpoints).
- Lookup order in `ResolveFiles` / `ReadFile(session, run, path)`:
  1. Turn's Git checkpoint tree (`s.inTree`);
  2. Turn's pinned image blobs (`turn_image_blobs`);
  3. Live disk (`s.now` inside folder, or owner magic-verified image outside folder — which also makes historical sessions like `s_kouddhys_01m8az1ya…` work immediately without backfill).

### 4.3 Zero-refetch caching & offline support (`internal/owner/files.go`)

`internal/owner` runs identically inside macOS `hermeshd` and iPhone `HermeshCore`:
- **Fix `ownerSessionService.ReadFile`**: Add the same settled-turn local cache check that `ResolveFiles` and `ReadFileBytes` already have — when `req.RunId != ""` and `session_files_kept` holds a settled `ReadFileResponse` (`Turn > 0` or `Hash != ""` from a finished run), serve it directly from local SQLite without dialing the remote machine.
- **Result for iPhone over cellular/relay**:
  - First view fetches `ReadFile` once over the E2E tunnel/relay and stores it in `session_files_kept` (200 MB LRU).
  - Re-opening the session (online or offline) renders in `< 2 ms` with `0` network traffic.
  - If the remote machine is offline before an image was ever fetched, `ReadFile` returns `Unavailable`; the UI renders a quiet offline placeholder card (`<machine> 离线 · 连上后自动显示`) and automatically retries when `reachable` becomes `true`.

### 4.4 Viewport laziness, concurrency cap & memory bounds

- **Streaming**: While `DraftContext` is `true`, render a fixed-height placeholder (`220px` Mac / `180pt` iPhone) and issue no RPCs until the Markdown block closes.
- **Viewport & concurrency**: Trigger `ReadFile` via `IntersectionObserver` (`rootMargin: "300px"`) on Mac and SwiftUI `.task` inside `LazyVStack` on iPhone, capped at **3 concurrent** image fetches per session so large PNGs never contend with live `SubscribeSession` deltas over a slow relay.
- **Virtual scroll stability (Mac)**: Store `{ status, objectUrl, width, height }` in a module-level LRU store (`useSyncExternalStore`, max 80 ObjectURLs, revoking evicted ones) keyed by `session\0run\0path`. When `useVirtualRows` unmounts and remounts a row, its exact `aspect-ratio` and `blob:` URL are available synchronously on frame 0.
- **Downsampled thumbnails (iPhone)**: Decode inline cards off the main actor via `CGImageSourceCreateThumbnailAtIndex` (`kCGImageSourceThumbnailMaxPixelSize: 960`), keeping scroll memory under `~2 MB` per screenshot; decode full-resolution `UIImage` only when the Lightbox opens.
