# Hermesh desktop: smoothness, metrics and results

Goal: the main window (the overview and the session page) should feel as
smooth as the machine allows: open fast, answer every click in a frame or
two, cost next to nothing while it only shows what is going on, and keep its
memory flat however long it runs. This document defines how that is
measured, what the best possible value of each metric is, and what the app
reaches, with the changes that got it there and the ones that were tried and
left out.

Test machine: Apple Silicon MacBook Pro, 120 Hz display, macOS 27, Electron 44.

## 1. The benchmark

`pnpm --filter @hermesh/desktop perf` builds the app and runs
`apps/desktop/perf/bench.spec.ts`. Results go to `apps/desktop/perf/results/<PERF_LABEL>.json`
and to the console; `node apps/desktop/perf/compare.mjs a b` or
`base=a1,a2,a3 new=b1,b2,b3` (medians) puts runs side by side.

- **A heavy owner's world** (`perf/world.ts`): 4 machines (one offline), 6
  grants, 12 projects, 402 sessions over two months (10 running, 2 waiting
  on a question, 3 failed, 40 archived), a year of usage, and two long
  conversations of 40 turns and about 2,700 events each (one finished, one
  running). Everything is generated from a fixed seed: every run sees the
  same world.
- **A stand-in for hermeshd** (`perf/daemon.ts`) serves that world on a Unix
  socket with the calls the main window makes. While "live" it changes the
  world as busy machines do: a running session every 100 ms (hermeshd pushes
  at most that often per machine) and an event every 250 ms on the running
  long conversation.
- **The floor** (`perf/floor/`): Electron opening a window of the same kind
  (size, title bar, vibrancy, sandbox) with a static page and no app code.
  What it measures, no app can beat; `floor.anim` adds one small compositor
  animation, the least anything moving can cost.
- Options: `PERF_REPS` (default 5), `PERF_SECONDS` (steady windows, default
  15), `PERF_ONLY=start`, `PERF_AB=<out A>,<out B>` (startup of two builds
  launched in turn, with the median of the paired differences),
  `PERF_PROFILE=<window>` (a CPU profile, summarised by `perf/profsum.mjs`),
  `PERF_TRACE=<window>` (the renderer main thread's work by kind),
  `PERF_MUTATIONS=1` (which elements the page changes).

Each app launch has its own profile and its own Chromium flags to keep a
covered window drawing (`--disable-backgrounding-occluded-windows`).

## 2. Metrics and their best possible values

| Metric | What it is | Best possible | Target |
|---|---|---|---|
| `start.ms` | Launch to the frame showing the overview with its data (list, map, cards, usage), seen by an observer in the page | `floor.start.ms` plus one round trip and one render | floor + 150 ms |
| `start.renderer.ms` | The part from the renderer's navigation start | `floor.start.renderer.ms` + script + data | — |
| `open.long.ms` | Click on the 2,700-event session to the frame showing all of it | streaming its events (measured alone: 26–55 ms, mostly the stand-in daemon) + one frame | ≤ 100 ms |
| `switch.session.ms` | Click on another small session to the frame with its conversation | one round trip + one frame (≈ 10 ms) | ≤ 33 ms |
| `back.overview.ms` | Click on 概览 (Overview) to the frame with the map | one frame + the map's layout | ≤ 50 ms |
| `palette.open.ms` | Click on 搜索 (Search) to the frame with its field | one frame | ≤ 33 ms |
| `inp.*` | Event Timing (input to next frame), clicks / typing / hovering | one frame (8 ms at 120 Hz) | p50 ≤ 33 ms, max ≤ 100 ms |
| `*.renderer.busy%` | Renderer main thread busy (all tasks) while nothing is clicked | what must change on screen: clocks once a second, appended events | quiet ≤ 1 %, live ≤ 5 % |
| `*.cpu.total%` | CPU of every process of the app (main, renderer, GPU) | `floor.idle` when nothing moves; `floor.anim` and up for the motion the design has | as close to `floor.anim` as the motion allows |
| `*.mutations/s` | DOM changes per second | only the clocks and new events | exactly that |
| `*.loaf>50ms`, `*.slowFrames` | Long animation frames, frames over 1.5 vsync | 0 | 0 |
| `*.footprint.*.MB` | macOS footprint (Activity Monitor's memory) per process type | `floor.footprint` + the data | — |
| `leak.*` | Heap and DOM nodes after opening 21 sessions and coming back, after GC | 0 | ≈ 0 |

Windows (`overview.quiet`, `overview.live`, `session.live`, `session.quiet`) are
15 s each, with every session in the list loaded (a heavy user scrolled
back): the overview with nothing changing, the overview while the machines
work, the running long conversation while it grows, and a finished
session's page.

## 3. Results

Before (bcb6b77) and after, each the median of three runs made in turn
(before, after, before, …) on AC power; startup from a paired A/B of 21
launches each. Lower is better everywhere.

| Metric | Floor | Before | After |
|---|---|---|---|
| Overview, nothing changing: renderer busy | 0 % | 94.7 % | **5.2 %** |
| Overview, nothing changing: CPU, all processes | 6.2 % (`floor.anim`) | 131.5 % | **25.1 %** |
| Overview, nothing changing: forced layouts / s | 0 | 7,297 | **1** |
| Overview, nothing changing: DOM changes / s | 0 | 14,618 | **22** (the clocks) |
| Overview, machines working: renderer busy | — | 97.1 % | **8.6 %** |
| Overview, machines working: CPU, all processes | — | 136.1 % | **30.1 %** |
| Long running conversation growing: renderer busy | — | 20.3 % | **11.2 %** |
| Long running conversation growing: CPU, all processes | — | 43.1 % | **35.6 %** |
| Finished session's page: CPU, all processes | 6.2 % (`floor.anim`) | 18.2 % | **16.0 %** |
| Open the 2,700-event conversation | ≈ 30–55 ms (data) | 109 ms | **90 ms** |
| Switch to another session | ≈ 10 ms | 59 ms | **17 ms** |
| Back to 概览 (Overview) | — | 95 ms | **60 ms** |
| Open the search palette | 8 ms | 36 ms | **23 ms** |
| Clicks, input to frame (p50 / max) | 8 ms | 40 / 72 ms | **32 / 88 ms** |
| Typing in the palette (p50 / max) | 8 ms | 32 / 64 ms | **24 / 48 ms** |
| Scrolling the long conversation | 120 fps | 117 fps | 118 fps |
| Long animation frames (> 50 ms), any window | 0 | 0 | 0 |
| Load every session in the list | — | 1,639 ms | **1,364 ms** |
| Startup, renderer's part (paired A/B) | — | 284.6 ms | 282.2 ms (−1 ms) |
| Memory after visiting 21 sessions, after GC: heap / DOM nodes added | 0 | +1.9 MB / +73 | +2.4 MB / +73 |
| Renderer footprint 3 s after startup, no forced GC | 21 MB | 158 MB | **136 MB** |
| Renderer footprint right after a forced GC | 21 MB | 99 MB | 114 MB |

Notes:

- The overview was the problem: with nothing changing it kept more than a
  core busy. It now costs about four times the floor's single animation,
  for about ninety animations on screen.
- Startup is unchanged overall: the code cache saves 24 ms, and the largest
  remaining item in the renderer is its first `localStorage` read (§6).
- Memory: the old comet loop made garbage every frame, so without a forced
  GC the renderer is 20 MB smaller now; right after one it holds 15 MB more
  (the animations' tracks and the compositor layers of the moving dots). No
  leak either way.
- After rebasing onto the localized app (3664e15), one pair of runs shows the
  same picture: the overview 95 % → 9 % busy, the growing conversation
  52 % → 17 % (translated rows made the old re-renders heavier).

## 4. What changed

### Motion drawn by the compositor, not by the page (the overview)

The map's comets were moved by a `requestAnimationFrame` loop that, every
frame, called `getTotalLength()` and `getPointAtLength()` after
`setAttribute` on 120 SVG circles: 60 forced layouts a frame, 7,300 a second
at 120 Hz, the renderer's main thread 82 % busy with nothing changing.
Three CSS animations ran on the main thread too (the card sweep animated
`background-position`, the 今天 pill's beat `box-shadow`, a question's ping
the SVG `r`), and any one of them makes Chromium run its whole frame pipeline
on the main thread at the display's rate.

Now every animation is `transform` and `opacity`, which the compositor runs
by itself:

- each comet dot is a small element with a Web Animations track sampled once
  from its link: the link's curve is flattened and resampled by length
  (1 px), then simplified (Douglas–Peucker in time) to the points where a
  straight move between them stays within 0.05 px of the curve (about 96
  keyframes a dot). Its phase is pinned to the document's timeline exactly
  as the loop computed it; each dot fades with the comet (dots of a tail
  never overlap, so this looks the same as fading the group, without an
  offscreen surface per comet). The tracks are made after the map is drawn
  and kept for when it is drawn again;
- the card sweep is a half-width bar moved by `translateX`, the pill's beat
  a disc of the dot's colour scaled out of it, a question's ping an element
  scaled from 0.8 to 3.2, the hub's aura an HTML ellipse with the same
  radial gradient.

`e2e/motion.spec.ts` checks it against real daemons: every animation on the
overview animates only `transform` and `opacity`; each dot is where the old
loop put it (worst 0.09 px, opacity exact); and while nothing happens the
page changes nothing but its clocks. Both tests fail on the code before.

### Render only what changed

- **What changes nothing renders nothing.** The status was polled every
  second and the lists every three, each answer replacing the store's data,
  so the whole window rendered every second. The window now follows
  `Watch` ([state-sync.md](state-sync.md)), which sends only what changed,
  and a message that changes nothing keeps the old objects (`applyWatch`).
- **One clock per interval.** Each elapsed time had its own `setInterval`, at
  its own phase: up to 25 separate renders and frames a second. `useNow` now
  shares one clock per interval, so all of them change in one frame (also
  checked by `motion.spec.ts`).
- **Memoised rows.** The 400 sidebar rows, the conversation's rows and the
  usage heatmap render again only when what they show changed. A row of the
  conversation was re-rendered, its Markdown parsed again, on every event and
  every push; the items are rebuilt from the events each time, so rows are
  compared by the events they show (which never change).

### Faster to open

- **V8 code cache** for `app://` (`codeCache` privilege): the renderer's
  compiled script is kept between launches (−24 ms, paired A/B).
- **A conversation shows as soon as it is all in.** Events were added in
  30 ms batches, so every session opened at least 30 ms after its events
  arrived. The events up to the session's `last_event_seq` now show at once
  (live events are still batched). `session.spec.ts` checks the conversation
  appears whole, never in pieces.

## 5. Tried and left out

| Idea | Why not |
|---|---|
| `content-visibility: auto` on conversation rows | Always applies paint containment, which would clip the copy button that sits outside a user's message bubble: a visible change |
| CSS `offset-path` for the comets | Exact and two keyframes a dot, but Chromium runs it on the main thread every frame (1,083 main frames in 3 s against none for `transform`) |
| Minifying the renderer bundle | −3 ms at startup with the code cache (paired A/B), not worth unreadable stack traces |
| Watching sessions before the first status answers | +8 ms: more requests in flight during the first render |
| Asking for usage only once the sessions are in (one request fewer) | +9 ms to the first full view |

## 6. What is left

- **Motion costs what the design asks for.** With about 90 compositor
  animations on the overview (comets, card sweeps, status pings), the
  renderer's compositor and the GPU draw at 120 Hz; the main thread is idle
  apart from the clocks. Fewer moving things is the only way lower.
- **Opening the long conversation** is dominated by streaming its 258 KB of
  events (the stand-in daemon, written in JS, is slower at this than
  hermeshd) and laying out about 1,700 rows once.
- **Memory**: the renderer's footprint is mostly allocator and V8 regions
  around a 9 MB heap; nothing grows with use. Going lower would mean V8 flags
  that trade CPU for memory.
- **Startup**: the renderer's part is script evaluation, the first renders
  and one round of requests to hermeshd; the rest is Electron itself
  (`floor.start.ms`). The largest single item is the first synchronous
  `localStorage` read (first-run progress, the info panel's visibility),
  10–55 ms depending on the disk: the renderer waits while the storage
  backend opens. Keeping those few values with the main process (and handing
  them to the page at launch) would remove it, but needs a migration for
  existing installs.

## 7. Measuring on a laptop

- Absolute CPU numbers move with what else the machine does (the floor's own
  animation ranged 4–12 % between runs). Compare runs made the same way:
  `PERF_AB` for startup, and builds run in turn (A, B, A, B, …) for
  everything, then medians.
- Playwright's `page.waitForSelector()` returns an element handle that keeps
  its node, and with it a whole detached page, alive: the first runs showed
  a "leak" of 10,000 nodes per opened conversation that was the benchmark's
  own. Use `locator.waitFor()`.
- A CPU profile samples at 5 kHz and wakes the renderer accordingly: windows
  that are profiled don't count.
