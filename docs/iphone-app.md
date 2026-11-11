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
| A session | Its conversation, newest at the bottom, as the Mac shows it: the owner's messages in a bubble of the brand's blue on the right, the agent's replies as GitHub Markdown with code in the Mac's colours under a bar with its language and Copy, and tables in a box with a shaded header, tool calls as grey lines folded until opened (a run of them one line, opening to a card with a row each; a row opens to its whole output); long-press a message to copy it. Scrolled well up from the end, a button brings it back. A control bar to interrupt a live turn, or the agent's question to answer; session info and the diff in a sheet; ⋯ to copy its id, rename, archive or delete it (delete asks first; its branch stays) |
| Grants | Agents and MCP connections: approve, renew, rename, disconnect, pause; each grant's sessions on a page of their own |
| Machines | The machines, a green shield on those that are trusted devices too, then the other trusted devices (which approve but run nothing; remove one with a swipe), each machine opening its page: its coding agents (signed in, projects, defaults), projects no installed agent may use, taking no new sessions, its sessions, rename, remove. A coding agent opens its own page: signing in, the default model, reasoning effort and (Codex) speed as menus in place, and its projects, each with a menu to change how agents work there or stop using it; Add a Project… for this agent. Three levels at most |
| Account | Laid out as iOS Settings: the service and whether it is reached; usage in place (the last 30 days, active days, streak, the weeks of days); appearance as three small phones (the system's, light, dark); Notifications on a page of their own; about (on an own server the connection address and, when it serves MCP, its MCP address, each copied with a tap; the web console; the version); sign out |
| Remote Desktop | From Her's settings, on a Mac or a Linux machine with a desktop that runs her: its screen, full screen, operated by touch, after Face ID ([computer-control.md](computer-control.md) §6, [computer-control-linux.md](computer-control-linux.md)) |
| New | The Overview's top right: Connect an Agent…, Add a Machine…, Enter a Pairing Code… |

Navigation is a side menu that slides in from the left (a drag anywhere on a
root page, or the menu button), as the iPhone apps of AI products do; pushed
pages keep the system's swipe back. The menu and the page sit in a small
UIKit container (`SideMenuController`): the page moves as one view, on a
spring timed to the system's push, and keeps its large title's margin. Controls use Liquid Glass
(`glassEffect`, `.glass` buttons); sheets use detents.

The container arranges the two in one of three ways, by the screen it has,
never by the device (iPhone Duo's displays and poses, and whatever else
gives an iPhone app those sizes):

| The screen | The menu and the page |
|---|---|
| Compact width, bars along the top (an iPhone), or regular width upright (iPhone Duo open, upright) | The page slides aside by the menu's width and stays in view, a dimmed card that closes the menu |
| Compact width, bars on the side (iPhone Duo closed: `verticalBarEdge`) | That strip of the screen is the bars' in every state, so a page beside the menu would lie under them: the menu takes the whole page's place. It has the bar then, Close at its top, where the page has the menu button, and Search under it (its own Search button goes) |
| Regular width, wider than tall (iPhone Duo open, on its side) | Side by side, a hairline between them, no drag. The menu's sidebar button (at its top's leading end) hides it, and the page then has the same button where the menu button is, to show it again; what would open the menu shows it. The hairline drags the menu between 240 and 400 pt wide (the page keeps 400). Hidden and width are kept on the iPhone |

Changing from one to another (the device opened, closed or turned) leaves the
menu closed. Covering, a finger drags a picture of the page, and the menu
keeps still: a view moving across the bars' strip has its safe area change on
every frame, and SwiftUI laid the whole page out again each time (measured on
an iPhone Duo: two thirds of the main thread while dragging). `DuoUITests/testMenuSwipe`
measures the moves' hitches (`HERMESH_DUO_SWIPES=<n>`, on a device). The bars themselves are the system's: every page's buttons are
toolbar items, which iOS stands on the side where it does so. Two views draw
their own top and read `barsOnTheSide` for it: Her's page keeps her picture
and name at its top and leaves its menu button to the system's bar; search,
which closes by its field, drops its second Close. In the demo the container
keeps the screen's full width (the bar takes none of it), or the system would
find no page at the bar's edge and leave the bars along the top.

Buttons come in four sizes, each by what it does, never sized by hand
(a height of its own made Connect Your Agent's Copy twice a main action's):

| Size | What it is | How |
|---|---|---|
| Main action | The one action that ends a card, page or panel (Copy, Scan the Code, Continue, Approve and Deny, Answer) | Across the width, the system's large size (about 50 pt), a capsule: `.glassProminent`, or `.glass` beside it; `CardButton`, `GuideButton` |
| Row action | An action at the end of a row (Verify, Renew…, Turn On…, Add…, Update) | As wide as its words, the regular size (about 34 pt), `.glass`; `.glassProminent` for the one that matters most in its list |
| Icon | Close, Copy and Send beside an address or a command | A glass circle: Close 32 pt inside its glass (`CardCloseButton`), Copy and Send 36 pt (`CopyIcons`) |
| Link | A way out or in that is not the main one (Send…, Can't Scan?, Enter a Pairing Code) | Text in the accent, no background |

### Public demo

The welcome page offers three choices: “Start Using Hermesh”, “Already using
it on another device”, and “Try It First”. “Try It First” opens a disposable
sample workspace without signing in, pairing a computer or
installing a coding agent. The normal screens show simulated machines,
projects, grants and conversations. “Run Demo Task” starts a question;
answering or declining it produces simulated tool output, a result and a
code diff. Adding machines, approving agents and managing projects also
work within this sample workspace.

The persistent banner and sheet footer identify simulated data. Exit Demo
returns to onboarding; reopening starts fresh. The signed-in account page
has no demo entry.
`AppAPI` routes demo calls to `DemoService` in memory, with no fallback to
the real core. Its model, conversation cache, search history and settings
are separate from the real workspace. `DemoUITests` exercises the public
entry and core flows. The [review acceptance matrix](ios-demo-review.md)
records the 15 UI scenarios and their execution boundaries. The first-run
end-to-end test also checks that the signed-in account page has no demo entry.

For App Review notes: open the app, tap **Try It First**, then **Run Demo Task**.
Choose a greeting and tap **Answer**. Open **Session Info** to inspect the
simulated changes; the menu also exposes machines and grants. No login,
external hardware, pairing code or external service is required for this
demo.

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
├── scripts/build-highlighter.sh  bundles the Mac app's Shiki for JavaScriptCore (code colours)
└── scripts/build-diagram.sh  bundles the Mac app's Mermaid and the page it draws on (diagrams)
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
  https links open. A reply being written renders block by block
  (`splitBlocks` in HermeshSessions). Code is coloured by the Mac
  app's own Shiki and themes, run in JavaScriptCore
  (`Code.swift`): `build-highlighter.sh` (an Xcode build phase) bundles it
  from `apps/desktop`'s dependencies into `Highlighter/` in the app, each
  language's grammar a file of its own, read the first time it is shown.
  Math reads as on the Mac (`\( … \)`, `\[ … \]`, `$$ … $$` and a
  fenced `math` block; `liftMath` in HermeshSessions takes it out of the
  source before Markdown is parsed) and is typeset on the phone by SwiftMath
  (`Math.swift`): each formula is drawn once as a picture that takes the
  text's colour, on the text's baseline in a line or centred when set apart,
  and shows as its source when it cannot be typeset. A fenced `mermaid`
  block is drawn by the Mac app's Mermaid with the Mac's settings
  (`Diagram.swift`), on one web page that is never shown and loads nothing:
  `build-diagram.sh` (an Xcode build phase) bundles it into `Diagram/` in
  the app. A diagram comes back as a PDF and shows as a picture, with its
  source a tap away; a tap opens it large to pinch and to share. While a
  reply is being written a diagram stays its source, and one that cannot be
  drawn shows its source.
  A file Her sends that is Markdown, or code in a language Shiki knows by
  its extension, opens on a page of the app's (`HerFilePage.swift`), laid
  out as it scrolls; anything else opens in Quick Look, which shows
  Markdown with its marks and such code not at all.
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
   itself. A notice the relay does not take (the machine is not connected
   to it, or the call fails) waits on the machine, the newest of each
   session, for up to an hour, and goes out once the machine is connected
   again; a question answered meanwhile is not announced.
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
  `HERMESH_IPHONE_GREP=testComputerControl` runs Computer Control alone,
  against the Mac's screen helper showing its test picture.
- `DuoUITests` (the demo world, no relay): every root page, a session, the
  menu and the sheets, upright and on its side, in the pose the Simulator is
  in when it runs; it checks that the menu shows as the table above says, and
  with `HERMESH_DUO_SHOTS=<dir>` keeps a screenshot of each page, named after
  `HERMESH_DUO_POSE`. Poses are set in Device Hub (XCUITest and `simctl`
  cannot fold the device). `HermeshUITests/testDuo` does the same against
  TestIPhone's world kept up with `HERMESH_IPHONE_PREVIEW`: the app joined,
  a question, and Her's page with a conversation. Run on the iPhone Duo
  Simulator closed (2026-11-11); open and half-folded are not yet run.

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

Info.plist answers export compliance (`ITSAppUsesNonExemptEncryption`
false): the app uses only standard algorithms (TLS, HPKE with X25519 and
AES-GCM, ECDSA P-256) and is not distributed in France, so a new build
needs no questions answered before testers get it.

## 7. Not now

Sign in with Apple and App Store review, an iPad layout, widgets and Live
Activities, starting a session from the phone.
