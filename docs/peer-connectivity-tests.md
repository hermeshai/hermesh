# Peer connectivity: the tests on real devices

For whoever runs them (a person, or an agent with a person at the phone).
[peer-connectivity.md](peer-connectivity.md) says what was built and why;
its §6 lists what the automated tests on one Mac already hold. This plan is
the rest: two real devices, real networks, and the phone's battery, which
one Mac cannot show. T12 to T14 were added after the first sessions on
real devices (design §2.4): T12 for the candidate exchange that now
repeats, T13 a question to answer before ICE over TCP is built, T14 for
the relayed path once it has servers.

The outcome is a report (§4): each test passed, failed or not run, with its
numbers. It decides three things: whether stage 1 ships as it is, how much
of stage 2 (the relayed path) is worth building, and whether stage 3
(direct RPC) can afford its pings.

## 1. Before starting

- **Builds from one commit of `main`**, the one that has this file: the
  iPhone app on a real iPhone (not the Simulator), and hermeshd with its
  screen helper on a Mac. T8 also needs the latest released hermeshd and
  the latest released iPhone app. Write the commit in the report.
- **The owner's consent, once, for**: reading the machine's log, capturing
  packets on the Mac (T6), and changing networks on both devices. Do not
  read anything else on either device.
- **The relay**: the one the devices normally use. T2 and T7 need to know
  its STUN and TURN host (the `screen: ` lines of a relayed session show
  the relayed address).
- **Hands**: steps marked ✋ need someone holding the phone or at the Mac
  (switching Wi-Fi, answering a system prompt). Everything else can be
  read from logs.

### What to read

The machine's log (`~/Library/Application Support/Hermesh/logs/` on macOS,
`~/.local/share/hermesh/logs/hermeshd.log` on Linux) has, per session:

```text
screen: candidates session=… local="host 3, srflx 1, relay 2" remote="host 4, srflx 1, relay 1"
screen: path session=… local="host 192.168.1.5:50123" remote="host 192.168.1.9:61002" after=412ms
screen: the viewer looks for a path again session=… after=1m12s
screen: the relay's STUN and TURN servers err=…
```

- `candidates`: what each end found. Two lines for a session with
  candidates later (the second is the full set); `relay 0` or no `relay`
  means the relay's TURN server was not reached from that end.
- `path`: each path taken, the machine's end first. `host`–`host` is a
  path between advertised interface addresses, possibly a VPN or debugging
  tunnel rather than physical Wi-Fi; `srflx` or `prflx` can describe a
  direct NAT path; `relay`
  on either end is relayed. `after` counts from the session opening on the
  machine: the first `path` line is how long the handshake took.
- `looks for a path again`: an ICE restart (a network change, or a path
  that broke).

On the iPhone the page's bar shows the round trip, with "Relayed" before it
when the path is relayed, and a note when both ends look to be on one
network and are relayed anyway.

For every session in the report, give: the first `path` line's kinds and
`after`, any later `path` lines, and the round trip the page showed.
Addresses may be shortened to their kind and first two numbers.

## 2. First, the automated tests

On the Mac, from the repository:

```bash
go test ./internal/screen -count=1
```

```bash
go test ./e2e -run '^TestScreen' -count=1 -v
```

```bash
HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testComputerControl go test ./e2e -run TestIPhone -v -timeout 40m
```

The UI flow opens Remote Desktop through Her's settings, so it needs both
`her` and `her-mock`. Build them with `cargo build` in `her/`, or provide
existing binaries with `HERMESH_HER_BIN` and `HERMESH_HER_MOCK`. Check the
result bundle's executed and skipped counts: Xcode can exit successfully
when every selected test was skipped. The Go harness rejects zero passes.

All must pass before anything below means something. The second prints
the times the design quotes (the first frame, the move to a direct path,
the picture after a network change): put them in the report.

## 3. The tests

Each has what to do, what must hold (**Pass**), and what to write down.
Run sessions at least as many times as stated: connection problems show
up one time in ten.

### T1. One Wi-Fi

Both devices on the same Wi-Fi, permissions granted (T3 done once).

1. ✋ Open Remote Desktop, wait for the picture, close it. 20 times, a few
   seconds apart.

**Pass**: 20 of 20 connect; every first `path` line is `host`–`host`;
`after` is under 500 ms at the median and under 1 s at worst; the page
never shows "Relayed"; the round trip shown is under 20 ms.

Write down: `after` for each run, and the round trips.

### T2. One Wi-Fi, the relay's STUN and TURN not answering

The point of candidates later: a path on one network must not wait for
servers. Make the relay's STUN and TURN port unreachable from both devices
while the relay itself stays reachable: on a self-hosted relay, start it
with `--turn-host` naming an address where nothing answers (`192.0.2.1`);
with the hosted relay, block UDP and TCP port 3478 to its host on the
router.

1. ✋ Open Remote Desktop 10 times.

**Pass**: 10 of 10 connect, `host`–`host`; `after` within 300 ms of T1's
median. The `candidates` lines show no `relay`, and
`the relay's STUN and TURN servers` or a 2 s gap before the second
`candidates` line is expected. A build before this change takes about 4 s
here: if one is at hand, one run with it shows the difference.

### T3. The local network permission

1. ✋ Delete the app from the iPhone and install it again (iOS forgets the
   permission). Join the owner, open Remote Desktop on the same Wi-Fi.
   iOS asks to find devices on the local network. Do not answer for 10 s.
2. ✋ Allow.
3. ✋ Close and reopen: direct from the start.
4. ✋ In the iPhone's Settings, turn Local Network off for the app. Open
   Remote Desktop. Then turn it on again from the note's button, and come
   back to the page.
5. ✋ On the Mac (macOS 15 or later), turn Local Network off for hermeshd
   in System Settings › Privacy & Security, open a session, then turn it on.

**Pass**: in 1 the picture comes relayed, with the note saying what to
allow. In 2, 4 and 5 the same session moves to `host`–`host` within 5 s of
allowing (a second `path` line, same `session`), the picture going on and
the note gone; the page is never reopened by itself. In 3 the first
`path` line is `host`–`host`.

Write down: seconds from allowing to the second `path` line, for each.

### T4. The iPhone changes network during a session

Start each from a live session and keep the picture moving on the Mac (a
clock with seconds, a video) so that a stall is visible.

| | ✋ Do | Expected path after |
|---|---|---|
| a | Wi-Fi off: the phone falls to cellular | `srflx`/`prflx`, or `relay` |
| b | Wi-Fi on again | `host`–`host` |
| c | Join another Wi-Fi that is not the Mac's (a hotspot from a second phone) from the Mac's Wi-Fi | `srflx`/`prflx`, or `relay` |
| d | Back to the Mac's Wi-Fi | `host`–`host` |
| e | Leave the app for 10 s and come back | as before |
| f | Lock the phone for 30 s and unlock | as before, or the page asks to open again |

**Pass**, for a to d: the same `session` goes on; a
`looks for a path again` line and a new `path` line follow the change;
the picture moves again within 5 s of the phone showing the new network
(8 s for a, where the phone itself takes time to give up on Wi-Fi); the
page never says the session failed. c and d are the cases a change of
interface name does not show: they prove the comparison of addresses works
on a real iPhone. For e and f: the picture is back within 3 s, or the page
says plainly that the session ended; it must not stay frozen.

Write down: for each, seconds from the change to the picture moving, and
the `path` lines.

### T5. Two networks

The phone on cellular, then on a second Wi-Fi (another home, an office, a
café), the Mac at home. As many kinds of network as can be reached: a home
router, cellular (most are carrier NAT), a phone's hotspot, a guest Wi-Fi
that keeps its devices apart, an IPv6-only cellular network if one is at
hand.

1. ✋ Three sessions on each.

**Pass**: every session connects. No rate of direct paths is required:
this is the baseline. On a guest Wi-Fi shared by both devices, the session
is relayed and the note shows, and stays.

Write down, per network: the kind of network on each end, the
`candidates` lines, the first path's kinds and `after`, whether a later
`path` line moved it, the round trip. This table decides whether port
mapping or more than one TURN node is worth building.

### T6. What the pings cost

A connected viewer pings every pair of addresses every second for as long
as the session lasts (peer-connectivity.md §4). Two measurements, on one
Wi-Fi, the Mac's screen still.

1. Packets. With the owner's consent, on the Mac, for 60 s of a session
   that has been connected for a minute:

   ```bash
   sudo tcpdump -ni any -w /tmp/screen.pcap udp and host <the iPhone's address>
   ```

   Count the STUN packets (the bytes `21 12 a4 42` at offset 4 of the UDP
   payload), per second and per pair of addresses. Then delete the file.
2. ✋ Battery. A 15 minute session on Wi-Fi with cellular on, and one with
   cellular off (Settings › Cellular), the screen still, the phone not
   charging. Xcode's energy gauge or Instruments' Energy Log for the app;
   failing that, the battery percentage before and after, and whether the
   cellular indicator in Control Center shows activity.

**Pass**: under 50 STUN packets a second seen at the Mac; the energy use
of the two runs within a tenth of each other (the pings from the cellular
interface cost nothing worth it). Either one failing is a finding to fix
before stage 1 ships: say which pairs the packets belong to.

Write down: packets a second, the number of pairs, the two energy readings.

### T7. The relayed path

1. From each place of T5 where the session was relayed, note the round
   trip the page shows.
2. From the same two networks, the round trip to the relay's TURN host
   (`ping`, or the time of a TLS handshake to the relay).

**Pass**: nothing to pass. Write down the relayed round trip beside the
sum of the two ends' round trips to the relay. If they agree, the node's
distance is the whole delay, and a nearer node (stage 2) removes it.

### T8. Old and new together

| Viewer | Machine | Where |
|---|---|---|
| this build | the latest released hermeshd | one Wi-Fi, then cellular |
| the latest released app | this build | one Wi-Fi, then cellular |

**Pass**: all four connect; the times are those of the older build (about
what T1 gave before this change), never worse. The older machine logs none
of the lines above; with the older viewer this build logs one `candidates`
line a session.

### T9. The Mac changes network

1. ✋ During a session, move the Mac to another Wi-Fi (or pull its
   Ethernet cable, with Wi-Fi as the fallback).

**Pass**: the same session recovers by itself. Expected about 7 to 10 s
(the viewer waits for the path to be declared dead: a known limit,
peer-connectivity.md §2.2). Over 15 s, or a session that ends, is a
finding.

### T10. A Linux machine

A Linux machine with Remote Desktop
([computer-control-linux.md](computer-control-linux.md)) on the phone's
Wi-Fi: T1 with 5 runs, and T4 a and b.

**Pass**: as T1 and T4. A firewall on the machine that drops UDP shows as
relayed with the note: write down what had to be opened.

### T11. A long session

1. ✋ A relayed session (the phone on cellular) kept for 70 minutes, the
   phone awake.
2. ✋ Then Wi-Fi on and off again, as T4 a and b.

**Pass**, against a relay built from this commit or later: the session
lasts the 70 minutes, and in 2 it finds a path again as in T4 (the
machine's `candidates` line after the restart has `relay` on both sides).
Against an older relay the session is expected to lose its path 20 to 60
minutes in: that relay refuses a credential past its hour at every
refresh. Write down which relay it was, and when the picture stopped.

### T12. Candidates that come late

`TestLateTURNAddressIsExchanged` holds this on one Mac. On real devices,
with the relay's TURN server far away (its allocation over TCP then takes
about two seconds):

1. On a network that blocks UDP to the TURN server and passes TCP (block
   UDP port 3478 to its host on the router), with the two ends on different
   networks, open Remote Desktop five times.

**Pass**: five of five connect, relayed. The machine's log has more than
one `candidates` line for the session, the last with `relay` on both
sides; the first `path` line comes within 3 s of the last `candidates`
line. No `looks for a path again` line: nothing was restarted to get there.

### T13. Before ICE over TCP is built: can a TCP connection be made?

ICE over TCP is set aside (design §5.3): it helps only where the two ends
cannot exchange UDP and can make a TCP connection to each other. This
test finds out whether such a network is among those the owners use. It
builds nothing.

1. On each network of T1 and T5 where the session was relayed though both
   devices were on it (the note showed), on the Mac:

   ```bash
   python3 -m http.server 8765 --bind <the Mac's address on that network>
   ```

2. ✋ On the iPhone, open `http://<that address>:8765` in Safari. Then stop
   the server.

**Pass**: nothing to pass. Write down, per network: whether the page
loaded, and the address the server's log shows the request came from. Loaded,
from the phone's address on that network: a TCP connection passes where
UDP did not, and ICE over TCP would connect there. Not loaded: the network
keeps the two apart, or the phone's proxy app took the request (its
address in the log, if any, is not the phone's): say which the log shows.

### T15. How each network maps a socket

Mapping observations are in one log line per end (design §2.5), with both
ends built from this commit. These observations alone do not establish
why a session is relayed:

```text
screen: network session=… end=machine found="system 183.0.2.9:54428>54428 54430>54430; 192.168.1.5 183.0.2.9:53344>53344 53345>53345"
screen: network session=… end=viewer found="answered in 812ms, try 1; system …; 10.20.3.4 …"
```

For each way a packet leaves (`system`, then each local address) and each
of two sockets: the port the STUN server saw, `>`, the port a relayed
address on the same server saw. `none`: no answer that way.

1. ✋ One session each: the phone on cellular and the Mac at home; the
   phone on the office Wi-Fi and the Mac at home; both on the home Wi-Fi.
   With the proxy apps as the owner normally has them.

**Pass**: nothing to pass. Write down both lines of each session, and for
each end and way say which of these it shows:

- the same port to both destinations (`40001>40001`): the checks alone
  observed a stable mapping for those destinations; filtering is still
  unknown, so this does not prove direct reachability;
- different ports, a small step apart from socket to socket
  (`28293>28294 28296>28297`): record the samples, without assuming the
  peer-directed port follows the same pattern;
- different ports far apart (`51350>39438`): the advertised STUN mapping
  may not be the peer-directed mapping. The other end's filter and
  peer-reflexive discovery determine whether standard checks can connect.

And `answered in`: over a second is the tunnel to the machine, which the
relayed path's servers (T14) do not change.

### T14. Reachable, low-latency relay fallback

Requires the TURN servers of design §5.1. In an isolated test network,
block direct paths and UDP to TURN, then TCP 3478, leaving TURN over TLS
on 443 reachable. Check that the certificate is verified (a server with an
invalid one must be refused), ten sessions with picture and input, one
server stopped during a session and at the start of one, and which server
each end kept (the `relay` addresses in the machine's `local_addresses`
and `remote_addresses`). Run T11 again: a credential running out must no longer end
the session, nor a restart after 70 minutes.

✋ On the actual Wi-Fi and cellular networks, compare available regions at
the same bitrate and resolution. Record allocation time, selected path
RTT (median/p95), first frame, freezes and signal/answer latency separately.
Choose and record the region-specific latency target before deployment;
neither a region name nor an echo test proves desktop performance. Compare
with another remote desktop app on the same networks as a baseline for how
it feels, without assuming how that app connects. TLS on 443 does not pass
a network that requires an authenticated HTTP proxy.

### T16. Physical UDP mapping and first-frame recovery (peerfix10–12)

Automated checks:

```bash
(cd third_party/pion-ice && go test . -race -run '^Test(HostSocketDiscoversPublicAddressAndConnects|NATDirectReplacesRelay|RenominationKeepsStableRelayAndFindsNATDirect)$' -count=3)
go test ./internal/screen -race -run '^TestViewerRecoversFirstFrameOnHighRTT$' -count=3
```

The first blocks the default UDP route, verifies srflx candidates share
physical host base ports, exchanges data through two simulated NATs, and
repeats after ICE restart. The second uses WebRTC SRTP and RTCP NACK on a
550 ms RTT virtual path, loses one initial-keyframe packet, and requires
byte-identical recovered frames with no UI status callback or replacement
keyframe.

The NAT tests also cover a destination-dependent phone mapping with two
machine filters: an IP-only filter permits standard direct discovery and
TURN-to-direct upgrade; an exact IP/port filter retains a working relay in
the bounded test. Neither identifies the real network's filter type.

✋ For the real-device comparison, unplug USB, keep both Surge instances
in their normal working state, and connect from Duo on 5G to yee-homebook.
Record 3 opens: success, displayed direct/relay and RTT, first picture time,
and whether input remains usable for 60 seconds. Correlate both ends'
candidates and selected pair with the phone core's `viewer first packet`,
`viewer first frame` and `viewer receive ended` logs. An assembled frame
alone is not proof of successful native decoding/display. Repeat on common
Wi-Fi, then switch Wi-Fi/5G during one session. Retain failures as failures;
no manual proxy rules are part of the acceptance criteria.

On Mac peerfix12, correlate `screen: checks received` at 4 s, 12 s and
session end with `screen: public direct check received` (at most 16 distinct
pairs). These count authenticated arrivals even after TURN is connected;
they add no network probes. If public direct requests arrive but selection
stays relayed, inspect ICE response/selection. If none arrive, distinguish
network loss/filtering from packets rejected before authentication using
the authorized scoped capture; do not infer a particular router or proxy
is responsible from a zero counter alone.

### T17. Interrupted restart signaling (peerfix13 / phone 1122)

```bash
go test ./internal/screen -race -run '^Test(SessionRecoversInterruptedRestart|SignalRetriesOnlyTransientErrors)$' -count=1
```

The full screen/helper test drops a restart request or its answer. It also
exhausts the first restart's retries, then restores signaling. Require the
same session, a stable signaling state, more received frames and input at
the helper. A retry must finish the pending offer before creating another;
it must not retry permission/not-found errors as transient failures.

✋ Leave a working session open while switching Wi-Fi/5G and back, keeping
the normal Surge settings. Correlate network-change events, restart errors,
selected paths and uninterrupted/recovered picture and input on both ends.
Record 5G direct/relay separately: successful restart recovery is not proof
that cellular UDP hole punching succeeded.


## 4. The report

One table, one row per test:

| Test | Result | Numbers | Notes |
|---|---|---|---|
| T1 | pass / fail / not run | medians, worst | what was seen |

Below it: the commit, the devices and their system versions, the networks
used, the automated tests' times (§2), and for every failure the log lines
of that session. Findings are reported, not fixed in place: a change to the
connection code goes back through its own tests first.
