# When devices and machines run different versions

Status: accepted 2026-11-15 with the owner's decisions (§9). Built the same day: §6.1–§6.3, and §6.6 for
an assistant's memory. Not built: the waiting row of §6.4, §6.5, Update Now from a device.

## 1. Goal

The owner's devices (iPhone, the Mac and Windows apps) and the machines they
reach update on their own schedules, so running different versions is normal,
not a failure. When one side asks for something the other side doesn't have
yet, the owner sees what is going on (which side is behind and what it is
missing) and one way to fix it. The screen then recovers by itself once that
side is updated. A protocol error never reaches the screen.

The case that started this: the iPhone was updated before the Mac. Opening an
assistant's memory showed "读不到记忆 / unknown method 'bots/memory'". The
Mac's `her` was from 9486bda, which has no `bots/memory` (7ccd92ce added it).
Ten minutes later the Mac's app updated, and the same screen worked.

## 2. Where versions differ, and for how long

| Side | How it updates | Behind for |
|---|---|---|
| iPhone app (and its core, `cmd/hermesh-mobile`) | TestFlight or the App Store, when the owner allows it | Days |
| Mac app (with its `hermeshd`, `her` and signer) | Squirrel.Mac on quit or restart ([desktop-app.md](desktop-app.md) §6.5) | Hours to days |
| A Mac app's own `hermeshd` | Restarted by the new app, after any running session ends (desktop-app.md §6.5) | Minutes, or until a session ends |
| Machines from `install.sh` | Every 6 hours, after their sessions end ([machine-updates.md](machine-updates.md)) | Up to a day, or for good when automatic updates are off |

Version skew therefore goes both ways: a device can be newer than a machine,
and a machine can be newer than a device.

## 3. What happens today

A device reaches a machine's abilities through three hops, and each one says
"I don't have this" in its own way:

| Hop | What a missing ability looks like | Where it comes from |
|---|---|---|
| `her` | JSON-RPC `-32601`, `kind: method_not_found`, "unknown method 'x'" | `her-rpc` `RpcError::method_not_found` |
| `hermeshd`'s gate for her (`internal/herhost/allow.go`) | Connect `permission_denied`, "her's "x" is not open to the owner's devices" | An older `hermeshd` whose list lacks a newer method |
| `hermeshd`'s own services | Connect `unimplemented` | A service or method this version lacks |

What the devices do with these:

- **Handled in four places only:** the folder browser (`unimplemented` → "too
  old to browse its folders"), the iPhone's and the desktop's doorbells
  (`unimplemented` → hidden), and a secure card the app can't show ("Update
  Hermesh to answer this request", the reverse case).
- **Everywhere else the message is shown as it came.** The iPhone calls 17 of
  her's methods and the desktop calls 24, across memory, routines, cards,
  sign-ins, connectors, the browser and conversations. Through its core, the
  iPhone also calls 57 RPCs, all of them shown raw.
- **Version numbers can't tell what a machine can do:**
  - `her` reports `0.2.0` from both 9486bda and 219d2fc.
  - Builds that are not releases (`0.1.5-dev.<commit>`) compare equal to their
    release.
  - The machine page's `isOlder` compares the app's version with the
    machine's, so it can't flag a machine whose missing features came from a
    development build.
- **Nothing recovers by itself.** The memory screen showed the error until it
  was opened again, even after the Mac had updated.

## 4. What others do

- **Capabilities in the handshake:** LSP, MCP and her's own `initialize` (its
  `ServerCapabilities` already carries `session_delete`, `bots`, `routines`
  and others). The client asks what the server can do. It doesn't infer that
  from a version number.
- **Discovery:** the Kubernetes API lists the resources and verbs a server
  serves. Clients like `kubectl` use that list to decide what to offer.
- **Saying which device is behind:** Apple's Notes and Reminders name the
  device that needs updating before a new feature can be used. Chat apps show
  "update the app to see this message" for a message kind the app doesn't
  know.

## 5. Principles

1. **Ask what it can do, not what version it is.** Abilities are discovered,
   never inferred from a version number.
2. **Derived, not kept by hand.** What a machine says it can do comes from
   what it actually serves: her's dispatch table, `hermeshd`'s gate list and
   its registered handlers. It can't drift from the code.
3. **Missing is a state, not an error.** The state names the machine, says
   which side is behind and what it is missing, and offers the one thing to
   do.
4. **In one place.** Each client has one gate and one safety net (§6.3).
   Screens don't each catch error codes.
5. **It recovers by itself.** When the machine's abilities change, screens
   that were waiting for them load again.
6. **Both directions.** A device that is behind a machine says so too (§6.5).

## 6. Design

### 6.1 A machine says what it can do

- **her:** `initialize`'s result has `methods`, the methods this `her`
  answers: the requests of `her-protocol`'s schema, but those it describes
  ahead of the kernel (`NOT_SERVED` in her-server's `native.rs`: `ai/action`
  and most of `deck/*`). A test (`initialize_lists_what_it_answers`) checks
  the list is exact both ways: every method listed is dispatched, and every
  other request of the schema is an unknown method.
- **hermeshd:** `HerStatus.methods` is what the owner's devices may call
  there: her's `methods` that its gate (`internal/herhost/allow.go`) lets
  through. It travels with the machine's status to the devices, and the
  owner's device keeps it with the rest of `Machine.her`.
- **Empty means unknown, not "nothing":** her is not running there, or the
  machine is too old to list its methods. The gate then lets the call through
  and the safety net (§6.3) catches what is missing. Every machine updated
  after this ships lists them.
- **Not listed:** hermeshd's own RPCs. No feature gates on one yet, and the
  safety net covers them; a list comes when a feature needs it, derived from
  the handlers hermeshd registers, not kept by hand.

### 6.2 One gate in each client

- **The check:** `herAnswers(machine.her, "bots/memory")` gives true, false or
  unknown (nil on the iPhone, undefined on the desktop). Unknown is taken as
  yes. It lives in HermeshHer (`Environment.swift`) and in the desktop's
  `skew.ts`.
- **What false looks like:**
  - The entry point stays visible, with a "需更新" mark. Tapping it explains
    instead of failing; Clear Memory says the same instead of asking to
    confirm. Hiding it would leave the owner wondering where the feature went.
  - The screen shows the Needs Update view: "需要更新 Hermesh", the machine
    named, and that the page loads by itself once Hermesh there is updated
    (`NeedsUpdateView` on the iPhone; on the desktop, the memory panel).
- **Built for an assistant's memory,** the feature that showed the problem.
  Routines and cards need no gate: they only appear when the machine's her
  reports them.

### 6.3 The safety net

Each of these means the machine is older than the app:

- her's `method_not_found`;
- `unimplemented` from a machine's RPC;
- `unimplemented` from hermeshd's gate, for a method not on its list. Until
  now the gate answered `permission_denied`; that is kept for refusals by
  policy, such as a connector started as a command, or a card's op off the
  list. The console's HTTP route answers 501.

Where the error is made, the text becomes "那台电脑上的 Hermesh 版本较旧，还没有这项功能。
在那台电脑上更新 Hermesh 后再试。" in place of the transport's words. On the
iPhone, that is `APIError.received` (HermeshKit) and `HerRPCError` (HermeshHer).
On the desktop, it is `errorText`, through `needsUpdate` in `skew.ts`. Every
screen and toast that shows an error reads right, gated or not. The codes
stay, for the screens that act on them: the folder browser and the doorbells.

### 6.4 What the Update button does

| Behind | The owner sees | The action |
|---|---|---|
| A Mac with the app | "<Mac> 上的 Hermesh 需要更新才能使用记忆" | Today: open Hermesh on that Mac (it updates on restart). Later: Update Now from the device (machine-updates.md §8) |
| A Mac with the app, the new version installed, its `hermeshd` waiting for a session to end | "<Mac> 已下载新版本，当前会话结束后自动完成更新" | None needed. It shows progress, so it doesn't read as a failure |
| A machine from `install.sh` | The same wording | The command sheet that exists (`MachinesView` "Update Hermesh") |
| This app | "此 App 需要更新" | The App Store or TestFlight page; on a Mac, its updater |

The waiting row is not built. On a Mac with the app, it is the new app that
waits for sessions to end before restarting `hermeshd`
(desktop-app.md §6.5). Meanwhile the old `hermeshd` answers the devices, and
it can't know an update is waiting. The new app would have to tell it, and
an old `hermeshd` has no call for that. For a machine installed with
`install.sh`, its own updater knows (machine-updates.md §3.1), so it could
report this.

### 6.5 The other direction: this app is behind

- **Content the app doesn't know** (a card kind, an event type, an enum value)
  shows as one row, "更新 App 以查看此内容". This makes general what
  `HerCards` does today.
- **On the machine page:** a machine newer than the app shows "此 App 有新版本"
  with the store link. A machine older than the app keeps the Update button it
  has today, now driven by `abilities` rather than by version numbers alone.

### 6.6 Recovering by itself

The memory page reloads when the machine's `methods` change and when her's
connection comes back: an update restarts her. Before it reloads, it waits
for the connection to be up. Otherwise the first call after her restarts
would fail as "can't reach" and stay that way.

On the iPhone, the memory page holds the environment's connection itself.
SwiftUI cancels the tasks of the pages a pushed page covers, and those held
it. Without its own, the memory page loaded only by winning a race with that
cancellation, and never once her restarted.

### 6.7 A machine from before Her

The case: an iPhone turning Her on for a machine whose `hermeshd` was 0.1.4
or older. Face ID signed `SetHer`, and the machine answered
`invalid_argument` "unknown admin command", shown as it came. `SetHer` and
`MachineStatus.her` both came in 0.1.5 (6093c626, a4422506), and every
`hermeshd` since sends `her` in its status. So a machine that answered,
without `her` in its status, can't run Her until it is updated. That comes
from what it serves, not from its version number (§5.1).

- **Where Her runs** (the iPhone's `herChoices`, the desktop's, and the
  desktop's switch in Settings › Machines): such a machine stays listed,
  saying "这台电脑上的 Hermesh 版本较旧，在那里更新后才能运行 Her", and can't
  be chosen. What else a machine says of Her (`unsupported`) is read from its
  status too: `Machine.her` is kept only once Her was on there, so a machine
  Her was never on has none. Updated, its status has `her`, and the row offers
  Her by itself. A machine whose status hasn't come yet is unknown, and may be
  chosen, as §6.2 has it.
- **Before Face ID:** turning Her on asks the machine for its status first
  (`Owner.SetHer`). One from before Her answers `unimplemented`, and one that
  can't be reached `unavailable`, with no signature asked for.
- **The safety net:** a command a machine doesn't know is `unimplemented`
  from now on. Machines up to 0.1.5 said `invalid_argument` "unknown admin
  command"; the owner's devices take that answer as `unimplemented` too, so
  §6.3's text shows in its place.
- **Tests:** `HERMESH_TEST_BEFORE_HER=1` makes a `hermeshd` act as 0.1.4: no
  `her` in its status, and her commands answered as 0.1.4 did.
  `TestHerOnAMachineFromBeforeHer` (through LocalService),
  `TestDesktopHerBeforeHer` (`her-before.spec.ts`) and the iPhone's
  `testHerOnAMacFromBeforeHer` (`TestIPhone`, control route `before-her`).

## 7. Building it

1. **Built:** her's `methods`, `HerStatus.methods`, and the gate answering
   `unimplemented`.
2. **Built:** the safety net and the Needs Update view, on the iPhone and the
   desktop.
3. **Built:** the gate and the "需更新" mark for an assistant's memory, and
   the memory page recovering by itself.
4. **Later:**
   - the waiting row (§6.4);
   - the reverse direction (§6.5);
   - Update Now from a device (machine-updates.md §8).

## 8. Tests

- **Unit tests:**
  - her: the list is exact both ways (`e2e_native.rs`).
  - hermeshd:
    - `HerStatus.methods` from a real her: `bots/memory` listed,
      `server/shutdown` not (`TestHost`);
    - the gate list ∩ her's methods, with nil staying nil (`TestOpenTo`);
    - the gate's `unimplemented` kept apart from refusals by policy
      (`TestCheck`, `TestCardsAnswer`, `TestConsoleRefusals`).
  - The iPhone's package (`KitTests`, `HerTests`) and the desktop
    (`skew.test.ts`): the safety net maps each of the three errors, and only
    those; nothing listed is unknown.
- **End to end, as the owner uses it.** `HER_TEST_WITHOUT` (debug builds of
  her only) names a file of methods her acts as if it lacked. Its line
  `no-list` makes her list none, as a her from before the list. her reads the
  file when it starts. The tests restart her by `server/shutdown`, and
  hermeshd starts it again, as after an update. Three phases:
  - a her from before the list, without `bots/memory`: opening the memory
    shows the Needs Update view naming the machine, never "unknown method";
  - one that lists its methods without it: the row shows "需更新", and Clear
    Memory explains without asking to confirm;
  - updated: the open page loads the memory by itself.

  Desktop: `TestDesktopHerOlder` (`her-older.spec.ts`). iPhone: `testHer`'s
  `herOlder` (`TestIPhone`, control route `her-without`).

## 9. The owner's decisions

1. The entry point of a missing feature stays visible, with a "需更新" mark.
2. Update Now from a device comes later, as machine-updates.md §8 has it.
3. The first cut is steps 1–3 of §7.
