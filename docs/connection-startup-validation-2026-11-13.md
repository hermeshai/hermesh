# Home Wi-Fi connection startup validation

Date: 2026-11-13. Workspace: codex/peer-udp-performance, base 2a4af781.
The nearby TURN plan remains separate; this change targets current LAN startup.

## Device baseline

Both devices were on the home Wi-Fi: Mac 192.168.1.127, Duo 192.168.1.140.
The installed baseline was Mac peerfix17 / iPhone 1125.

| Session | Viewer first frame | Connect attempts | Answer to first frame |
| --- | --- | --- | --- |
| 399c9eb20eba1b1e | 16.526 s | 6.002 s timeout, 6.001 s timeout, 4.271 s success | 198 ms |
| fad1a00cfb293efc | 11.534 s | 6.001 s timeout, 5.300 s success | 166 ms |

Mac candidate/answer preparation was tens of milliseconds. The screen audit
records also completed at those times, using the machine software signing
key; no interactive signer is implicated. The dominant measured wait was
a Connect RPC response. Baseline logs cannot by themselves identify its
entire transport-level cause.

## Implemented repair

A reproducible cache race could amplify reconnect churn. Concurrent calls
held the same old machine tunnel. One failure removed it, another call
created a replacement, and a delayed failure of the old call then closed
the healthy replacement by machine ID.

ForgetMachine now compares the exact failed Machine instance under the
cache mutex and removes only that instance. It releases the mutex before
closing the old network connection. Error recovery in Screen, Her, Admin,
FanOut and CLI forwarding uses the tunnel each call actually held. Explicit
Forget by ID remains available for deliberate invalidation.

The real HTTP/2 regression reproduces two old failures interleaved with a
new tunnel. The replacement must answer before and after the delayed
failure, without a third dial. It failed before the fix and passes with
race detection ten times. This proves repair of the race; it does not yet
prove that the full device startup delay is removed.

Existing phone signal logs now include cumulative relay_after, tunnel_after
and rpc_after timing, plus an initial Connect request event. Mac logs show
Host answer and audit durations in signal answered. No SDP or credential is
logged. Differences between the cumulative phone fields give each stage.

## Automated verification

- transport / peer / owner / CLI race suites: pass, 12.700 / 3.244 / 4.117 / 2.854 s.
- New HTTP/2 recovery race test, ten repeats: pass, 1.421 s.
- Five screen E2Es: pass, 29.254 s.
- Actual Host/Viewer/SRTP/helper/input with both server refreshes pending and
  silent STUN: pass twice under race, 3.337 s total; first frame 861 / 874 ms.

Independent cache characterization found duplicate cold Warm/Get requests,
late old-key overwrites and TURN ranking that does not inherit caller
cancellation. Those experiments are preserved outside the repository and
are not production fixes in this change. The 16.526 s viewer clock starts
after the owner server-list lookup, so these cannot explain that measured
interval.

## Installation and device acceptance

Mac peerfix18 was installed at 22:34:11 +08:00; Duo 1126 was installed.
Both signatures and the linked phone core version and repair symbols were
verified. The user was asked to repeat one home-Wi-Fi connection and close
it after ten seconds of video. Device acceptance is pending.

Artifacts, package hashes and baseline logs:
/tmp/hermesh-connect-speed-20261113
/tmp/hermesh-signaling-review-20261113

No claim of a measured device speedup is made until the result is recorded.


## peerfix18 device result

The user reported a connection error rather than a speedup. At 22:38:24
the cached tunnel was obtained immediately; its Connect RPC timed out at
six seconds despite the Mac logging a complete answer in 131 ms with zero
audit delay. Subsequent fresh machine sub-tunnels on the same outer relay
connection failed their TLS handshake twice at six seconds. A second open
failed similarly; one handshake eventually took 4.173 s and left too little
time for its RPC before the attempt deadline. There were no authenticated
ICE checks on the first Mac session. This run is failed, not accepted.

Resource inspection found both center containers healthy, each at about
0.18% CPU with memory below its limit. A scoped log query returned no
matching tunnel-forwarding/cluster errors. This does not prove the network
path healthy or identify the source of the transport outage.

The next repair targets explicit, bounded recovery of a failed outer relay
carrier after a fresh machine TLS handshake confirms a transport timeout.
Ordinary canceled calls and authentication failures must not trigger it.


## peerfix19 recovery change

The peer layer now marks only a new relay-backed tunnel's TLS transport
timeout with the exact outer connection used. RecoverTunnel is explicit: it
closes that carrier only while it is still current. It does not act on
ordinary RPC deadlines, cancellation, identity/credential rejection or a
late failure from an already replaced carrier. The screen wrapper may
request recovery once per Connect request; Candidates cannot trigger it.
Existing three-attempt limits and handshake deadlines are unchanged.

A real WebSocket/Yamux server answered pings while discarding tunnel data.
New child tunnels on it still timed out; replacing the outer connection
restored real mutually authenticated TLS and HTTP/2 to the same machine.
Tests also cover sixteen concurrent late errors and the per-request screen
recovery limit. A genuinely sleeping machine can incur this one outer
reconnect too, interrupting other tunnels on it; this is the bounded
tradeoff, not proof that every TLS timeout means the relay is faulty.

Race repeats: peer 3.429 s and owner 4.417 s. Related complete race suites:
transport 12.700 s, peer 3.646 s, owner 4.351 s, CLI 2.780 s. Five screen
E2Es passed in 29.116 s. Artifacts: /tmp/hermesh-connect-speed19-20261113 and
/tmp/hermesh-signaling-review-20261113. Mac peerfix19 installed at
22:52:22 +08:00; signed Duo 1127 package verified before installation.
Real-device connection and startup acceptance remain pending this revision.


## Final result before saving this work

The user retested peerfix19 / Duo 1127 and reported that it still could not
connect and was too slow. The Mac's latest session 25966b046475ee35 encoded
a frame after 508 ms but failed its connection after 17.124 s. Actual
device startup and connectivity acceptance therefore **failed**; no
measured speedup is claimed. Detailed phone logs from this final attempt
have not yet been retrieved, so the exact remaining wait is unresolved.

The complete Simulator Computer Control UI flow passed: one test passed,
zero failed, zero skipped, verified from the result bundle. The UI case
executed for 120.153 s; the Go test completed in 255.19 s. This automated
result does not override the failed real-device result.

At the user's request, all current local code, tests and planning/validation
documents are being saved as a local commit. Further connection changes
and additional device retries are deferred while this snapshot is saved.
