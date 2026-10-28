# Erase everything: one approval, every device back to its first run

Status: agreed 2026-10-28 (§8), being built: steps 1 and 2 are done, step
3 under way (§6). One action, **Erase
Everything** (全部清空), which first says exactly what it erases.

## 1. The ask

The owner wants one action, approved once, that takes every device of
theirs back to how it was before Hermesh was set up there, online or not:
trusted devices (Macs, iPhones), machines (a VPS, a machine-only Mac),
agents (the CLI on a computer, connectors in Claude, Grok and the like), and
the account on the center. Then each kind of first run can be tried again
from nothing: the Mac's, the iPhone's ([onboarding.md](onboarding.md)),
a machine's pairing, an agent's connecting.

## 2. What there is

- **The console's "start over"** (center `ResetOwner`, hosted-center design
  5.4): with a sign-in from the last ten minutes, the center forgets the
  account's owner and everything under it (`owners`, `machine_certs`,
  `grants`, `pairings`, and by cascade the post office, doorbells, push
  targets), ends its MCP connections (`OwnerGone`), unlinks the account and
  mails the person. Machines and agents are then refused ("the owner
  identity was reset; pair this device again"). It is for an owner who has
  lost every device, so it needs none and signs nothing.
- **`Leave`** on a removed device (trusted-devices.md 4.4): it retires the
  device's own machine (a new key, its projects and sessions kept), forgets
  the owner and grants, and deletes the root key in the signer. The app
  offers it when the device finds it is no longer listed.
- **`SignOut` / `SignIn`** (4.8): stop and resume a profile, keeping its
  keys.

What is missing: starting it from a device, devices that act on it by
themselves (now each removed device waits for a tap on `Leave`), offline
devices doing it when they come back, and clearing what Hermesh keeps on
each device besides its keys (sessions, projects, the apps' settings).

## 3. Design

### 3.1 One signed order

On a trusted device the owner chooses **Erase Everything…**, reads what it
erases (§3.6), and approves once (Touch ID, Face ID). The device signs, with
its root key, an **erase order**:

```proto
// hermesh.identity.v1
message EraseOrder {
  string owner_id = 1;
  string erase_id = 2;                     // random, names this erase
  google.protobuf.Timestamp issued_at = 3;
}
```

Everything that acts on it checks it against the root chain it already
holds: signed by a root key of that chain, for its owner. **What deletes
anything on a device is the owner's signature, never the center's word**:
a center (or a relay) that wants to wipe a device cannot forge the order,
and the console's "start over", which signs nothing, keeps cutting devices
off only, as now.

### 3.2 The center (or a self-hosted relay)

`OwnerControl.EraseOwner(signed order)`, from a connected trusted device of
that owner. The relay verifies the order against the owner's current chain,
then, in one go:

1. keeps the order for 10 minutes (§3.4);
2. closes, half a second later, every connection of the owner's peers
   (trusted devices, machines, CLI agents), which are then refused
   ("this relay no longer serves this device's owner") and ask for the
   order (§3.4);
3. forgets the owner as the console's start over does (the same deletions,
   `OwnerGone`, unlinking the account) and mails the person, naming the
   device it came from;
4. signs the account out of every browser (its web sessions) and drops its
   connectors' OAuth registrations and consents, so the next connector
   starts from its first consent page. The account and how the person signs
   in stay: signing in again finds no owner, which is a first run.

A self-hosted relay does the same and becomes unclaimed again, with a new
claim code (`hermesh-relay claim-code`, as at its first start).

### 3.3 Each kind of device

Each one that gets a valid order (now, or within those 10 minutes) reports that
it has it (§3.4), then erases:

| Device | What it erases |
|---|---|
| Trusted device (Mac app) | `Leave`, without asking (the owner forgotten, the root key deleted), then the profile's directory, its own machine's sessions, projects list and the worktrees Hermesh made, and the app's settings (first-run steps, window, notification choices). The app opens on its first run |
| Trusted device (iPhone) | The same `Leave`, its push registration (ending with the owner), the cached world and the app's settings. It opens on its first run |
| Machine (VPS, Linux, machine-only Mac) | Its key, owner, sessions, projects list and Hermesh's worktrees: it is a newly installed machine, waiting to be paired |
| Agent (CLI) | Its key and grant files (`hermesh auth logout`) |
| Agent (connector) | Its connection at the gateway, and its OAuth registration and consent: connecting again starts from the consent page |
| Center account | The owner and everything under it (machines, grants, messages, doorbells, push targets); every browser signed out |

Never touched, and the sheet says so: the projects' own directories and
repositories (a worktree's branch stays in its repository); a worktree with
uncommitted changes, moved (`git worktree move`) to `kept-worktrees/` in
Hermesh's data directory and named afterwards; other profiles on a device
(another owner's); the account itself and how the person signs in; and what
the operating system keeps (notification permission, login items).

### 3.4 Those online now, and the others later

The relay keeps each erase order for 10 minutes, while the erase runs, and
then deletes it and its acks. A device, machine or agent of that owner that
connects in that time is refused; it then asks `GetInfo` with its owner id,
which answers with the order and the chain it was signed under. It verifies
the order against the chain it holds (taking the newer chain only if it
extends its own) and erases as in §3.3.

Each erases after reporting it (`AckErase`, signed with its own key before
deleting it: the erase id and the id the owner knows it by, which that key
must name: a machine's or an agent's id; a trusted device signs with its
device key and adds its device cert, which its root key signed). The relay
records the acks against the order, for the device that started it to
tick each one as it reports.

One offline then is not chased: nothing of the owner is kept to tell it.
When it is next used, it is refused as a removed device is, and is reset
there:

- a trusted device (Mac, iPhone, machine-only Mac) shows that it is no
  longer in the account, and Set Up This Mac Again (`Leave`);
- a machine logs how to pair it again, and `hermeshd pair <address>` pairs
  it with the new owner as it is: its old owner is gone from the server;
- an agent is refused as revoked, and `hermesh auth login <address>` pairs
  it with the new owner as it is.

So there is nothing to take back, and no order waiting for anyone: what
the order erased is gone the moment it is approved (the keys, the server's
records), and a new owner (a new sign-in, a new setup) has a new owner id
the old order never names.

### 3.5 In the apps: the sheet says what it erases

Mac: Settings → Account & Devices, at the bottom, **Erase Everything…**
(red). iPhone: the account sheet, at the bottom. One sheet, read from the
world the app already has, by name, not only counts:

1. **What it erases**, one group per kind, each item named with whether it
   is online:
   - trusted devices: signed out of the account, their keys, sessions and
     settings erased;
   - machines: unpaired, their projects list, sessions and the worktrees
     Hermesh made erased;
   - agents: disconnected; a CLI's pairing files and a connector's consent
     erased;
   - the account (its email): unlinked, its messages, wake-ups and
     connections erased, every browser signed out.
   An offline one says "erased the next time it connects".
2. **What stays**: the projects' directories and repositories (branches
   included), worktrees with uncommitted changes, the account and how you
   sign in, system permissions. And: this cannot be undone.
3. **Type the account's email** (a self-hosted relay's address) to enable
   the red button, then Touch ID or Face ID, whose prompt names it ("Erase
   Hermesh on 6 devices").
4. **Progress**: each one ticked as it reports, for up to 15 seconds; then
   this device erases itself and opens on its first run, saying once what
   was done ("5 erased; yee-ipad will be when it connects").

The console keeps its "start over" for an owner without devices,
unchanged: it signs nothing, so it erases nothing on a device.

### 3.6 Built for testing first runs

- `hermeshd erase` on a Mac or a machine (asking the running app or
  service, or acting on its data dir when stopped), for scripts.
- The e2e harness gets an `erase(world)` helper that runs the real order,
  so each first-run test starts from a world that was used and erased,
  which is closer to people than a world never set up.
- OS state is not Hermesh's to reset: the notes say how to clear it for a
  test (`xcrun simctl privacy … reset`, deleting the iPhone app, System
  Settings).

## 4. What an attacker can do

| Attack | Result | Why |
|---|---|---|
| The center wipes a person's devices | Fails | Devices act only on an order the owner's root key signed (§3.1) |
| The center replays an old order to a new owner | Fails | The order names the owner id; a new owner has a new id |
| A stolen, unlocked device erases everything | Possible, and seen | As removing every other device is today; the typed email and Touch ID slow it, the mail tells the person, and no project's files are touched |
| The center withholds the order from an offline device | That device is cut off but not wiped | It is refused as today and offers `Leave` |
| A removed device signs an order | Fails | Its key is not in the chain the others hold |

## 5. Not doing (now)

- Deleting the center account itself (the operator's privacy process).
- Erasing from the console with devices acting on it (it would need a
  signature the console has not got).
- A lighter reset that keeps machines' projects and sessions: the
  console's start over is that, for an owner without devices.
- Undo: keys are deleted, by design.

## 6. Steps

1. Done: the order, the relay's `EraseOwner` and tombstone, `AckErase`;
   machines, CLI agents and trusted devices acting on it (hermeshd's local
   API: `EraseEverything`, `EraseStatus`, `FinishErase`, and
   `GetStatus.erased` on the first run that follows); the signers' card.
   e2e `TestEraseEverything`: a second trusted device, a machine-only Mac,
   an online and an offline machine and an agent; a dirty worktree kept, a
   clean one erased; the relay claimed again. Forged orders and acks:
   `pkg/identity` tests.
2. Done: the Mac app and the iPhone: the sheet (`EraseStatus` without an
   erase id lists what it reaches, a device's own machine as the device),
   its progress, and the first run's line (`Erased.by` names the device
   that erased, for the others). Until step 3, only with a server of one's
   own: the official service hides the button. The app's own preferences
   (appearance, language, notification choices) stay; the first-run steps
   start again with the next sign-in. If the app never asks `FinishErase`
   (its window closed), the device erases itself two minutes after the
   order. e2e: the desktop's onboarding spec, `TestIPhoneFirstRun`.
3. The center: account sign-out and connector consents, the console's
   erase record. Cloud e2e.
4. `hermeshd erase`, the harness helper, the testing notes.

## 7. Tests (each step's e2e, as people do it)

- Two trusted devices and two machines, one of each offline: erase from
  the Mac; the online ones erase within seconds, the offline ones when
  they connect; every app opens on its first run; the account signs in to
  no owner.
- A machine keeps no Hermesh data but the projects' directories; a
  worktree with changes is kept and named.
- A relay that sends a forged or another owner's order erases nothing.
- A device back after the 10 minutes is refused and offered `Leave`; a
  machine and an agent pair with the new owner without logging out.

## 8. Decided (2026-10-28)

1. Worktrees with uncommitted changes are kept (and named), not erased.
2. The center account stays (sign in again for a first run); deleting it is
   the operator's process.
3. The order is kept 10 minutes, for those online (changed from 90 days the
   same day: nothing is kept for those offline, which are reset when next
   used).
4. The owner confirms by typing the account's email (a self-hosted relay's
   address).
