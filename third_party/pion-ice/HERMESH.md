# Local Pion ICE patch

Source: `github.com/pion/ice/v4` v4.4.8, unchanged upstream files and MIT
license included. The root `go.mod` pins this copy with `replace`.
The upstream module ZIP checksum is
`h1:NB22Ze5WrgMjcf9L+O+1B29bl6anq+sbyvddImDLdhc=`.

The first local change is in `agent.go` and `candidatepair.go`:
relay STUN writes run outside the ICE task loop. Pion TURN synchronously
creates a permission for a new peer IP during `WriteTo`; several slow or
denied permissions otherwise block all incoming checks, including a working
LAN path, until ICE fails. There is at most one write per candidate pair and
STUN message type in flight. Duplicate checks are dropped while it is blocked;
normal STUN retransmission continues. Requests and responses have separate
slots. Host and server-reflexive writes, media packets, authentication and
the TURN server's permission rules are unchanged.

A second change, in the same function: a check from a relayed candidate
whose server is on the internet is not written to a private, loopback or
link-local address (`relayCannotReach`). Such a server forwards to none (the
Hermesh relay refuses the permission, as coturn does by default), and the
TURN client asks for a refused permission again at every write: each such
pair cost a round trip to the server at every check, for as long as the
connection lasted. Two loopback peers told of twelve private addresses asked
a TURN server 106 times in 3.5 s; with this, twice. A server on a private
network, or on the same computer, is asked as before. This one is Hermesh's
own and need not go upstream as it is: pruning such pairs is the better
upstream fix. Regressions: `TestRelaySTUNSkipsPrivatePeersOfAPublicServer`
here, `internal/screen/TestPublicTURNIsNotAskedForPrivatePeers`.

Reproduction: two loopback peers with twelve unreachable host candidates;
delay TURN CreatePermission responses by 150 ms and reject those addresses.
Upstream fails after about 23 seconds despite the reachable loopback path;
the patch connects in under one second. The application regression is
`internal/screen/TestSlowTURNDoesNotBlockDirectChecks`.

Run upstream tests from this directory with `go test -race ./...` and the
application screen unit and end-to-end tests from the repository root.
Remove the replacement when an upstream release includes an equivalent fix
and passes the same regression. Do not replace this with module-cache edits.

## UDP candidate reuse and path selection (2026-11-13)

`gather.go` also probes STUN from each ordinary UDP host socket through
UniversalUDPMux, publishing srflx candidates with that host's base port.
The host owns and closes its mux on failure, close and ICE restart. System
route STUN and TURN are retained, with no new per-interface TURN allocation.
Externally supplied muxes, mDNS gathering and address rewrites retain their
previous paths. `TestHostSocketDiscoversPublicAddressAndConnects` blocks the
wildcard route and verifies NAT data plus ICE restart through physical bases.

In `agent.go`, a relayed pair's type quality is limited by its weaker end;
a host-relay pair must not outscore a working srflx-prflx direct pair solely
because one end is host. Direct promotion includes srflx/prflx. RTT changes
must exceed both 10 ms and 15%; score gains must exceed both 5 points and
15% of the absolute old score, avoiding reversed comparisons below zero.
`TestRenominationKeepsStableRelayAndFindsNATDirect` covers these regressions.

While relayed with automatic renomination enabled, the controlled selector
continues bounded checks on waiting/in-progress pairs. Otherwise a late
public candidate cannot open its NAT filter, even though the controlling
end keeps checking it. It does not nominate, does not recheck succeeded or
failed pairs, and retains `maxBindingRequests`. `TestNATDirectReplacesRelay`
uses real TURN and two virtual NATs: data first over relay, then over direct
public candidates without restarting the session. It failed before the
controlled-side change and passes after it. These are local changes pending
upstream consideration; no module-cache source is modified.

## Shared-socket response association and bounded punching

Binding transactions now retain the candidate that sent them. When a UDP
mux delivers a response through a host/srflx alias, both selectors update
the original pair. Alias recovery requires the same actual mux; matching
advertised addresses is insufficient. `TestHostSRFLXMuxPreservesNomination`
uses real UDP to verify both roles, rejection through a different socket,
and application data after nomination. This fixes an incomplete triggered
nomination, not an absent inbound packet at the network interface.

`punch.go` is an opt-in Hermesh extension, disabled by default. Callers may
use `WithBoundedNATPunch` or pass `BoundedNATPunchNet` through `WithNet`.
The authenticated controlled peer may send bounded standard ICE Binding
requests to nearby ports of public remote candidates while using TURN.
No guessed candidate is inserted and no unauthenticated response can select
a path. Bases must have a public STUN mapping; aliases and destination
probes are deduplicated, TURN-server IPs are excluded. There are at most
128 attempts, a minimum 40 ms spacing, and a six-second per-generation
limit. A separate worker leaves ordinary ICE cadence unchanged and stops
on direct success, close or restart. It relies on the controlling peer's
ongoing checks to discover and nominate the working reflexive address.

Seed selection avoids spending two windows on almost identical srflx ports.
The second window maximizes added coverage on the same public IP; prflx
seeds must have received authenticated ICE requests in this generation.
The optional Report callback records only addresses, ports and bounded
attempt totals in the application's local log. Nearby windows cannot
reliably discover arbitrary destination-dependent mappings; these are
limited improvements, not a promise of universal direct connectivity.

`TestBoundedPunch*` covers relay-to-direct data with the production one-second
ICE schedule, integrity rejection, budget/rate/deadline, restart cancellation
and seed selection. `TestNATPunchExperiment*` is confined to virtual networks
and explicitly distinguishes controlled allocation gaps from a random NAT.

## Authenticated multi-socket punching

`birthday.go` extends the same opt-in transport with the candidate extension
`hermesh-punch 1`. Both peers must advertise it. While relayed, the controlling
peer opens at most 128 temporary UDP sockets on a physical interface that has
a public STUN mapping. Each contacts the same controlled peer endpoint before
an authenticated readiness message is sent over the selected path. The
controlled peer verifies its own mapped endpoint and a current authenticated
source on the remote public IP, then probes at most 2048 distinct ports on
that IP, spaced at least 4 ms apart. TURN-server IPs are excluded.

The pool lives for at most 15 seconds. Only a Binding request from the expected
public IP with the current ICE username and valid MESSAGE-INTEGRITY can
promote one socket into the candidate table. A changed source port is learned
through normal peer-reflexive ICE processing. All losing sockets close;
the winner continues through normal ICE validation and nomination. Direct
selection, close, restart and role changes cancel temporary work. Duplicate
readiness cannot reset the budget. The readiness deadline reserves two seconds
for network delay. No speculative port becomes a candidate or pending check.

The maximum additional request budget is 2308 per ICE generation: 128 existing
neighbor probes, 128 pool openings, four readiness messages and 2048 random
probes. Normal ICE validation and keepalives are additional. This is a bounded
connectivity attempt, not a guarantee that a carrier or proxy permits it.

`TestBirthday*` covers authentication, budgets, pacing, ownership, cancellation,
role changes and stale generations. The application regression
`TestBirthdayAdoptionPreservesWebRTCMediaAndInput` carries real SRTP frames and
SCTP input through TURN, adopts a production pool socket, then continues both
streams over direct ICE without recreating the PeerConnection.

`TestBirthdayAuthenticatedReflexivePort` reproduces a changed Mac source port
that reaches a permissive phone NAT. Rejecting that authenticated same-IP
request prevented a working path; accepting it now nominates the reflexive
endpoint and carries bidirectional data. Wrong IPs and keys still fail.
The test also distinguishes accepting a request from achieving connectivity:
two destination-dependent mappings can still prevent the return path, and
an exact-port NAT filter can discard the request before it reaches the pool.

The controlling selector also retains the highest acknowledged renomination
value. A delayed older success still updates its pair statistics but cannot
undo the new selection. `TestRenominationIgnoresOutOfOrderSuccess` reproduces
the previous rollback using authenticated UDP and verifies subsequent data
on the newer path.
