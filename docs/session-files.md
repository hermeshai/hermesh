# A session's files: tags, preview, the file panel, and the requests behind them (2026-11-25)

Status: steps 1 and 2 built (2026-11-25), with the recommended answer to each of §11's decisions; §13
records what was built and how it differs from the plan. Step 3 (changing files) is not.
On the Mac, §14 (2026-11-26) replaces the preview over the window (§3.2, §3.3) and the file panel
(§3.4) with the side panel's tabs.

Scope: coding sessions, on the Mac and the iPhone, for a session on any of
the owner's machines. What the owner sees comes from FinxWork
(`~/CodeSpace/finxwork`, the owner's own, and where this app's session
page came from); what is behind it is new, because a FinxWork file is on
the same Mac and a hermesh file is on a machine that may be another one.
Her's conversation has its own (her-deliverables.md) and does not change.

## 0. Summary

| | What | From |
|---|---|---|
| A file a reply names | A **tag** in the line: icon, name, `:42`. From a link always (grey when it is not there), from inline code only when it is there | FinxWork `FileTag` |
| Clicking a result (Markdown, picture, PDF, page) | A **preview over the window**, one button from the side panel | FinxWork `FilePreview` |
| Clicking code, or anything with a line | The **file panel** on the session's right: tabs, the file at that line, the folder tree | FinxWork `FilePanel`, `Tree`, `Editor` |
| A page (`.html`) | Runs in a view of its own, in a process apart from the app | FinxWork `pagePreview.ts` |
| The iPhone | The same tags and rules; a file is a pushed page, the tree a pushed list | New, on Her's `HerFilePage` and Quick Look |
| Where the bytes come from | Three requests to the session's machine, routed and kept by the owner layer both apps share; a file read as it was when its turn ended | New (§6) |

Nothing is fetched to draw a reply but one small question per reply ("which
of these are files"). Content moves when a file is opened.

## 1. Before

The case that started this: session `s_kouddhys_01m86yb…` (Antigravity)
wrote `docs/standalone-mode-mac-demo.html` and linked it with a `file://`
target. Both apps showed plain words (`Link` in `session/content.tsx` and
`linked` in `Markdown.swift` make only http and https links), and the
browser that opened was the agent's own `open` on the machine.

What each app can show today:

| | The Mac | The iPhone |
|---|---|---|
| Markdown, PDF, pictures | Yes, in Her only (`her/preview/FilePreview`, `PdfView`, `Lightbox`) | Yes, in Her only (`HerFilePage`, Quick Look) |
| Code | Coloured, no line numbers, no going to a line (`Code`) | Numbered lines, no going to a line (`HerCodeLines`) |
| A page | Nothing: no frame or view for one | Quick Look, which lays it out and runs none of its scripts (not checked on a device) |
| A file tree, a side panel, a diff | Nothing | Nothing |

## 2. Where it comes from

**FinxWork** is the same stack (Electron, React 19.3.1, pdf.js 5.4.394,
shiki 4.5.0), and hermesh's session page is its page with the file parts
taken out: `session/content.tsx` here is 142 lines to its 232, `scope.ts` 9
of its 91, `her/preview/FilePreview.tsx` its preview without the page, the
document and the picture. `packages/her-client/src/refs.ts` (`fileOfLink`,
`fileOfCode`, with tests) and `files.ts` (`FileDoc`) came along and are
unused. Its designs are `docs/desktop/file-panel-design.md` and
`content-actions-design.md` (§3.6, §4, DD-157, DD-161); both are built and
tested there (`e2e/file-panel.mjs`, `content-actions.mjs`).

What cannot be copied as it is:

| FinxWork | Why not here | Here |
|---|---|---|
| `pathInfo`: the main process `stat`s the paths | The file may be on another machine | `Resolve` (§6) |
| `files/list`, `files/read` to its own kernel | Coding sessions are hermeshd's, not her's | `List`, `Read`, same shapes |
| A page loaded from `file://` | Not on this disk | A scheme of the app's, answered from `ReadBytes` |
| PDF and tables through its `docs/*` kernel (`finx-docs`, `finx-sheet`) | Not in hermeshd | PDF by hermesh's own `PdfView` from bytes; tables later |
| Word and slides editors | Not this feature | Left out |
| No iPhone app | | §4 |

**t3code** (pingdotgg/t3code, read 2026-11-25) confirms the shape: tags
drawn by the client, everything read at the click, a page served for its
folder under a sandbox. It reads the file as it is now; hermesh can read it
as it was (§6.2).

## 3. The Mac

### 3.1 The tag

As FinxWork (content-actions §4.1), class `fref`:

- Icon (file or folder), name, grey `:42`. From a link: the link's words.
  From inline code: the code as written, in the code face.
- From a link it always shows, grey with "找不到这个文件" when it is not
  there. From inline code it shows only once it is found.
- Hover: the path within the project, and "第 42 行".
- Secondary click: 打开, 在侧边栏打开 or 查看源码, 拷贝路径, 拷贝相对路径;
  and on the session's own Mac 打开方式 ›, 在访达中显示.
- The block still being written has no tags (as today's `DraftContext`).

### 3.2 What a click does

FinxWork's rule (DD-161): work is read beside the conversation, a result is
read wide.

| Clicked | Opens |
|---|---|
| A result with no line: Markdown, picture (SVG too), PDF, page | The preview over the window (§3.3) |
| Code and other text; anything with a line | The file panel, at the line (§3.4) |
| What the app does not show: Office files, video, sound, archives | The panel's card: name, size, 存储到下载; on the session's own Mac 用默认应用打开 |
| A folder | The panel, the tree opened to it |
| A tool row that names a file, secondary click 打开 | The panel, at the line it read from |

### 3.3 The preview over the window

FinxWork's `FilePreview` (`.fpv`), which Her's preview already is:

```
┌─────────────────────────────────────────────────────────────┐
│ standalone-mode-mac-demo.html          </>  ◫  ⋯  ✕         │  name · 源码 · 在侧边栏打开 · 更多 · 关闭预览
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   the page, running, in a view of its own                   │
│   (Markdown as it reads · a picture · a PDF's pages)        │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│ 这是第 16 轮结束时的版本                    查看当前版本     │  only when it differs from now (§6.2)
└─────────────────────────────────────────────────────────────┘
```

A page runs as FinxWork's does (`main/browser/pagePreview.ts`): a
`WebContentsView` placed over the dialog's room, a process of its own, a
partition kept only in memory, every permission refused, downloads
cancelled, Esc handed back to the dialog. The one change: it loads
`hermesh-file://<session>.<run>/<path>`, which that partition answers from
`ReadBytes`, the page's folder and below and web kinds only. No change to
the app's own CSP, and no frame in the app's page.

### 3.4 The file panel

FinxWork's `FilePanel` (file-panel-design §3), on the session's right:

```
┌ conversation ───────────────┬ [engine.go] [erase.go] [demo.html]   </> ⋯ 🌲 │ ⤢ [📁] ┐
│                             ├──────────────────────────────────────┬───────────────────┤
│  …回复…  [engine.go:3341]   │ 3339      case runtime.EventAssi…    │ ▾ internal        │
│                             │ 3340        cur := e.ensureCont…     │   ▾ kernel        │
│                             │ 3341 ▌      if cur == nil {          │     engine.go  ●  │
│                             │ 3342          continue               │     service.go    │
│                             │                                      │ ▸ docs            │
│                             ├──────────────────────────────────────┴───────────────────┤
│  [composer]                 │ 这是第 16 轮结束时的版本                 查看当前版本      │
└─────────────────────────────┴──────────────────────────────────────────────────────────┘
```

- **The column:** dragged by its left edge, its width kept, 56% of the
  page at first (420 to 960), the conversation keeping 160 beside it; ⤢
  takes the window but the side bar. A 文件 button (folder) joins the
  session's toolbar left of the info button; while the panel is open it
  sits at the strip's right end and closes it. The info and background
  cards float over the conversation as today.
- **Tabs:** one per file, kind icon, name, close; the strip scrolls. Open
  tabs stay when the panel closes and when another session is shown.
- **The file:** CodeMirror 6, loaded when first needed, language by name:
  line numbers, folding, ⌘F, the linked lines marked and brought into
  view. Read-only in the first version (§11, decision 2). Markdown and SVG
  show as they look, with 源码.
- **The tree:** to the right, 200 wide, shown when the panel is 600 or
  more; a level read when opened, kept per folder, only rows in sight
  drawn; folders first, names in number order; `.git` and `.DS_Store`
  hidden; the open file marked. Arrows, Return, the tag's menu.
- **The bar at the foot** is FinxWork's notice bar, used here for the
  version (§6.2) and for "<machine> 现在连不上".
- **Kept fresh:** read again when a turn of the session ends and when the
  window comes back, as FinxWork rechecks its tags. No watching in the
  first version.

## 4. The iPhone

No FinxWork to copy; the same rules on the phone's own parts.

- **The tag:** the same in a reply: glyph, name, `:42`, grey when missing.
  Touch opens; touch and hold: 预览, 拷贝路径, 分享.
- **One place to open:** a pushed page, `SessionFilePage`. A phone has no
  "beside", so a result and code open the same way:

  | Kind | Shown with |
  |---|---|
  | Code, text | `HerCodeLines`, scrolled to the line, the line marked |
  | Markdown | `MarkdownText`, with 源码 |
  | PDF, picture, video, sound, Office | Quick Look |
  | Page | A `WKWebView` with a store that is not kept, as `HerConsoleWeb`, loading the same scheme through a `WKURLSchemeHandler` |
  | Anything else | Name, size, 分享 |

  Its bar: the name, 源码 where there is one, 分享; the version line at
  its foot as on the Mac.
- **The tree:** a 文件 button in the session's bar pushes "项目文件": a
  list with folders that open in place, the same order and hiding; a file
  pushes its page. On a wide iPhone (Duo open) and the iPad it is the
  column beside the file.

## 5. The front end, file by file

| FinxWork | Lines | Goes to | Change |
|---|---|---|---|
| `packages/client/src/refs.ts` | (here already) | `packages/core` | Moved from `her-client`; its cases shared with Swift |
| `session/scope.ts` | 91 | `session/scope.ts` | `useFileInfo` asks `Resolve` in one batch per reply, kept per session and turn |
| `session/content.tsx` (`FileTag`) | 232 | `session/content.tsx` | The tag; `Link` and `InlineCode` hand it their targets |
| `session/contentMenus.ts` | 232 | `session/contentMenus.ts` | Without the browser and the editors' list off the session's Mac |
| `preview/FilePreview.tsx`, `context.ts` | 247 | `session/FilePreview.tsx` | Joined with Her's; reads through a `FilesSource` (below); no tables |
| `main/browser/pagePreview.ts` | 113 | `main/pagePreview.ts` | The scheme in place of `file://` |
| `files/FilePanel.tsx` | 366 | `session/files/FilePanel.tsx` | Without Word, slides, tables; the version bar |
| `files/Tree.tsx` | 111 | `session/files/Tree.tsx` | As it is (`@tanstack/react-virtual` added) |
| `files/state.ts` | 576 | `session/files/state.ts` | About half: tabs, folders, targets; no documents, no watching yet |
| `files/Editor.tsx` | 239 | `session/files/Editor.tsx` | Read-only first (CodeMirror 6 added, loaded on demand) |
| `files/ImageView.tsx`, `FileCard.tsx`, `kind.ts` | 204 | `session/files/` | Card's buttons by whether the session is on this Mac |
| `panelWidth.ts`, `styles/files.css` | 290 | the same | Tokens are already the same |
| `main/files.ts` (`openFile`, `reveal`, editors) | 103 | `main/files.ts` | Only for a session on this Mac |

**`FilesSource`** is the one seam: `resolve`, `list`, `read`, `bytes`,
given a session and a turn. Its hermesh form calls `SessionFiles`; Her's
can call her `files/*` later, so Her's preview and this one are one
component.

On the iPhone: `HermeshSessions/FileRefs.swift` (the parser, same cases),
`Session/Markdown.swift` (the tag), `Session/SessionFilePage.swift`,
`Session/SessionFilesPage.swift` (the tree), with `HerCodeLines` and the
Quick Look wrapper moved out of `Views/Her` to be shared.

## 6. The back end

### 6.1 `SessionFiles`

`proto/hermesh/session/v1/files.proto`, served by the kernel. FinxWork's
methods with a turn added, so the copied state reads the shapes it knows:

| FinxWork | Here | What |
|---|---|---|
| `pathInfo(base, paths)` | `Resolve(session, run, paths[])` | For each: file, folder or nothing; its path within the folder; its size |
| `files/list` | `List(session, run, dir)` | Entries (`name`, `dir`, `link`, `size`), folders first, at most 5,000, `truncated` |
| `files/read` | `Read(session, run, path)` | `kind` (text, image, binary, too large), `size`, `hash`, the text decoded (UTF-8 with or without a mark; UTF-16 and GB18030 read-only), `encoding`. Text to 5 MB |
| (a page from `file://`) | `ReadBytes(session, run, path, offset, length)` | A range, at most 4 MiB: pages, PDF, pictures, video, Save |
| `files/write` | Later: `Write` with `base_hash` | Step 3 |
| `files/watch`, `files/changed` | Later: a stream | Step 3 |

`run` empty means the folder as it is now. Who may: `canViewSession`, as
`GetDiff`. The listing, the text decoding and the hash are a port of
`finx-core/src/files.rs` (350 lines, with its seven tests) to Go.

### 6.2 As it was: the checkpoint

Every turn ends (completed, interrupted or failed) with a Git tree of the
folder pinned under `refs/hermesh/checkpoints/<session>/<n>`
(`vcs.CaptureCheckpoint`): tracked and new files, not ignored ones. With a
`run`, the three requests read that tree (`git ls-tree`, `git cat-file`) in
the project's repository. Measured: the tree of turn 16 of the session in
§1 holds the demo, 64,686 bytes, read in 12 ms.

- A tag's file opens **as it was when its turn ended**: the line it names
  is the right line, and it opens after the worktree is released. The foot
  bar says so only when the file is different now, with 查看当前版本.
- The panel's tree and a file opened from it are **the folder as it is
  now**. When the folder is gone they are the last turn's tree, and the
  bar says "工作区已回收，这是第 N 轮结束时的文件".
- A turn still running, a file `.gitignore` hides, a folder that is not a
  repository: read as it is now.
- `resolveWorkdir` is not called for a read: it makes a released worktree
  again.

### 6.3 The owner layer, once for both apps

`internal/owner` is the same code in the Mac's hermeshd and in the iPhone
(`internal/mobile`). It answers `SessionFiles` to the app beside it:
routes as `GetDiff` (`resolveMachineForSession`, the local engine or
`dialRemoteSessionMachine`); keeps a finished turn's `Resolve` and `List`
answers and content by its Git id, up to 200 MB, beside `sessions_cache`.
What a device opened once opens with the machine away.

### 6.4 Telling agents

One sentence in the `hermesh` MCP server's instructions (`messagemcp.go`):
the owner may be reading on another device; link the files they should
open, and do not open them on this machine.

## 7. Order of building

| Step | Back end | The Mac | The iPhone |
|---|---|---|---|
| 1. A reply's files open | `Resolve`, `Read`, `ReadBytes` from a turn and from the folder; the owner's routing and keeping; the sentence to agents | The tag; the preview over the window (Markdown, picture, PDF, page; code as a code block) | The tag; `SessionFilePage` |
| 2. The panel and the tree | `List` | `FilePanel`, tabs, `Editor` read-only, `Tree`, the toolbar button; tool rows' 打开 | The tree page |
| 3. Changing files | `Write`, the stream of changes | Editing, saving, the conflict bar (`FileDoc`, already here) | Not planned |
| Later | | Tables, what a turn changed beside the file | The same |

Step 1 alone makes the reply in §1 open on both. Each step ships whole:
back end, both apps, its tests.

## 8. Not in this version

- Files outside the session's folder.
- Word, slides and table views; FinxWork's built-in browser.
- Choosing a default editor; searching the folder for a bare name.
- A `send_file` tool for an agent to hand a file over with its own words.

## 9. Safety

- A read from a checkpoint cannot leave the folder; a symlink in a tree is
  its text. A read of the folder as it is: symlinks resolved, inside the
  folder, at every call; never `.git`'s insides.
- Files `.gitignore` hides (`.env`): the owner only, as they are now.
- A page: a process of its own, nothing kept, no permission, its own
  folder only, and no network (§11, decision 3): on another device it
  would otherwise reach that device's network, which the agent's own shell
  cannot.
- Opening with an app, Finder, the editor: only for a session on this Mac,
  and FinxWork's rule that a file that runs when opened is only shown.

## 10. Tests

- Go: the port's cases (order, hiding, encodings, sizes); each request
  from a turn and from the folder; a symlink out, `..`, an ignored file as
  the owner and under a grant; a released worktree that stays released;
  the owner routing to another machine, a second read from what was kept,
  the machine away.
- TypeScript and Swift: `refs` cases, shared.
- End to end, from FinxWork's `file-panel.mjs` and `content-actions.mjs`,
  with the session on a second machine: a scripted agent writes
  `docs/demo.html` and `demo.js`, links them and `main.go:12`, and changes
  all three the next turn. The Mac (Playwright): the tags; the page opens
  over the window, its button works, its `fetch` fails; `main.go:12` opens
  the panel at line 12 with the tree at the file; the first reply shows the
  first content and 查看当前版本 the second; the worktree is released and
  the machine stopped, and both still open. The iPhone (`TestIPhone`): the
  same through the relay, and the tree.

## 11. The owner's decisions

1. **The click rule is FinxWork's**: results over the window, code in the
   panel. Recommended.
2. **The panel is read-only at first**; editing is step 3. Recommended:
   here the agent is the one changing files, the folder may be on another
   machine, and no lock is shared with Claude Code or Codex as FinxWork's
   kernel shares one with its own tools. The other way: editing in step 2.
3. **A page has no network**; 在浏览器中打开 on the session's own Mac.
   Recommended. FinxWork lets a page reach the network, on the same Mac.
4. **A tag opens the file as it was at its turn**, the bar offering the
   current one. Recommended. The other way: always the current file.
5. **Steps 1 and 2 as two releases**, or together.
6. **A deleted session's checkpoints are removed** (today they stay in the
   project's repository for good), and a device keeps 200 MB.

## 13. What is built (2026-11-25): steps 1 and 2

| Part | Where |
|---|---|
| Requests | `session.proto`: `ResolveFiles`, `ListFiles`, `ReadFile`, `ReadFileBytes` on `SessionService` (read access) |
| The machine | `internal/kernel/files.go`: the scope (folder, checkpoint tree, owner or grant), reading a turn's tree or the folder, FinxWork's order and decoding; `vcs/tree.go`: `TreeEntries`, `ReadBlob`, `Prefix`, `Ignored`, `RemoveCheckpoints` (a deleted session's refs go) |
| The owner layer | `internal/owner/files.go`: routing as `GetDiff`; every answer kept (`session_files_kept`, 200 MB, least recently used first), served when the machine is away; a turn's bytes served from what is kept |
| The rule both apps share | `packages/her-client/src/refs.ts` (FinxWork's, already here), `fileView.ts`; `HermeshSessions/FileRefs.swift`; cases in `packages/her-client/testdata/file-refs.json` |
| The Mac | `session/files/`: the tag (`FileTag`), the preview over the window (`FileView`), the panel (`FilePanel`, `Tree`, `panel.ts`, `panelWidth.ts`), the code (`CodeView`, CodeMirror read-only); `main/sessionFiles.ts`: the page's view and the `hermesh-file` scheme, Quick Look, Save, Finder, Open in Browser |
| The iPhone | `Views/Session/SessionFiles.swift`: the tag, the file's page (code, Markdown, a page in `WKWebView` through a scheme handler, Quick Look), the folder's pages; ⋯ › 项目文件 |
| Agents | `internal/machine/messagemcp.go`: one sentence: link the files, do not open them on the machine |

Differences from the plan:

- **The iPhone's tag has no symbol before its name.** An image in a SwiftUI `Text` shifts its links'
  places for VoiceOver by two characters (seen in the UI test's tree: `emo.html，`). The tag is the
  name in the link colour on a grey ground, as on the Mac otherwise.
- **The iPhone's tree is a page per folder**, the system's way, not folders opening in place.
- **The iPhone reads a page's files whole**; video in a page does not play by ranges there.
- **The owner keeps every answer**, not only a finished turn's: what a device saw last opens with the
  machine away. A turn's bytes are served from what is kept without asking.
- **A page may fetch files beside it** (`access-control-allow-origin: *` on the scheme): it has no
  origin of its own, and demos read their JSON that way. Still only its folder, no network.
- **A page has no source button** (the owner, after trying it on the phone: of no use). On the Mac
  the preview over the window has **Fill the Window** (铺满窗口), and the file panel runs a page
  instead of showing its source, its ⤢ taking the window with it; the main process keeps a page
  view per place (`dialog`, `panel`), the panel's hidden while the dialog's shows. On the iPhone
  there is no button for it (the owner: an iPhone's page is the screen's already, and a page over
  it was not how the app widens): on a wide screen, where the menu stands beside the page, the
  file's and the folder's pages have the button a session's page has (`WidenPageButton`), which
  shrinks the menu to its rail; checked on an iPhone 17 Pro and a Pro Max on its side.
- Not yet: tool rows opening files (§7 "Later"), a page reloading when its files change.

Verified:

- Go: `internal/kernel/files_test.go` (by turn and now, ranges, listing, a link out, `..`, `.git`, an
  ignored file as the owner and as a grant, a released worktree that stays released, refs gone with
  the session); `e2e/session_files_test.go` (this Mac and another machine through the relay, then
  that machine stopped).
- The shared cases in TypeScript and Swift.
- The Mac: `apps/desktop/e2e/session-files.spec.ts` on the built app, the session on another Mac
  (tags; the page runs, reads its folder, cannot reach the network; the turn's version and the
  current one; code at its line in the panel with the tree; tabs, expand, put away and back; the
  other Mac stopped and both open again).
- The iPhone: `HermeshUITests/SessionFilesUITests.swift` on the iOS 26 Simulator, the same session
  through the relay. Not checked on the iOS 27 device.

## 14. The side panel as tabs (2026-11-26, Mac)

Prototype: [side-panel-mac-demo.html](side-panel-mac-demo.html). The file panel becomes the **side
panel**, a place that will take more kinds of work (a terminal, a browser) as tabs, and the preview over
the window (§3.3) goes into it.

**The toolbar's right end** has two fixed buttons:

- **信息面板** (ⓘ, the app's `info` glyph; it was `infoCard`, too like the panel's own): the environment
  and context cards. It cannot be shown while the side panel is open: the button is greyed, ⌥⌘0 does
  nothing, and opening the panel puts the cards away (as before).
- **侧边面板** (`panelRight`, the side bar's glyph mirrored): shows the panel from the toolbar's right
  end while closed; while open (including maximized), it moves to the right end of the panel's strip
  after ⤢ so opening and putting away stay at the same corner of the window. The 文件 button is gone.

**The panel** (`session/files/SidePanel.tsx`, state in `panel.ts`):

- The strip: one tab per thing open (kind glyph, name, close), ＋ for a new tab (a native menu of
  kinds: today only 项目文件), ⤢ to take the window but the side bar, and the panel's toggle at its right
  end to put it away. Under it, the tab's own bar.
- Each session keeps its tabs. Opened empty, the panel starts with 项目文件; the last tab closed, it goes.
- **Files tab**: a file (code at its lines, Markdown and SVG as they look or their source, a picture, a
  PDF) with the folder's tree beside it, shown or hidden by the bar's tree button; hidden when a reply
  opened it. With no file it is 项目文件: the tree alone, files with their sizes, the folder in its bar.
  A file picked in the tree opens **in that tab**, as a browser goes to a page; the tree stays.
- **Page tab**: a `.html` running in its own view, as §3.3 had it (a process apart, no network, its own
  folder). Its bar is the page's address, 在浏览器中打开, and the file menu. It is where a built-in
  browser would go; back, forward and reload wait for that. A page picked in the tree opens its page tab.
- The foot keeps the version bar of a reply's file (§6.2).

**A click in a reply**: a picture, the Lightbox; Office, sound, video and archives, Quick Look; anything
else, its tab (a page tab for a `.html`, else a files tab, at its line, tree hidden), the tab it already
has brought forward. A folder: 项目文件, opened to it. The preview over the window and its 铺满窗口 are
gone; ⤢ takes their place.

**The page's view** is drawn over the app's own page, so it hides while something of the app's is over
the panel (a `<dialog>`, the Lightbox, a sheet: anything with `role="dialog"`) and shows again after.
With the dialog gone there is one view per window (`main/sessionFiles.ts`), and the page's Esc no longer
closes anything.

Verified: `apps/desktop/e2e/session-files.spec.ts` on the built app with the session on another Mac (the
page tab, max and back, the version bar; code at its line with the tree hidden, then shown; the tree
opening files in its tab and a page in its page tab; ＋ 项目文件 with sizes; put away and back; every tab
closed and the panel back with 项目文件; the page's view hidden under the Lightbox; the other Mac stopped).

## 15. The iPhone, narrow and wide (2026-11-27)

Prototypes: [side-panel-iphone-demo.html](side-panel-iphone-demo.html), [side-panel-iphone-v2-demo.html](side-panel-iphone-v2-demo.html).
The same ideas as the Mac's side panel (§14), in the iPhone's ways, by the screen it has (as the menu is, docs/iphone-app.md), not by the device.

**A narrow screen** (an iPhone; iPhone Duo upright or closed): pages are pushed.

- The session's bar gets no new button: 项目文件 is the first item of ⋯, a folder a page, as before.
- A reply's file pushes its page; a `.html` pushes a page that runs (no network, its own folder).
- A file's page (`SessionFilePage`): its name only at the top (where it is, is on the drawer's bar); ⋯ with Share…
  and Copy Path; 源码 for Markdown and SVG. A page's page has its folder under its name instead, as it has no tree.
- The tree's drawer: a glass bar at the page's foot (项目文件, the project and folder), pulled up into a sheet
  (medium and large detents) with the tree, folders opening in place, the file marked and its folders open. A file
  picked there opens in this page, as the Mac's tab goes to it, and the sheet goes down; a `.html` is pushed; a
  picture opens in the Lightbox. A page's page has no drawer.
- The version bar (§6.2) sits above the drawer's bar.

**A wide screen** (regular width, on its side: iPhone Duo open, a Pro Max turned): the session's side panel
(`SessionSidePanel`), beside the conversation, as on the Mac.

- **Hiding and showing**: the bar's 侧边面板 button (`sidebar.right`, switching to the panel-hide glyph when open)
  shows and puts away the panel while keeping its open tabs; opening it shrinks the menu to its rail (`menuRail`, as
  `WidenPageButton` does) to give it room, and an empty panel starts with 项目文件. Swiping right from the panel's
  leading edge puts it away too.
- **iPhone Duo (`barsOnTheSide`)**: the split between the conversation and the side panel aligns with the physical
  center fold (`screenWidth / 2`). Window and contextual panel controls move into the system's right vertical bar
  (`ToolbarItemGroup(placement: .topBarTrailing)` with `.visibilityPriority(.high)` and
  `.axisBehavior(.verticalPreferred)`): a high-priority window capsule (收起侧边面板, 放大 / 缩小), a contextual tab
  capsule (＋ 新建标签页, Markdown / SVG 源码), and a single sectioned `⋯` menu (the active file's 分享… and 复制路径 over
  当前会话's items, avoiding duplicate `⋯` buttons). The panel's own top is then a single slim strip of glass tabs.
- **Other wide screens** (a Pro Max turned, bars along the top): a compact glass capsule at the trailing end of the
  tab strip holds ＋, 放大 / 缩小 and 收起侧边面板 (so an enlarged panel still hides in one tap), beside 源码 and ⋯.
- **Unified bottom tree drawer**: instead of a 200 pt column beside the file (which crushes code and deep folder names
  on a phone), a files tab gives the file the panel's full width and places the same `SessionFileDrawerBar` at its
  foot; pulling it up raises `SessionPanelDrawer` within the side panel (medium and large detents by dragging the
  grabber, `⌄` to close), and a file picked there opens in the tab and closes the drawer.
- A reply's file, ⋯ › 项目文件, and a folder tag open tabs (or the drawer) here instead of pushing pages. The tabs and
  the tree are the session's (`SessionFilesModel`), kept while its page is away.

**The owner's own files**: a `.html`, `.svg`, Markdown, code or text sent with a prompt opens as a reply's would: its
page (`SessionAttachmentPage`, named with 附件 · its size) on a narrow screen, a tab on a wide one. A page runs on its
own (`AttachmentWeb`): only the one file was sent, so nothing beside it can be read. Anything else stays in Quick Look,
and pictures in the Lightbox.

Verified: `HermeshUITests/SessionFilesUITests.swift` (`testSessionFiles`) on an iPhone 17 Pro and a 17 Pro Max on its
side, iOS 26 Simulator, the session on another Mac through the relay: the drawer, the tree opening a file in place, ⋯;
the side panel's tabs, the tree in a tab, enlarge and shrink. Not checked on the iOS 27 device (iPhone Duo). The
attachment page has no UI test yet: the test world sends no non-picture attachment.

## 16. The owner's own files, on both apps (2026-11-27)

A file sent with a prompt opens as a reply's would, on the Mac too (it did nothing there before):

| The file | The Mac | The iPhone |
|---|---|---|
| A picture | The Lightbox, no menu (session-images.md §5) | The same |
| A page, SVG, Markdown, code, text | A tab of the side panel (`attachment` kind): 附件 · its size in its bar; a page with 在浏览器中打开; ⋯ › 存储到“下载” | Its page (narrow) or a tab (wide); ⋯ › 分享… |
| Anything else | Quick Look | Quick Look |

- The bytes come from the blob the machine keeps (`DownloadBlob`). On the Mac a `SessionFileRef` may name one
  (`blob: { machine, sha }`, `path` its name), so Quick Look, Save, the page's view and Open in Browser take an
  attachment as they take a file; a page is served at `hermesh-file://blob/<session>/<machine>/<sha>/<name>`, itself
  only.
- An attached page refers to files beside it (a script, a style sheet, a picture by a relative address:
  `refersToOwnFiles` on both apps, the same rule): only the page was sent, so a bar under it says
  「只发送了这一个文件，页面引用的其他文件不在这里」.
- On the iPhone a picture or file in the box, before it is sent, opens too: a picture in the Lightbox, a page,
  Markdown or code on its page (its bytes are on the iPhone already).

Verified: `apps/desktop/e2e/session.spec.ts` (a dropped `notes.md` and `page.html`, read back after a reload: the
tabs, the page running, the bar), `HermeshUITests/SessionAttachmentsUITests.swift` (the page, Markdown, the picture;
the panel's tab on a Pro Max on its side), `ThreadComposerUITests` (the photo in the box).
