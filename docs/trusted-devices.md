# Trusted devices

> Status: implemented (2026-10-18). Replaces the recovery key and the 24
> recovery words (technical-design.md 3.1, F8) for self-hosted relays and the
> hosted center alike. There were no users yet, so nothing is migrated: a
> hosted center drops the owners it had, and their accounts link again.

In one sentence: **every device with the Hermesh app is an equal trusted
device of its owner**: any of them can approve, each is usually a machine as
well, a new one joins with the approval of one it already has, a lost one is
removed from any other, and only when none is left does the owner start over.

## 1. Why

- People install the app on every Mac they use. "Which one is the signer?"
  is our implementation detail; they should not have to choose.
- A device that is lost or replaced should cost nothing while another one is
  at hand: no recovery secret to keep, no re-pairing.
- Keys never leave secure hardware (Secure Enclave on a Mac, later on an
  iPhone; TPM on Windows; a passphrase-protected key or a YubiKey on Linux).
  There is no exportable master secret anywhere, so a device that was taken
  and then removed is powerless.

## 2. Roles

| Device | Role | How it is added |
|---|---|---|
| A device with the app (Mac now; iPhone, Windows, Linux later) | **Trusted device**: holds a root key, approves, signs. Also a **machine** unless the owner turns that off | The first one creates the owner; each further one joins (4.2) |
| A device with the app that should not approve (a work Mac) | **Machine** only | Joins with "可信设备" (Trusted device) off (4.7), approved on any trusted device |
| A server, a VPS, any `hermeshd` without the app | **Machine** only | `hermeshd pair <connection address>`, approved on any trusted device |
| A personal agent (`hermesh` CLI) | **Agent** | `hermesh auth login` (a link the owner opens in the web console) or `hermesh auth login <connection address>`, approved on any trusted device |

## 3. Identity: a chain of root certs

There is no recovery key. The trust anchor is the first device's root key,
and the set of trusted devices changes only with the approval of a device
already in it.

```
RootCert v1  root_keys = [A]       signed by A   owner_id = "o_" + fp(A)[:24]
RootCert v2  root_keys = [A, B]    signed by A   (B joined)
RootCert v3  root_keys = [B, C]    signed by B   (C joined and A was removed: by a key of v2)
```

- `RootCert { owner_id, root_keys[], version, issued_at }`; each `RootKey`
  is `{ public_key, label, platform }`. `recovery_public_key` is gone.
- **v1** lists exactly one key, signed by it, and `owner_id` is that key's
  fingerprint. **v(n+1)** must be signed by a key listed in **v(n)**.
- Everything that verifies (relay, machines, agents, devices) keeps the
  **whole chain** and accepts a longer one only if the one it has is its
  prefix, byte for byte. Two different certs at the same version are a fork:
  rejected, as today's "conflicting root cert".
- Credentials (TLS extension, relay admission) carry the chain
  (`Credentials.root_chain`, repeated `Signed`). It grows by one per device
  added or removed, so it stays a few entries long. A verifier caches the
  chain it checked and re-verifies only what is new.
- Everything else is unchanged: MachineCert, Grant, AdminCommand and
  OwnerDeviceCert are signed by any key in the current RootCert.
- `MachineCert` gains `device_key_id`: set when a trusted device's own
  machine is certified (by that device), so removing the device knows which
  machine goes with it.

## 4. Flows

### 4.1 The first device

Before the first Touch ID the app shows an authorization page: what this
Mac is about to become, row by row, with the optional parts off until the
person allows them ("安全可信地共享本机 agent", "有请求时提醒我": open at
login and notify). Nothing is claimed or signed before they continue. The
app then signs RootCert v1 with the root key in the Secure Enclave and
links the account (hosted) or claims the relay (self-hosted), minus the
recovery words; its own machine follows only if allowed, and can be added
later.

### 4.2 Another device joins

A new pairing kind, `PAIRING_KIND_DEVICE`, whose subject key is the new
device's root key.

1. On the new device (B) the user signs in. The hosted center sees the
   account already has an owner: `/app-login` offers **"把这台 Mac 加入你的账号"
   (Add This Mac to Your Account)** first, and "start over" only as the way
   out when no other device is left. It hands the app
   `hermesh://join?owner=<owner_id>`. (Self-hosted: B is given the connection
   address, as machines are.)
2. B creates its root key and submits a device pairing request pinned to that
   owner. It shows the verification code, `VerificationCode(DEVICE, B's key,
   owner_id)`, and "approve it on one of your other devices".
3. Every trusted device of the owner that is online gets the request pushed
   (as pairing requests are today); the app, which opens at login once the
   person allowed reminders on the authorization page (4.1), posts a system notification that opens the approval
   window (local notifications, no push service; for machine and agent
   requests too), and the window shows: the device's name, platform and
   the verification code, "add it as a trusted device: it will be able to
   approve everything". The user compares the codes and confirms with Touch
   ID. That device signs RootCert v(n+1) adding B's key (with its label) and
   completes the pairing with the new chain. The first device to answer wins;
   the others' prompts go away.
4. B receives the chain and checks that its key is in the last cert and the
   owner is the one it pinned. Because the pin came from the center, B then
   needs proof that the approval is the person's own: the approving device,
   having signed, shows **two digits** (`JoinMatchCode`, from the root cert it
   signed), and the person types them on B, which computes them from the root
   cert it received. A center that routed the request to an owner it
   controls cannot make the person's device show them (1 in 100 to guess;
   three wrong tries end the join). Only then does B keep the chain. This
   replaces a yes/no question, which people answer without reading.
5. B signed its own OwnerDeviceCert and, if allowed (4.1), its own
   machine's MachineCert together with the request (one Touch ID; both valid
   once the chain lists it), so after the digits it is a device like the
   others.

### 4.3 A device is removed

On any other trusted device: **"移除这台 Mac" (Remove This Mac)**, one Touch
ID, which signs in one batch:

- RootCert v(n+1) without the removed key: its root key, its OwnerDeviceCert
  and anything else it signed stop verifying at once;
- the MachineCerts and Grants the removed device had signed, re-issued
  unchanged by this device, so every machine and agent keeps working (the
  relay admits them with the newer credential for the same key, as it does
  today after a root rotation);
- a revocation of the removed device's own machine (`device_key_id`).

A device cannot remove itself (what it signed must be re-issued by a device
that stays). Replacing a Mac is therefore: join the new one (4.2), then
remove the old one from the new one.

### 4.4 The removed device

The relay disconnects a removed device's owner connection as soon as the
chain without it arrives. When it next connects and is refused, it asks the
relay (`GetInfo` with its owner id) whether it is still listed; if not, the
app says "这台 Mac 已不在你的 Hermesh 账号里" (This Mac Is No Longer in Your
Hermesh Account) and offers to reset it (`Leave`), after which it can be set
up or join again. `Leave` also has the signer delete the root key it no
longer uses. Its own machine keeps its key and projects.

### 4.5 Every device is gone: start over

Unchanged from the hosted-center design 5.4: after a fresh sign-in the
center forgets the owner, the first new device creates a new one, and
machines and agents pair again with the same command. A self-hosted relay is
reset by its operator.

### 4.6 One decision, one Touch ID

Every Touch ID must stand for one decision the person understands:

- Setting up the first device, or joining, is one Touch ID: the root cert
  (or the join request), the device cert and, if the person allowed it on
  the authorization page (4.1), the device's own machine are signed
  together. The prompt follows macOS's "“Hermesh”正在尝试" and names the
  act ("创建你的 Hermesh 密钥，让这台 Mac 成为你的可信设备").
- When the person confirms something in the app's own window (a machine or
  an agent with its verification code and scope, a device with its code),
  the app checks the batch hermeshd asks it to sign against exactly that,
  on the signed bytes: the same key (through the verification code), owner,
  scope and no longer validity, and nothing more. Then it goes straight to
  Touch ID, whose prompt names it ("批准机器「vps-1」加入（核对码 …）"). Anything
  else, anything more, and everything started outside the app opens the
  signing window, which lists each item. So nothing is signed that was not
  shown, and nothing is shown twice.
- What grants nothing is not signed at all: a machine's projects and
  pausing are settings (a grant is full trust on its machines; projects tell
  agents what to use), sent by a trusted device over its own connection.

The relay pushes the new chain to every machine (as root certs are pushed
today); agents pick it up from the next credential they see.

### 4.7 A Mac as a machine only

A Mac the person does not want approving anything, or seeing their other
machines (a work Mac), joins with "可信设备" (Trusted device) turned off on
the authorization page; that switch is there only when joining, since the
first device is the owner. Sharing its agents is then required, and there is
no Touch ID on it.

1. Its hermeshd creates no root key: its machine signs a machine pairing
   request pinned to the owner (`RequestJoin` with `machine_only`), and shows
   the verification code. Any trusted device approves it as any machine.
2. When the owner id came from the center (the person signed in), the
   request asks for the two digits (`PairingRequest.confirm_match`): the
   approving device's signer shows `JoinMatchCode` of the machine cert it
   signed, and the person types them on the Mac, which computes them from
   the cert it received. A center that handed the request to another owner
   cannot make the person's own device show them; without this, the Mac
   would run that owner's agents with full access. When the person typed
   the connection address (`owner_typed`), the owner is theirs and no
   digits are asked for.
3. On the Mac, the app has the main window a trusted device has, with what
   is this Mac's: the sessions agents run on it in the sidebar, opened,
   answered, stopped and archived as on a trusted device, under the names
   the agents' grants gave them (the machine keeps them as agents reach it);
   and for the overview, "这台 Mac" (This Mac): its state, its projects
   (added here with a folder picker, a setting), what runs and the usage.
   hermeshd serves this from the machine in process (`machine.Local`);
   everything that needs the owner's identity (approving, grants, devices)
   is refused, and the app does not offer it.
4. The owner removes it like any machine; the relay then refuses it (only a
   refusal saying the machine or its owner is gone counts), and the Mac says
   so and can leave. It can also leave by itself: it stops the turns under
   way and asks the relay to remove it (`MachineControlService.Leave`; that
   only lowers permissions, so it needs no signature). Either way it is
   retired (`machine.Retire`): it forgets the owner and gets a new key, so a
   new id, since a removed id never comes back; it keeps its projects and
   sessions, and the old key stays in `keys/retired` for the audit log's
   checkpoints. A trusted device that leaves after being removed retires its
   own machine the same way.
5. It can become a trusted device later, by joining as in 4.2. The same
   Touch ID re-certifies its running machine as that device's own, so
   removing the device later removes the machine too.

A default name that another machine of the owner has (two Macs called
"MacBook Pro") gets the next free suffix (`macbook-pro-2`) when approved.

### 4.8 Profiles: signing out, and more than one owner

A device keeps a **profile** for each owner it set up or joined, so that one
device can later belong to several owners (a personal and a work account)
and switch between them. Only the foundation is built so far; the apps
offer neither signing out nor a second owner yet.

- A profile is a directory of its own,
  `<data>/owner/profiles/<id>/` (its `owner.db`, `keys/device.key`, and its
  own machine in `machine/`), with a root key of its own in the signer.
  `<data>/owner/current` names the current profile. hermeshd runs only the
  current one (its relay connection and its machine), and `LocalService`
  acts on it. A root key is never shared: an owner id comes from its first
  device's key, so a device that set up two owners with one key would name
  both the same.
- The signer keeps every profile's root key and signs with the one a
  `SigningRequest` names (`root_key_id`); creating a key keeps the others,
  and `delete_root_key` deletes one, without asking, since that gives no one
  anything.
- `ListProfiles`, `SwitchProfile` and `AddProfile` (an empty profile, made
  current, to set up or join) manage them. Joining an owner another profile
  already belongs to is refused.
- **Signing out** (`SignOut`) stops using a profile without giving it up: it
  stops its notifications, its relay connection and its machine, and keeps
  every key, so it stays one of the owner's trusted devices and what it
  signed stays valid. It is not a security boundary: what keeps a device
  from signing is Touch ID, and what takes that away for good is removing
  the device (4.3).
- **Signing in again** (`SignIn`) takes no other device. A hosted center
  wants the person signed in, in the browser, to the account that owns the
  profile: the app opens `/app-login` naming the owners it has (`owners`),
  the page hands the link code straight back for one of them, and hermeshd
  redeems it with `Claim{resume}`, which the center accepts only when the
  account's owner is the profile's, and which links or changes nothing. A
  self-hosted relay has no accounts, so signing in only checks the relay.
  Either way the device then asks the relay for the current chain: removed
  meanwhile (or the owner started over), it signs in removed, for `Leave`.

## 5. What an attacker can do

| Attack | Result | Why |
|---|---|---|
| The center routes a join request to its own owner, approves it, and hands B that chain | Fails | No device of the person shows the two digits B needs (4.2 step 4); 1 in 100 to guess, three tries |
| hermeshd asks the app to sign more than, or other than, what the person approved in the app | Shown in full | The batch does not match what was confirmed (4.6), so the signing window lists it |
| The center swaps B's key in a join request | Fails | The verification codes differ (as for machines) |
| A stranger who knows the owner id submits join requests | Prompts the user, who rejects them | Same as machine and agent requests; rate-limited per owner |
| The center withholds a removal from some machines | They keep trusting the removed device until they see the chain | Bounded as withheld revocations are: trusted devices also push the chain to machines over E2E when online |
| A stolen device, unlocked by the thief (Touch ID or password), approves things before it is removed | Possible | The same as a stolen signer today; removal from another device ends it |
| A stolen, unlocked device signs a competing v(n+1) before the owner removes it | A fork; verifiers that saw one version reject the other | Detected, not resolved: the owner starts over (4.5). Cheap, because it re-pairs and keeps projects |
| Someone with the center account signs a signed-out device in | Nothing gained | Signing in only lets the device use its own keys again; each use still needs Touch ID on it |
| Another account's link code signs a device in (`Claim{resume}`) | Fails | The center accepts it only from the account whose owner the device belongs to; the code is not used up |
| A removed device later signs anything | Fails | Its key is not in the current RootCert; keys cannot be extracted to sign elsewhere |
| The center hands a machine-only Mac's request to its own owner and approves it there | Fails | No device of the person shows the two digits of that machine cert (4.7) |

## 6. Changes

| Area | Change |
|---|---|
| `proto/hermesh/identity/v1` | `RootCert` without `recovery_public_key`; `RootKey.platform`; `Credentials.root_chain`; `PAIRING_KIND_DEVICE`; `MachineCert.device_key_id` |
| `pkg/identity` | `Trust` keeps and verifies a chain; owner id from the first key; `recovery.go` and go-bip39 removed |
| `pkg/relay` | Stores the chain; admission, `GetInfo` and the machine push use it; `PublishRootCert` appends; device pairing requests pushed to trusted devices |
| `internal/machine`, `internal/cli` | Keep the chain; pairing installs it |
| `internal/owner` | Setup without words; `RequestJoin`, `WaitJoin`, `ConfirmJoin` (B); `ApproveDevicePairing` (A); `ListDevices`, `RemoveDevice`, `Leave`; `Recover` removed |
| Mac app | First run without words; "add this Mac" waiting screen with the code and the final confirmation (from `hermesh://join`, or a connection address on a self-hosted relay); device requests in the approval window; a Devices list with Remove; the removed screen with Leave; self-hosted recovery screens removed |
| Hosted center | `owners.root_chain`; `/app-login` offers joining first; the console lists trusted devices (read only) |
| Profiles (4.8) | `LocalService` `ListProfiles`, `SwitchProfile`, `AddProfile`, `SignOut`, `SignIn`; `SigningRequest.root_key_id` and `delete_root_key`; `ClaimRequest.resume`; `internal/owner` `Device` over one `Owner` per profile; the signers keep a key per profile; the center redeems `resume` codes and `/app-login` takes `owners`; `TestAccountsOnOneDevice`, hosted `TestDeviceSignsOutAndIn` |
| Tests | `pkg/identity` `TestRootChain` (join, removal, forks, gaps, rollbacks, foreign chains); `TestUserJourney` A10 (self-hosted); hosted-center `TestTrustedDevicesOnCenter` (join, approve, remove, a join the center completes itself) and `TestMacAppJoin` (the app's own join code); the browser test for the join choice |
