# Connectivity validation — 2026-11-13

Workspace: `/Users/yipengfei/.codex/worktrees/peer-udp-performance/hermesh`.
Branch: `codex/peer-udp-performance`; base `d68c45a8` plus the local diff.
The user's main checkout and Claude's checkout were not changed.

Installed pair at the end of this traversal round: **Mac peerfix17 / Duo 1125**. Cellular direct
acceptance remains outstanding. The peerfix16 test on the new Wi-Fi stayed
relayed at 400–500 ms; UU also appeared to relay there. The earlier Wi-Fi,
where UU had proven UDP P2P, cannot be retested until the user returns.
Later sections distinguish each build, network and automated result.

Subsequent home-Wi-Fi startup work and peerfix18/19 installations are recorded
in [connection-startup-validation-2026-11-13.md](connection-startup-validation-2026-11-13.md).

## Implemented and installed

- Latest Claude baseline: early and repeated candidate exchange, TURN
  selection/refresh work, adaptive retransmission wait, NAT mapping logs.
- peerfix10 / iPhone 1120: host/STUN socket reuse on each physical interface,
  direct transport-RTT reads for first-frame recovery, local first-packet and
  assembled-frame logs. Installed and verified on both devices.
- peerfix11: bounded controlled-side checks after a relay is selected,
  NAT-direct promotion, stronger switching thresholds and corrected negative
  quality scores. Mac peerfix11 and Duo iPhone 1121 were both installed.
  On the follow-up installation request, devicectl verified bundle version
  1121 and launched `io.hermesh.ios` at 09:09 local time. The following
  5G session still used TURN; installation is not a connectivity pass.
- Mac peerfix13 and Duo iPhone 1122: the phone app's
  signature, bundle version and embedded core version were verified; the
  pending-offer implementation is present in the linked Go core. The app
  was installed and launched successfully. This version fixes interrupted
  restart negotiation. The user subsequently confirmed automatic recovery
  in both directions of one Wi-Fi/5G sequence; cellular direct still fails.

## Automated results

| Check | Result | Evidence |
|---|---|---|
| Host socket/public mapping/NAT data/ICE restart | Pass, 3 repeats | Failed before the shared-socket fix |
| 550 ms RTT, lost first-keyframe packet, SRTP + NACK | Pass, 3 repeats | Before: no first frame at measured 554 ms RTT; after: exact frames, no replacement keyframe or UI callback |
| Relay to NAT direct, late public candidates | Pass, 3 repeats | Failed until controlled-side outbound checks were restored |
| Renomination jitter/negative score/NAT direct | Pass, 3 repeats | All three cases failed before the selection changes |
| Pion relevant candidate/mux/selection race suite | Pass, 36 top-level tests, 18.227 s | `pion-regression.log` |
| Screen, owner, relay race suites | Pass, 60.359 / 3.950 / 11.372 s | `regression.log` |
| Five full screen E2E flows | Pass, 28.242 s | TURN, unreachable server, network change, relay-to-direct, screen/input |
| iPhone UI E2E on 1120 source | Pass, 1 / fail 0 / skip 0 | 240.517 s; no phone-side Swift changes in 1121 |
| Pion entire race suite | Not passed | Existing `TestConnectionStateFailedDeleteAllCandidates` and broader `TestUDPMux_Agent_Restart` timeouts also reproduce on clean d68c45a8; related suites above pass |

Local E2E first frame was 576 ms, and 30 frames arrived 250 ms after the
simulated network change. These are one-computer measurements, not 5G
latency or an improvement claim over the earlier 584 ms sample.

## Real 5G result: not solved

At 01:05 local time, session `7710baa17ab3ab4f` used Mac peerfix10 and Duo
1120, with both Surge instances left in the user's normal state. The user
confirmed it connected but was still relayed. The Mac records:

- Offer answered in 489 ms on try 1 (reported by the viewer).
- Physical host port 60686 also supplied its srflx base/public port 60686:
  the shared-socket change is active.
- Phone physical mapping sample: `47672>33532`, then `47674>33535`;
  system-route sample: `47673>33534`, then `47675>33537`.
- Mac physical mapping sample: `61015>61015`, then `57732>57732`.
- Connected at 3.631 s; first encoded frame at 964 ms. Paths repeatedly
  changed between relay alternatives during the 30-second session.
- Phone first-packet/frame logs could not be collected after USB was
  unplugged; the user is temporarily unable to reconnect. Native display
  timing and loss recovery on this real path are therefore not verified.

This proves destination-dependent port mapping on the phone path, which
includes Surge and the carrier. It does not identify the responsible layer,
measure NAT filtering, or prove that port prediction alone will solve it.
Read-only NAT-PMP address and PCP ANNOUNCE requests to the current Mac
gateway received no response within 1 s; no port mappings were created.

The user confirmed that the Mac-only peerfix11 retest also stayed relayed.
After the phone 1121 installation, session `5b18107cf1b60a00` began at
09:09:57 local time. Its phone candidates contained cellular/virtual
interfaces but no Wi-Fi address. It selected TURN at 1.568 s and connected
at 2.838 s; the Mac encoded its first frame at 498 ms. The phone reported
an answer in 460 ms, with physical mapping samples `55818>20852` and
`55822>20856`. Paths at 10.271 s and 29.582 s were also relayed.

At 09:11:46 the same session selected a private phone address
`192.168.66.35`; later sessions advertised that Wi-Fi address too. This is
not evidence of a cellular direct connection. The earlier session then
disconnected and failed; recovery on a live network change is not accepted
as passing based on this trace. The phone logs were subsequently retrieved
at 09:30 after the user connected and unlocked Duo; devicectl confirmed 1121.

The phone timeline for that session shows:

- Selected relay RTT samples of 446 ms and 410 ms.
- First RTP packet at 3.282 s and first assembled keyframe at 3.295 s.
- 2,393 packets and 1,374 assembled frames before the session ended.
- A network-change event at 09:11:46.275, direct selection at 11 ms RTT at
  09:11:46.479, then a second network-change event at 09:11:47.162.
- The in-flight restart RPC failed 1 ms later with
  `unavailable: http2: client connection force closed via ClientConn.Close`.
  The session ended at 09:12:03.947.
- The subsequent Wi-Fi session `f4b0ba9135f233ab` selected direct at 5 ms
  RTT, received its first packet at 812 ms and assembled its first frame at
  817 ms. Native decode/display timing is still not instrumented.

## Follow-up diagnostics and NAT matrix

The host/STUN and real TURN integration tests now include a phone with
destination-dependent mapping. In the virtual network:

- With an address-dependent filter on the machine, standard ICE discovers
  the phone's peer-reflexive address, carries data and survives ICE restart.
  It also upgrades a working TURN connection to direct.
- With an address-and-port-dependent filter on the machine, the same
  advertised candidates do not establish direct connectivity. TURN remains
  selected and continues carrying data during the bounded test.

The targeted race suite passed in 15.779 s. This proves a boundary of the
current implementation, not the filtering behavior of this actual Mac's
gateway or Surge. Mapping and filtering must be distinguished, as in
[RFC 5780 sections 4.3–4.4](https://www.rfc-editor.org/rfc/rfc5780.html#section-4.3).

Mac peerfix12, installed and signature-verified at 09:26 local time, adds
observation of authenticated incoming ICE binding
requests. It counts direct/public-direct/relay requests at 4 s, 12 s and
session end, with at most 16 public direct address-pair details per session.
It uses the existing ICE callback, always returns false from the selection
hook, sends no extra probes and never logs STUN payloads or credentials.
The existing full screen/input/restart/takeover test verifies the counters
are populated during actual connections. Screen and owner race suites pass
(60.146 s / 3.960 s); the relay suite passes in 11.610 s and all five screen
E2E flows pass in 29.224 s. One initial test command used the nonexistent
`./internal/relay` path; the correct `./pkg/relay` suite was then run and
passed. This is a diagnostic build, not a claim that cellular
hole punching has been fixed. Phone 1121 remains compatible.

Zero authenticated direct requests cannot distinguish upstream packet loss
from rejection before authentication; a scoped packet capture may still be
needed. The callback avoids concurrent full PeerConnection stats collection
after TURN connects, which the existing implementation intentionally avoids.

Do not mark T5/T16 direct success until a real selected pair has no relay
and the user sees stable video.

## Interrupted network-change recovery (peerfix13 / phone 1122)

The phone log identified a failed restart signaling exchange. Reproduction
with actual screen capture, WebRTC media and input found that the viewer
left Pion in `have-local-offer` after a failed restart RPC. Subsequent
attempts created another offer and failed with
`have-local-offer->SetLocal(offer)->have-local-offer`. The previous restart
loop did not log this error, so that internal error is reproduced evidence,
not a line recovered from the production phone log.

The fix retains the exact pending offer, retries transient signaling errors
on the same session, completes any pending negotiation before gathering
another offer for the latest interfaces, and keeps retrying a failed
negotiation even if the old transport still reports connected. Attempts
retain the existing bounds; permission/not-found errors are not retried by
the signaling helper. Restart failures now appear in the local phone log.

`TestSessionRecoversInterruptedRestart` covers request loss and response
loss, each with a one-attempt failure and exhausted retries followed by a
later restart. All four failed before the fix and pass afterward, retaining
the same session, receiving more frames and delivering text input to the
real helper. The targeted race run took 5.778 s. Full screen/owner/relay
race suites passed in 66.267 / 3.944 / 11.214 s; five screen E2E flows passed
in 28.303 s. An older unit-test expectation that restarts get only one
signaling attempt was updated to require bounded retries with unchanged
session/offer and no retry for permission/not-found errors.

The incoming-check diagnostic also excludes the proxy benchmark range
`198.18.0.0/15` and carrier shared range `100.64.0.0/10` from its public
counter. Neither range is covered by Go's `net.IP.IsPrivate`.

This fixes a reproduced recovery failure; the device result below validates
one Wi-Fi/5G sequence, while cellular direct remains unsolved. Build/install records and
the sanitized phone timeline are in `/tmp/hermesh-peerfix13-20261113`.

## 1122 device validation and UDP observation

The user reported stable video on cellular via TURN, then automatic recovery
when switching to common Wi-Fi and back to 5G. Both logs correlate this to
session `f73673916dd9faee` (09:43:39–09:45:20 local time). The session ID
survived all restarts and ended when the user closed the page, not with a
connection failure. Wi-Fi selected direct; cellular selected relay. Phone
relay RTT samples were 432–523 ms, with first packet at 3.506 s and first
assembled keyframe at 3.559 s. Multiple network notifications still caused
consecutive restarts; this is one successful sequence, not a long-run
stability or recovery-latency claim.

At 4 s / 12 s, Mac authenticated-request counters showed direct=0 and
relay=124 / 324. A fresh cellular-only session `761fc28305df3e10` also had
direct=0 throughout; its final relay request count was 635 at 26.142 s.

For the fresh session, a bounded PKTAP capture retained only tcpdump text
metadata (addresses, ports, interface, process, direction and UDP length).
The filter covered the phone public IP and STUN control traffic at the
existing TURN server. No raw pcap or packet payload was written. The
capture began at 09:49:34.852; the user opened the session later, so the
actual peer traffic observation was 09:49:58.411–09:50:11.432, **13.02 s**,
not the entire 26-second session. The 35-second timer sent SIGINT; after
the bounded 2-second grace it killed tcpdump. Total wall time was 37.017 s;
the process was verified absent and the temporary helper removed.

All 3,739 text records parsed. The 2,520 peer-address observations were
outbound: 504 on en0 attributed to hermeshd, 1,008 on utun4 attributed to
hermeshd, and 1,008 on en0 attributed to Surge. They include observations
of the same traffic on multiple interfaces and are **not** unique packets
on the wire. No packet from the phone's public IP appeared on any captured
interface. STUN responses did appear, including 58 en0 observations
attributed to hermeshd. The physical host socket `192.168.77.95:62356`
sent checks directly and its advertised mapping preserved port 62356.

This places this observed failure before Mac ICE authentication/selection;
it does not locate a carrier, router or proxy as the responsible layer.
Two read-only UPnP IGD discovery requests (multicast and current gateway,
bound to en0) received no responses in 3 s. Earlier NAT-PMP/PCP discovery
also received none. No port mapping was created; no response is not proof
that a protocol is unsupported. Automatic mapping remains a standard
option where available ([Tailscale's NAT traversal description](https://tailscale.com/blog/how-nat-traversal-works)).

## Remaining work

- With phone 1122 now installed, compare repeated 5G/common-Wi-Fi opens,
  first frame, selected path/RTT, input and Wi-Fi/5G recovery.
- Determine the actual peer-directed mapping/filtering and whether bounded
  enhanced UDP punching is needed. No brute-force port scan was added.
- Measure new candidate-check packet/battery cost (T6), and long sessions.
- Resolve recurring initial signaling stalls if they recur; retries are
  not proof of the root cause being fixed.
- Pacing/FEC and nearby TURN deployment remain unimplemented. ICE-TCP is
  still deferred at the user's request. RPC peerlink extraction is deferred.

[UU analysis](uu-connectivity-analysis.md) remains evidence and inspiration;
its entire feature set has **not** been implemented. No raw packet capture
was made during this turn, and no proxy rules were modified.

Artifacts: `/tmp/hermesh-peerfix10-20261113` (installed 1120, UI xcresult,
source and binary hashes, first real attempt) and
`/tmp/hermesh-peerfix11-20261113` (follow-up build and tests), and
`/tmp/hermesh-peerfix12-20261113` (NAT matrix, incoming-check observation,
installed Mac binary, source snapshot, post-1121 session and test logs).


## peerfix14 / 1123: nomination fix and bounded NAT punching

The shared host/srflx UDP mux could deliver a host's successful triggered
check through its srflx reader. The pending transaction did not retain its
sender, so the controlled selector updated the wrong pair and left the
actual nomination incomplete. A real UDP regression failed five consecutive
times before the fix. Transactions now retain their sender; restoring it
requires the same candidate ID or the same actual UDP mux, not merely equal
advertised base addresses. Both ICE roles and the different-socket rejection
case pass, including actual data after nomination.

The desktop opts into a bounded adjacent-port experiment through
`ice.BoundedNATPunchNet`. It uses existing bases with public STUN mappings,
excludes relay-server IPs, retains authenticated ICE validation and ordinary
renomination, and does not add guessed ports to the candidate table. The
budget is at most 128 attempts, one per 40 ms, for at most six seconds per
ICE generation. A dedicated worker leaves normal ICE check cadence alone;
direct success, restart and close stop it. The library defaults to disabled.
It is a limited sequential-mapping improvement, not a general random-NAT
solution. With the real one-second ICE schedule, a virtual restricted-NAT
pair switched from TURN to direct after five extra requests and carried
bidirectional data. The new punch/experiment race tests passed twice.

Mac peerfix14 was installed at 10:27:46; Duo 1123 was installed, inspected
and launched. The packaged debug library contains the new core version and
punch implementation, and both app signatures were verified. The user
reported video but still relay. Session `0fa58ebc97f33446` had relay RTT
455–463 ms; the phone assembled its first keyframe at 4.750 s. This is **not**
a successful cellular-direct acceptance result.

The first Apple RVI attempt recorded no phone packets and is not evidence
of absent phone transmission. A temporary local pymobiledevice3 11.54.0
pcapd reader subsequently worked. It retained only matching Hermesh UDP
endpoint/interface/process/length metadata and decoded STUN mapped addresses;
no raw packet file or media payload was written. The 1123 capture ran 55.64 s.
Its 1,104 observations involving the Mac public address were all outbound
on `pdp_ip0`: 368 attributed to Hermesh, 736 to Surge. Observations across
stack stages are not unique wire packets. There were zero inbound observations
from that public address. The preceding 1122 sample likewise showed actual
cellular egress. Candidate delivery and total absence of phone sends are
therefore not sufficient explanations; the responsible filtering device has
not yet been established.

Pion's default logger only emits errors, and this daemon discards stderr.
Consequently peerfix14's punch Info messages were not retained; their absence
does not establish that the worker did not run. The next Mac revision routes
bounded-probe metadata to the session's local Hermesh logger and improves
coverage when several advertised ports occupy the same small range.

Validation also exposed two test defects: TestSession read `h.cur` without
its mutex, and the late-TURN test imposed an arbitrary eight-call limit even
when candidate batches kept growing. The first now uses the existing lock;
the second verifies new batches for old peers and no further calls after
both peers complete. Both tests pass twice with race detection. Five screen
E2Es passed. The initial broader screen runs are recorded as failed, not
silently reclassified. The independent Pion TestConnStats timed out both
with and without the nomination fix; logs preserve that comparison.

Artifacts: `/tmp/hermesh-peerfix14-20261113`,
`/tmp/hermesh-bounded-punch-race-20261113.log`, and
`/tmp/hermesh-pion-connstats-{baseline,fixed}.log`.


The follow-up application validation after these test corrections passed:
screen race 66.283 s, owner race 3.964 s, relay race 11.499 s, and all five
screen E2Es 28.120 s. Logs are in `/tmp/hermesh-peerfix15-20261113`.
The phone remains 1123; the next grouped-seed change is confined to the Mac
controlled agent and its local reporting, so reinstalling the phone is not
needed for that comparison.


## peerfix15: independent port windows and visible local probe reports

Seed selection now retains a public srflx window and chooses the second
same-IP window by additional covered ports. A prflx seed is eligible only
if this generation's checklist has authenticated received requests for it;
a merely signaled prflx type is insufficient. An independent vnet regression
first sends 40 checks into the old nearby STUN windows without obtaining
direct data; the new authenticated prflx window then connects with two extra
requests, and data passes in both directions. The expanded punch suite
passed twice with race detection (26.114 s). Limits were not increased.

The desktop explicitly passes the opt-in network wrapper and a session-local
Report callback. Probe target bases/seeds and final attempted/sent counts
are therefore visible even with the launcher's stderr discarded. Logs contain
no ICE credentials. Mac peerfix15 was built from the recorded production
hashes and installed after signature verification; the phone stays 1123.
Real cellular acceptance remains pending this revision's test.


peerfix15 real session `e6137a841a4871d0` remained relayed with video. The
local report confirms 99 successful extra writes over six seconds, using
the physical and Surge bases and the srflx 20144 / authenticated prflx 15687
windows. Phone metadata again contained no incoming packet from the Mac
public IP. Thus adjacent-window expansion is insufficient for this observed
network; multi-socket collision probing is being tested separately, not
claimed as implemented or accepted yet.

## peerfix16 / 1124: multi-socket probing and nomination ordering

Both desktop and viewer now opt into the transport's explicit capability
extension. Once TURN is working, the phone opens up to 128 temporary UDP
sockets bound to its physical interface and contacts the same Mac endpoint.
An authenticated readiness message then enables at most 2048 unique random
port probes from that exact Mac base. Temporary sockets never enter the ICE
checklist. Only a request from the expected endpoint with current credentials
can promote one socket; all losers close. The pool lifetime is 15 seconds,
probes are spaced at least 4 ms apart, and restart/close/direct selection or
role changes cancel work. Old peers continue through existing ICE behavior.

The complete additional request cap is 2308 per ICE generation, including
the existing neighbor probes, pool openings and readiness retries. This
does not count normal ICE validation or network-layer retransmission/overhead.
The theoretical 98.4% collision probability assumes 128 distinct, stable
mappings on one public IP and 2048 probes without loss. It is not an observed
carrier success rate or a promise of direct connectivity.

The controlled experiment now exercises production pool creation, candidate
adoption and nomination through WebRTC: three SRTP frames and input first
pass through TURN; the selected phone candidate must then equal the actual
pool socket allowed by the virtual NAT; three more frames and input pass
through the same PeerConnection and DataChannel over direct ICE. This and
capability compatibility passed twice with race detection (18.640 s).
Production pool tests separately cover rejection of invalid integrity,
unknown public endpoints and late readiness, bounded probing, pacing,
resource reclamation, roles and stale generations.

A separate real UDP regression reproduced a delayed older nomination success
rolling selection back after a newer one. The controlling selector now keeps
the highest acknowledged nomination value while still updating statistics
for older responses. Data is verified on the newer path after reordering.
The combined ICE regressions passed with race detection (74.846 s).

Artifacts for build, installation and the next device result are collected
in `/tmp/hermesh-peerfix16-20261113`. At this stage, **5G direct acceptance is
still unproven**; the preceding installed peerfix15 / 1123 session remained
relayed. Automated relay-to-direct success does not replace the device test.


The final source set passed application race tests (screen 85.168 s, owner
4.044 s, relay 11.666 s) and all five screen E2Es (27.662 s). Both signed
packages were verified. Mac peerfix16 was installed at 11:37:50 +08:00; Duo
1124 was installed and its app inventory checked. The packaged debug library
contains the peerfix16 version and multi-socket implementation. Automatic
launch was denied because the phone was locked; manual launch was requested.

At installation the Mac had changed networks: en0 was 30.235.16.167 and a
bound-socket STUN check observed 140.205.11.255. Earlier samples used
192.168.77.95 / 183.247.171.62. The next result must be labeled with this new
network and must not be treated as an identical-environment comparison.


Device result, session `a99ea7ba3dfb2ac2`: the user reports relay at 400–500 ms.
The first keyframe reached the viewer at 4.242 s. The phone opened all 128
pool sockets, sent four readiness messages, received zero pool packets,
adopted none and reclaimed all 128 at 15.004 s. The Mac sent all 2048 probes
in 11.607 s. This is a real failed direct attempt, not an unstarted worker.

Current Mac NAT observations differ by destination port: the same physical
socket mapped to 37564 at STUN and 9760 at the relay allocation, with another
socket giving 37567 / 9763. The previous Wi-Fi had preserved the observed
port across these destinations. These measurements establish destination-
dependent mapping in this sample, not a complete filtering classification.
The user also reports that UU now appears to show UDP high-speed relay.
UU's earlier P-to-P result therefore does not establish direct reachability
on this new network. Testing peerfix16 on the earlier Wi-Fi remains pending.

The initial 55-second phone metadata window preceded the session and matched
zero packets; it is not negative evidence for this session. A second window
from 11:40:26 to 11:41:20 retained 3722 observations involving the Mac public
IP, all outbound across stack stages (1294 Hermesh, 2396 Surge, 32 unnamed).
No raw packet file or payload was retained. At 25.475 s the selected pair
briefly used USB developer ULA addresses, then restarted and returned to
relay; that interval is explicitly excluded from 5G direct acceptance.
Live `devicectl` operations can establish this developer network, so subsequent
log reads use the trusted USB House Arrest/AFC service where available, or
copy logs only after the tested session is closed. AFC successfully read only
Hermesh's `Library/Application Support/Hermesh/logs/core.log`.

A read-only deployment review also found that production still advertises
only the Silicon Valley TURN endpoint. `TURNConfig.Others` and client-side
allocation ranking exist, but the cloud center CLI does not yet populate
`Others`. A nearby relay rollout therefore requires both a real node and
cloud configuration wiring; adding a name to a deployment file alone will
not activate it. The cloud checkout and deployment were not modified.


## peerfix17 / 1125: authenticate changed peer-reflexive ports

A new virtual-network regression reproduced a separate pool-reader defect.
With destination-dependent Mac mapping and a permissive phone NAT, the
current authenticated request arrived (`received=1`) but was rejected solely
because its source port differed from the Mac's STUN mapping. The reader now
requires the expected public IP, current ICE username and valid integrity,
then lets normal ICE learn and validate the peer-reflexive source port. It
retains the same generation checks and probe/resource budgets.

The repaired case selects the new prflx endpoint on both agents and transfers
data in both directions. Wrong IPs and wrong keys are still rejected. Five
scenarios passed five repetitions under race detection (6.688 s); prior
authentication/epoch/cleanup regressions passed (4.174 s), as did the real
WebRTC SRTP/input adoption test (9.871 s). Five screen E2Es passed (28.662 s).
This fixes a packet that arrives and was rejected; it is not presented as a
fix for the current device sample's zero-arrival condition.

Additional test-only dual-NAT experiments passed twice with race detection
(5.885 s). They distinguish destination-dependent mapping from filtering.
Under the strict dual APDM/APDF model, even probing all 128 correct pool
ports fails because the Mac's changed source port is outside the phone's
filter. Coordinated next-port prediction succeeds in an isolated global
linear allocator, but one unobserved allocation breaks it. That prediction
was not added to production. The real network's precise filtering behavior
has not been classified by these model tests.

The user cannot return to the earlier Wi-Fi now; that device comparison is
explicitly deferred. Full iPhone Remote Desktop UI automation is being
completed against the final build. Its result is recorded below. Artifacts: `/tmp/hermesh-peerfix17-20261113`.
Analyzed per-packet phone metadata was deleted after aggregate counts were
saved there; no raw packets or media payloads were stored.


Mac peerfix17 was installed at 11:53:19 +08:00. Duo 1125 was installed and
verified in the device app inventory. Both signatures and the embedded
peerfix17 core were checked. The first UI run returned success but its
result bundle had zero passes and one skip because Her's binaries were not
supplied. It is recorded as **skipped, not passed**. The test was relaunched
with explicit existing Her and mock binaries. The harness now checks the
result bundle and rejects zero executed passes, preventing that false
positive on future runs. The skipped bundle is preserved separately.


Final iPhone UI result: **one passed, zero failed, zero skipped**, confirmed
from `xcresulttool` summary. The selected Computer Control flow executed for
121.972 s; the Go harness including build/setup/cleanup completed in
271.70 s. It verified screen display, touch, keyboard, clipboard, trackpad,
rotation/close and relay-to-direct continuity. The passing result bundle
is `iphone-ui-full.xcresult`; the earlier skipped run remains recorded as
skipped. The final test-harness guard was compile-checked, and both result
bundles were independently checked for their actual pass/skip counts.

Remaining device acceptance: repeat on the earlier Wi-Fi with phone 5G and
USB unplugged, verify a public direct pair and live input, then repeated
opens, sustained use, network changes and power/traffic measurements.
Current-network relay latency remains unresolved; nearby TURN needs a node
and cloud CLI wiring. No direct-success or all-device-tests-pass claim is
made. Changes are local and uncommitted on `codex/peer-udp-performance`.
