# The iPhone app

> Status: first build (2026-10-22). A native SwiftUI app for iOS 26, with the
> Mac app's features except hosting itself as a machine.

The iPhone is a **trusted device** of its owner (trusted-devices.md): it
holds a root key, approves and signs, and sees every machine's sessions. It
is never a machine: nothing runs on it, and it hosts no agent.

## 1. What it does

| Place | What it shows |
|---|---|
| Side menu | Overview, Grants, Machines; the sessions by state (Needs You, In Progress, Recent), All Sessions; at the bottom the account, search and New |
| Overview | What needs the owner (questions, failed turns, requests to connect), what is running (the mark at rest when nothing is), unreachable machines; today and usage in a line each (usage opens its page: the year of days) |
| All Sessions | Laid out as the Mac's sidebar: the view menu groups (by status, date, coding agent or machine), sorts by last activity and shows or hides each row's metadata; a group's heading folds it; archived sessions on request; kept on the phone. Swipe a row to archive or delete it, long-press to rename, archive or delete |
| A session | Its conversation, newest at the bottom; a control bar to interrupt a live turn, or the agent's question to answer; session info and the diff in a sheet; ⋯ to copy its id, rename, archive or delete it (delete asks first; its branch stays) |
| Grants | Agents and MCP connections: approve, renew, rename, disconnect, pause; each grant's sessions on a page of their own |
| Machines | Each machine's state and sessions; rename, remove |
| Account | Trusted devices (approve a new one, remove one), appearance (the system's, light or dark), notifications, sign out |
| New | Connect an Agent…, Add a Machine…, Enter a Pairing Code… |

Navigation is a side menu that slides in from the left (a drag anywhere on a
root page, or the menu button), as the iPhone apps of AI products do; pushed
pages keep the system's swipe back. Controls use Liquid Glass
(`glassEffect`, `.glass` buttons); sheets use detents.

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
└── scripts/build-core.sh    builds HermeshCore.xcframework
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
- **Performance.** Native controls throughout, lazy stacks for long lists,
  the owner's world followed through one `Watch` from the in-process
  hermeshd ([state-sync.md](state-sync.md)) and a session's events read once
  and then followed (nothing polled), no work while in the background.

## 3. Notifications

The relay knows when something happened, never what (center-data rules).

1. On start, the app gets an APNs token and makes a **notice key** (X25519,
   in the keychain group shared with the extension). It sends both, with the
   kinds of notices it wants, signed with its device key (`RegisterPush`).
2. The relay keeps the registration and hands the owner's registrations to
   each of their machines (`MachineUpdate.push_targets`).
3. When a session asks a question, fails, or an agent asks to connect, the
   machine seals a `Notice` to each registered device's notice key (HPKE:
   DHKEM X25519, HKDF-SHA256, AES-128-GCM, info `hermesh-notice-v1`) and
   sends the ciphertext to the relay (`Notify`).
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

The team signs it with automatic provisioning (App ID `com.hermesh.ios` with
push notifications and the keychain group, and the extension's
`com.hermesh.ios.notifications`).

## 7. Not now

Sign in with Apple and App Store review, an iPad layout, widgets and Live
Activities, starting a session from the phone.
