# Local Pion ICE patch

Source: `github.com/pion/ice/v4` v4.4.8, unchanged upstream files and MIT
license included. The root `go.mod` pins this copy with `replace`.
The upstream module ZIP checksum is
`h1:NB22Ze5WrgMjcf9L+O+1B29bl6anq+sbyvddImDLdhc=`.

The only upstream production changes are in `agent.go` and `candidatepair.go`:
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
