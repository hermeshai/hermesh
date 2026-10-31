# The iPhone app

> Status: first build (2026-10-22). A native SwiftUI app for iOS 26, with the
> Mac app's features except hosting itself as a machine.

The iPhone is a **trusted device** of its owner (trusted-devices.md): it
holds a root key, approves and signs, and sees every machine's sessions. It
is never a machine: nothing runs on it, and it hosts no agent.

## 1. What it does

| Place | What it shows |
|---|---|
| Side menu | The logo and search at the top; Overview, Grants, Machines; the sessions by state (Needs You, In Progress, Recent), All Sessions (a running session's title shimmers there and in every list, a band across it every 3.2 s, instead of a dot or a spinner); at the bottom the account, a round picture alone: on the official service the account's picture from its sign-in provider (the center's `GetAccount`, asked on each connection), else its initial; on a server of one's own, a server |
| Search | A page of its own, the field at the top as the App Store has it: the recent searches, the states to search within (a token in the field) and the recent sessions; while typing, completions and the sessions that match. Cancelling closes it |
| Overview | Its title small in the bar, the range control the top of the page (the sync line takes the title's place while it shows); To Do: requests to connect, grants that end, and how many sessions wait (it opens the menu, which lists them); the work over a range picked with a glass segmented control (Today, 7 days, 30 days): the sessions that worked and those done, turns and tokens, what runs now (a live label), the sessions working each hour (today) or day; who asked which coding agent for the work (authorized clients on the left, coding agents on the right, each band as thick as its turns; tapping one side picks it, tapping a band says how much, and the clients that sent nothing are named under it), each coding agent with what it runs, on how many machines, its turns and its hours or days as heat cells (as deep as its sessions at work, now ringed in the accent, those to come hollow; a Less–More legend, and now's time or day under the cells; tapping a cell says whose it is, when, and its sessions) (all of them, those that worked first), machines counted only up to their last sync, the machines, unreachable machines. The counts come from the core's copy of every machine's (`QueryWork`, [work-stats.md](work-stats.md)) |
| All Sessions | Laid out as the Mac's sidebar: the view menu groups (by status, date, coding agent or machine), sorts by last activity and shows or hides each row's metadata; a group's heading folds it; archived sessions on request; kept on the phone. Swipe a row to archive or delete it, long-press to rename, archive or delete |
| A session | Its conversation, newest at the bottom, as the Mac shows it: the owner's messages in a dark bubble on the right, the agent's replies as GitHub Markdown with code in GitHub's colours, tool calls as grey lines folded until opened (a run of them one line, opening to a card with a row each; a row opens to its whole output); long-press a message to copy it. A control bar to interrupt a live turn, or the agent's question to answer; session info and the diff in a sheet; ⋯ to copy its id, rename, archive or delete it (delete asks first; its branch stays) |
| Grants | Agents and MCP connections: approve, renew, rename, disconnect, pause; each grant's sessions on a page of their own |
| Machines | The machines, a green shield on those that are trusted devices too, then the other trusted devices (which approve but run nothing; remove one with a swipe), each machine opening its page: its coding agents (signed in, projects, defaults), projects no installed agent may use, taking no new sessions, its sessions, rename, remove. A coding agent opens its own page: signing in, the default model, reasoning effort and (Codex) speed as menus in place, and its projects, each with a menu to change how agents work there or stop using it; Add a Project… for this agent. Three levels at most |
| Account | Laid out as iOS Settings: the service and whether it is reached; usage in place (the last 30 days, active days, streak, the weeks of days); appearance as three small phones (the system's, light, dark); Notifications on a page of their own; about (on an own server the connection address and, when it serves MCP, its MCP address, each copied with a tap; the web console; the version); sign out |
| New | The Overview's top right: Connect an Agent…, Add a Machine…, Enter a Pairing Code… |

Navigation is a side menu that slides in from the left (a drag anywhere on a
root page, or the menu button), as the iPhone apps of AI products do; pushed
pages keep the system's swipe back. The menu and the page sit in a small
UIKit container (`SideMenuController`): the page moves as one view, on a
spring timed to the system's push, and keeps its large title's margin. Controls use Liquid Glass
(`glassEffect`, `.glass` buttons); sheets use detents.

Buttons come in four sizes, each by what it does, never sized by hand
(a height of its own made Connect Your Agent's Copy twice a main action's):

| Size | What it is | How |
|---|---|---|
| Main action | The one action that ends a card, page or panel (Copy, Scan the Code, Continue, Approve and Deny, Answer) | Across the width, the system's large size (about 50 pt), a capsule: `.glassProminent`, or `.glass` beside it; `CardButton`, `GuideButton` |
| Row action | An action at the end of a row (Verify, Renew…, Turn On…, Add…, Update) | As wide as its words, the regular size (about 34 pt), `.glass`; `.glassProminent` for the one that matters most in its list |
| Icon | Close, Copy and Send beside an address or a command | A glass circle: Close 32 pt inside its glass (`CardCloseButton`), Copy and Send 36 pt (`CopyIcons`) |
| Link | A way out or in that is not the main one (Send…, Can't Scan?, Enter a Pairing Code) | Text in the accent, no background |

## 2. How it is built

```
apps/ios/
├── Hermesh/                 the app (SwiftUI)
│   ├── App/                 Core (the embedded owner role), Service (typed LocalService calls), Push
│   ├── Signer/              the root key and signing (shared preview from HermeshKit)
│   ├── Model/AppModel.swift the state the views read, and every action
│   └── Views/               Main (menu, overview), Session, Links (grants, machines), Sheets, Onboarding
├── HermeshNotifications/    the notification service extension
├── HermeshUITests/          the end-to-end UI tests (run by e2e/iphone_test.go)
├── scripts/build-core.sh    builds HermeshCore.xcframework
└── scripts/build-highlighter.sh  bundles the Mac app's Shiki for JavaScriptCore (code colours)
packages/HermeshKit/         Swift package shared with the Mac signer
├── HermeshProto             generated protobuf types
├── HermeshKit               LocalService client, signing preview, notice key
└── HermeshSessions          Swift port of @hermesh/core (buckets, filters, transcript, diff, text)
```

- **The owner role in process.** `cmd/hermesh-mobile` builds the Go owner
  role (`internal/owner`, `internal/mobile`) as a static library
  (`build-core.sh`, run before each Xcode build; it skips when nothing
  changed). The app starts it on launch: LocalService on `127.0.0.1` at a
  random port, with a bearer token made for that launch. It connects to the
  relay while the app is in the foreground and pauses when it goes to the
  background, so nothing runs in the background but notifications.
- **A conversation.** Replies parse with swift-markdown and render as
  SwiftUI views (`Views/Session/Markdown.swift`), with the Mac app's rules:
  a single newline breaks the line, a bare link stops where Chinese text
  starts, raw HTML stays text, images show their alt text, only http and
  https links open. A reply being written renders block by block as on the
  Mac (`splitBlocks` in HermeshSessions). Code is coloured by the Mac
  app's own Shiki with the same GitHub themes, run in JavaScriptCore
  (`Code.swift`): `build-highlighter.sh` (an Xcode build phase) bundles it
  from `apps/desktop`'s dependencies into `Highlighter/` in the app, each
  language's grammar a file of its own, read the first time it is shown.
- **The signer.** The root key lives in the Secure Enclave, usable after Face
  ID (`userPresence`); the Simulator has no Secure Enclave for such keys and
  uses a software key. As on the Mac, there is one per profile the owner role
  keeps (trusted-devices.md 4.8), named by its key id in each request, and
  deleted when the owner role asks once the phone left that owner. Requests the owner started on this phone (a pairing
  code they entered, a first setup) are expected and signed after Face ID
  alone; others show the shared signing preview (`SigningPreview`, worded for
  an iPhone) first.
- **Joining.** A new iPhone joins as a trusted device with another one's
  approval (two digits to confirm), by the official service (signing in
  through `ASWebAuthenticationSession`, back with `hermesh://`) or by an own
  server's connection address.
- **Performance.** Native controls throughout, lazy stacks for long lists
  (not a conversation: opened at its bottom, a lazy stack's guessed row
  heights left a gap under the last message, so it lays out every row),
  the owner's world followed through one `Watch` from the in-process
  hermeshd ([state-sync.md](state-sync.md) §11) and a session's events read
  once and then followed (nothing polled), no work while in the background.
  What the app last showed is kept on the phone (`world.pb`, and the last
  10 conversations): a launch shows it at once, and a conversation opened
  again reads only the events after the last one kept. Leaving the front
  pauses the core (no polls, no follows), and it stops listening on its
  loopback port, since iOS may take a suspended app's listening socket back
  without a word; its connections are kept. Coming back, it listens again
  and pings the relay connection it kept while it dials a new one, and
  keeps the old one (with the connections to the machines through it) if
  it answers first: after up to 5 minutes away (the relay waits that long
  for an owner device's answer), the world is current after a ping and one
  call to the Mac, unless the network or iOS dropped the connection. A change of network (Wi-Fi to cellular)
  connects again at once. While
  the relay cannot be reached, the machines are as it last listed them,
  never called offline or unreachable for it (the sync line says it). The
  LocalService client runs each call on a thread of its own, never on
  Swift's cooperative pool. `go test ./e2e -run TestMobileNetwork` measures
  it in round trips to a far relay.
- **Waiting.** Waits show the mark in the brand's motion
  (`Views/Components/Loading.swift`, as `packages/ui`): a page with nothing
  to show yet says what it reads, with the pulse (a conversation has the
  pulsing mark alone, in the middle of the screen); while what shows is not
  current, a quiet line says so, always in the navigation bar and never
  over the page: in the middle of a root page's bar (in the small title's
  place once it is small), under another page's title ("Connecting to
  Hermesh…", "Updating from Mac mini…", and after 10 s without the relay,
  "Can't reach Hermesh"); waiting for another device, the beams
  hover apart. Nothing shows for a wait under 400 ms, and a loader once
  shown stays 500 ms; with Reduce Motion the mark only fades. A card read
  for the first time (the overview's counts, the coding agents while no
  machine's are known) shows its shapes in grey at once, a light sweeping
  across them (a skeleton; with Reduce Motion they fade). A machine being
  read again keeps the state it was last shown with, its coding agents
  too, as its sessions, so coming back to the app reloads nothing in view.

## 3. Notifications

The relay knows when something happened, never what (center-data rules).

1. On start, the app gets an APNs token and makes a **notice key** (X25519,
   in the keychain group shared with the extension). It sends both, with the
   kinds of notices it wants, signed with its device key (`RegisterPush`).
2. The relay keeps the registration and hands the owner's registrations to
   each of their machines (`MachineUpdate.push_targets`).
3. When an agent starts a session, a session asks a question, fails or
   finishes a turn, the machine seals a `Notice` to the notice key of each
   registered device that asked for that kind (HPKE: DHKEM X25519,
   HKDF-SHA256, AES-128-GCM, info `hermesh-notice-v1`) and sends the
   ciphertext to the relay (`Notify`). A new session's notice
   says its title (the starter's, else the prompt's first line, cut at 40
   characters), who started it (the agent's name), the machine, the
   project and the coding agent; the relay seals a request to connect
   itself.
4. The relay sends APNs a generic alert ("Something needs you", a localized
   key) with the ciphertext; urgent notices are time-sensitive.
5. On the phone, the notification service extension opens the notice and
   fills in its title and text (`NoticeText` in HermeshSessions). A question
   whose first item has options gets them as actions, answerable from the
   lock screen after unlocking. If the extension did not run, the app opens
   the notice when the notification is tapped.

The relay sends with an APNs auth key: `hermesh-relay serve --apns-key
<file.p8> --apns-key-id <id> --apns-team-id <team>` (or `HERMESH_APNS_*`). The
key must belong to the team that signs the app. Without one, nothing is sent.

## 4. Languages

English is the source; Chinese is a translation, in String Catalogs:
`Hermesh/Localizable.xcstrings`, `Hermesh/InfoPlist.xcstrings` (permission
prompts) and `HermeshNotifications/Localizable.xcstrings`. HermeshSessions
and HermeshKit keep theirs in `Resources/zh-Hans.lproj/Localizable.strings`,
as the Mac signer does (docs/desktop-app.md §6.6).

## 5. Tests

- `swift test` in `packages/HermeshKit`: the session logic, transcript,
  diff, notice text and both languages.
- `go test ./e2e -run TestPushNotices`: registration, sealing and delivery
  through a real relay and owner Mac, to a spool directory.
- `HERMESH_IPHONE_E2E=1 go test ./e2e -run TestIPhone -timeout 40m`: the app
  on a Simulator of its own, in Chinese, driven by XCUITest against a real
  relay and an owner Mac with a session in every state (a scripted ACP agent).
  It joins, answers a question, interrupts, archives and undoes, reads a
  diff, approves and renews an agent, visits a machine and the devices,
  searches, and answers a notification delivered with `simctl push`.
  `HERMESH_IPHONE_RESULTS=<path>.xcresult` keeps the result bundle.

## 6. Installing on an iPhone

Open `apps/ios/Hermesh.xcodeproj`, pick the phone and Run, or from the
command line:

```
xcodebuild -project apps/ios/Hermesh.xcodeproj -scheme Hermesh -configuration Release \
  -destination 'generic/platform=iOS' -allowProvisioningUpdates -derivedDataPath apps/ios/DerivedData build
xcrun devicectl device install app --device <device> apps/ios/DerivedData/Build/Products/Release-iphoneos/Hermesh.app
```

The team signs it with automatic provisioning (App ID `io.hermesh.ios` with
push notifications and the keychain group, and the extension's
`io.hermesh.ios.notifications`).

Each release goes to TestFlight with the release's version (App Store
Connect app "Hermesh", SKU `hermesh-ios`), signed with a team App Store
Connect API key, as no Apple account needs to be signed in to Xcode:

```
VERSION=0.1.1 ASC_KEY_ID=<key id> ASC_ISSUER_ID=<issuer id> apps/ios/scripts/testflight.sh
```

The build number is the commit count unless `BUILD` says otherwise.

## 7. Not now

Sign in with Apple and App Store review, an iPad layout, widgets and Live
Activities, starting a session from the phone.
