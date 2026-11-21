# iOS demo review acceptance

This audit starts with a fresh installation, no account, no paired hardware,
no test signer and no E2E control server. Every scenario enters through the
public **Try It First** button. Tests use XCUITest to tap the app's visible
controls; they do not invoke DemoService directly or inject a prepared model.

The acceptance boundary is the demo workspace. Real Apple/Google sign-in,
physical pairing, external agents, push delivery and real webhook delivery
are outside this demo. They must not be required to finish a demo flow.
Simulation remains identified by the persistent banner and sheet footer.

## Release Her reviewer path

The current source adds Her to the public Release demo. The earlier November 2
record below applies to the Work demo and does not certify this new path or
TestFlight 0.1.6 (722). The old `-HerDemo` scrolling/Markdown fixtures remain
DEBUG-only; they are not the reviewer path or evidence for Release support.

1. Fresh launch → **Try It First** → Her's sample conversation.
2. **Delegate a sample task** → **Allow Once** → **Show sample result** → sample coding task result and review.
3. Task card → output → **Session Info** → `src/welcome.ts` diff.
4. Back to Her → **Progress** → **Check sample change** → review record.
5. Reopen the demo; **Deny** must start no task. Reopen again;
   **Simulate a failure** → **Allow Once** → **Show sample result** → error → **Retry sample task**
   → a new approval → **Show sample result** → successful simulated result.
6. Interrupt the coding session or exit during execution: a stale result action
   must not complete it later. Reentering or relaunching starts no account or old task.

Her's task stays in simulated execution until **Show sample result**, interruption
or exit. Reviewers can inspect its record and confirm interruption at their own
pace; it never races an automatic completion timer. The existing Work greeting
task shows eight seconds of simulated execution.

The sample's words and results identify it as simulated. Only sample text is
accepted: photos/files, real model credentials, connector authorization and
external services are unavailable. Her's demo settings do not write the real
settings tab or environment choice. The Work menu retains the previous sample
flows. No real security permission or signing check is bypassed.

`HerReviewUITests` enters only through the public button, with no test signer,
account, relay, model key or `-HerDemo` launch argument. The repeatable command
is `apps/ios/scripts/test-demo.sh /tmp/hermesh-demo-review`. It builds the app
with `-configuration Release`, uses a new simulator, checks the UI flows and
compares nine synthetic real-preference values afterward. The script deletes
only its own simulator, retaining logs and the `.xcresult` in the output folder.
The state tests use the same `HerLocal` protocol and parsers as the UI; an outer
OS sandbox denies networking during their offline run. This proves the local
scenario works without networking, not real E2EE, third-party sign-in, push,
remote desktop, or real model behavior. The app's normal onboarding core may
still use its local loopback API; it is not a production connection.

The `hermeshkit-tests` CI artifact retains the unit and offline logs. The
`ios-demo-release` artifact retains the simulator logs and Release `.xcresult`.
Preference comparison uses decoded JSON values: normal startup may reorder
encoding without changing a setting. Depending on scene activation, normal
signed-out onboarding may save an empty world cache. Any such cache must decode
to no owner, account or world entities, and no real conversation files may be
created; file creation timing alone does not establish a demo-state leak. No new Hermesh binary is uploaded
or installed on a physical device by this acceptance.

## Acceptance matrix

| Area | Actions and required results | UI scenario |
|---|---|---|
| Entry | Chinese three-row welcome card; demo last; enter without login; start a task; exit to welcome | `testChineseEntry` |
| Task | Select an answer; observe completion; inspect the resulting file diff; exit/reopen to a fresh workspace; relaunch still shows onboarding | `testTaskAndExit` |
| Questions and interruption | Submit a free-text answer; interrupt and verify stopped state; run again and verify the supplied text in the result | `testReviewFreeAnswerAndInterrupt` |
| Session lifecycle | Copy ID, rename, archive, unarchive, delete; verify state and absence after deletion | `testReviewSessionLifecycle` |
| Session lists | All grouping modes, both sort orders, metadata, collapse/expand, archived visibility | `testReviewSessionLifecycle` |
| Tool output and clipboard | Expand grouped tools; read shell/edit results; copy session ID, paste it into a new task and verify it in the result | `testReviewToolOutputsAndClipboard` |
| Agent approval | Approval disabled before a project is chosen; choose scope, approve, run first task and decline a question | `testApprovalsAndMachineControls` |
| New menu | Connect/deny agent; open/cancel machine addition; simulated scan approval; reject invalid manual code then use supplied sample code; connect from Grants | `testReviewAllNewEntries` |
| Grant lifecycle | Rename, list its sessions, renew to 90 days, change project/agent scope without changing expiry, revoke; reconnect an agent and run a new task | `testReviewGrantLifecycle` |
| Machine lifecycle | Rename, rescan, model, effort, speed, change workspace, stop using project, re-add same folder without duplication, list sessions, remove | `testReviewMachineLifecycle` |
| Machine controls | Add/approve machine; pause/resume; test coding agent; browse folders and add project | `testApprovalsAndMachineControls` |
| Project/folder choices | Existing project cannot be duplicated; hidden-folder setting, typed path, both workspace choices, base branch, add/remove last-agent project | `testReviewFolderChoices` |
| Overview | All date ranges, client/agent selections, flow detail popover, heat-cell popover, machine link, waiting sessions link | `testReviewOverviewAndSettings` |
| Search | Query to session, disposable search history; failed/waiting/done state filters including empty results | `testSearch`, `testReviewSearchFilters` |
| Settings | Light/dark/system; no demo sign-out/erase/notification controls; reopen resets demo preference | `testAccountAppearance`, `testReviewOverviewAndSettings` |
| Persistent storage | Seed seven real setting values; change them in demo; compare decoded settings and world/conversation cache afterward | `testReviewPersistentSettingsIsolation` + host snapshot |
| Lifecycle | Background/foreground during question and execution; exit/reset; terminate/relaunch without creating an account | `testReviewBackgroundAndReset` |

## Running the audit

Use a fresh simulator and run `HermeshUITests/DemoUITests` through the Hermesh
Xcode scheme, with parallel testing disabled. The result bundle records the
individual taps, assertions, failure recordings and retained screenshots.
The signed-in account separately has an absence check for the demo entry in
`HermeshUITests/testFirstRun`, driven by `e2e/TestIPhoneFirstRun`.

## Execution record

2026-11-02: all 15 functional scenarios in the matrix have recorded successful UI runs
on an iPhone 18 Pro / iOS 27.0 Simulator. Results were collected across targeted
runs, with focused reruns after fixes. Earlier failing attempts remain in
the logs; the record uses the latest successful run for each scenario.
Physical iPhone validation has not been performed by this audit.

Since 2026-11-04 the wake section appears only for an agent that already has a
doorbell (a bot starts its setup with `message_wake`). The demo has no such
agent, so wake setup is no longer part of this audit.

Local execution artifacts:

- `/tmp/hermesh-demo-review-results.json`: each scenario, passing log and result bundle.
- `/tmp/hermesh-review-audit.log`: the initial broad audit.
- `/tmp/hermesh-review-second.log`, `/tmp/hermesh-review-third.log`: added branches and UI assertion corrections.
- `/tmp/hermesh-review-fourth.log`: task cancellation after machine removal, interruption and clipboard verification.
- `/tmp/hermesh-review-final-targets.log`: workspace appearance persistence.
- `/tmp/hermesh-review-grant-fixed.log`: the complete grant lifecycle after fixing scope state restoration.
- `/tmp/hermesh-review-final-expiry-ui.log`: final grant lifecycle run, also verifying scope changes preserve the renewed date.
- The earlier real-core expiry change and its tests were withdrawn during the
  isolation review. Those old logs are historical and do not certify the current patch.
- `/tmp/hermesh-demo-isolation-ui.log`: demo grant regression and persistent-setting isolation.
- `/tmp/hermesh-isolation-storage-results.json`: before/after comparison of seven seeded real settings, world cache and conversation cache.
- `/tmp/hermesh-demo-first-run-passed.log`: signed-in first-run regression, including absence of the demo entry.
- `/tmp/hermesh-live-isolation-regression.log`: initial signed-in regression failure (see status below).
- `/tmp/hermesh-live-isolation-recheck.log`, `/tmp/hermesh-live-isolation-recheck.xcresult`: passing complete signed-in regression.

Xcode may rotate older result bundles. The JSON record marks bundle availability;
the text logs remain available. The dedicated simulator is “Hermesh Reviewer Audit”.

## Fixes made during acceptance

- Supply a sample manual pairing code so Find can complete without external hardware.
- Supply model choices so model, effort and speed settings can be exercised.
- Normalize sample project paths so re-adding an existing folder does not duplicate it.
- Preserve the named-bot name step while using a simulated webhook.
- Cancel active tasks and pending questions when a machine is removed or a grant is revoked.
- Keep appearance in the demo model, applying it to the workspace until the demo exits.
- In demo mode, initialize the scope form only once after the workspace loads.
  The passing test verifies saved project and agent values in the grant detail page.
- In demo mode, preserve exact grant expiry on scope-only updates. Normal-mode
  TTL calculation and the real owner API keep their original behavior.

## Isolation review

The real Go owner code and RPC expiry contract are unchanged in the final patch.
The scope-form changes above are gated by `model.isDemo`. The pre-existing
normal-mode scope form reset / whole-day expiry behavior is outside this demo
change; it was deliberately not bundled into the demo feature.

Demo mode has its own AppModel and in-memory DemoService. It does not start a
signer, ask for notifications, load/write the real conversation cache, save the
world snapshot, or persist session-list and work-range preferences. Its API
wrapper has no live client and no fallback to the real owner on errors.

The storage check seeds non-default appearance, search history, hidden-folder,
work-range, list-layout, archived-visibility and collapsed-group settings. The
UI then changes each of these inside the demo. The setting **values** remain
unchanged afterward, and no world/conversation cache files are created. JSON
preference encoding can be reordered by the normal app startup; comparison
therefore checks decoded values rather than serialization byte order.

The UI test is `testReviewPersistentSettingsIsolation`; host-side evidence is
`/tmp/hermesh-isolation-storage-results.json`. Xcode migrated the app container
UUID during installation; the after snapshot uses the current container.

Full signed-in regression: **passed**, 2026-11-02, `TestIPhone`, 782.799 seconds.
Xcode reports one passing test, zero failures and zero skipped tests. That test
contains 30 sequential UI activities, covering joining, conversations, approval,
renewal, webhook checks, machine/project management, trusted devices, settings,
usage, search, offline recovery, background/foreground and notification actions.
It uses the real relay, owner daemon and CLI, with scripted coding agents,
a test signer and simulated push delivery. It is not physical-device validation.

The initial attempt failed when the test control server could not create the
session for `centeredLoader`. Its error body was hidden by the old test helper.
The rerun adds diagnostic output only; it passes that step and all subsequent
activities without further production-code changes. The original failure was
not reproduced and its cause remains unresolved; retain it as an intermittent
test/control failure rather than claiming every attempt passed.

The additional persistent-setting UI scenario and host snapshot both passed.
The seeded preferences were restored afterward, and the audit simulator was
shut down. No real Go owner/CLI production files are changed by this feature.

To repeat the signed-in regression:

```sh
HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_RESULTS=/tmp/hermesh-live-review.xcresult \
  go test ./e2e -run '^TestIPhone$' -count=1 -timeout 40m
```
