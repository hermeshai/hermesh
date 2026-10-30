# MCP gateway

Status: built, 2026-10-21 (self-hosted relay: `hermesh-relay serve --mcp`; the hosted center embeds the same gateway).

The MCP gateway lets AI agent products that only speak remote MCP (Claude.ai,
ChatGPT, Cursor, Claude Code over HTTP, ...) use Hermesh: list your machines,
start Claude Code or Codex sessions on them, follow and steer those sessions.
The agent product connects to one URL, is sent to a sign-in and consent page
the first time, and the owner approves the connection with Touch ID on their
Mac — then, or later if the Mac isn't set up yet.

The gateway is a server component in this repository, next to the relay. A
relay server embeds it the way the hosted center embeds the relay: the hosted
service runs it for everyone, and a self-hosted relay can run it for its one
owner.

## 1. Decision and what it changes

**Decision (2026-10-21).** For the cold start, MCP is served by the server
(the hosted center or a self-hosted relay), not by the owner's own machines.
It is the quickest way to put Hermesh in front of remote agent products, with
one fixed URL that agent directories can list. People who don't accept the
risk below run their own server.

**What it costs.** The gateway acts as a Hermesh client: for every approved
MCP connection the server holds a client private key and the owner-signed
Grant for it. Whoever controls the server can therefore act as each MCP
connection, within that connection's grant, and sees the content of the
sessions it reaches. This is a deliberate, opt-in exception to two rules of
technical-design.md:

- principle 2, "the relay holds no authority to make a machine execute
  anything", and
- decision #23, "P2 MCP does not rely on the relay vouching".

Both still hold for everything that is not an MCP connection. The exception
is bounded:

| Still true with the gateway | How |
|---|---|
| No MCP connection reaches a machine without the owner's Touch ID | A connection gets its power from a CLIENT pairing that a trusted device signs (§4); before that it can only report its own state (§5.7) |
| The server cannot widen a connection's access | Machines check the Grant on every RPC; a wider Grant needs the root key |
| Ordinary agents (CLI clients) and machines are unaffected | The server holds no keys for them |
| The server cannot undo a revocation | The owner device pushes revocations end to end to its machines (owner.go `revoke`); a hostile server can only delay them by dropping traffic, until the Grant expires |
| A connection ends on its own | Grants expire (30 days by default, 90 at most) |

The other P2 shape, a local stdio `hermesh mcp` that holds the client key on
the agent's side, stays planned and does not conflict: it suits agents that
run on the user's computer and needs no server trust at all.

## 2. Goals and non-goals

Goals:

- Conform to the MCP authorization spec (revision 2026-07-28): OAuth 2.1
  authorization code + PKCE, RFC 9728 protected resource metadata, RFC 8414
  authorization server metadata, RFC 8707 resource indicators, RFC 9207 `iss`.
- Work with Claude (claude.ai, Desktop, mobile, Cowork, Claude Code) and
  ChatGPT out of the box: Client ID Metadata Documents (CIMD) first,
  Dynamic Client Registration (DCR) as a fallback.
- Connecting never fails because the owner isn't ready: no Mac set up yet,
  Mac asleep, no machine online. The connection is made, says what is
  missing, and starts working by itself once it isn't (§5.7).
- One MCP connection = one Hermesh client identity = one Grant, so machines
  enforce access, attribute sessions, and revoke per connection.
- Stateless HTTP serving, so the server can run more than one instance later.

Non-goals for the first version:

- OAuth scopes. A connection can do everything its Grant allows; what it may
  reach is chosen in the Mac's approval sheet (machines, projects, agents),
  as for any agent.
- MCP on the owner's machines with SNI passthrough (the long-term option
  discussed before this decision; it is not ruled out).
- Confidential clients (`private_key_jwt`, client secrets), enterprise-managed
  authorization.
- Machine administration (adding machines, changing grants) over MCP.

## 3. Architecture

```
agent product ──HTTPS──▶ server (hosted center or self-hosted relay)
                         ├─ OAuth authorization server   /oauth/*, /.well-known/oauth-authorization-server
                         ├─ consent page                  /oauth/consent
                         ├─ MCP resource server           /mcp, /.well-known/oauth-protected-resource
                         │    └─ each tool: a hermesh CLI command, run in-process for the connection
                         └─ relay ◀── dialed in memory ──┘
                                   └──E2E mutual TLS──▶ machine (hermeshd)
owner's trusted Mac ◀─ pairing request (existing flow) ─ relay
```

Package `pkg/mcpgw`, embedded like `pkg/relay`:

```go
type Config struct {
    Issuer       string        // e.g. https://hermesh.io (where sign-in cookies live)
    ResourceURL  string        // canonical MCP URL, e.g. https://mcp.hermesh.io/mcp
    Relay        *relay.Server // pairing, grants, revocations, admission, owner presence
    RelayHandler http.Handler  // the relay's endpoints, dialed in-process (default Relay.Handler())
    Backend      Backend       // SQLBackend: SQLite in hermesh-relay, PostgreSQL in the center
    Vault        Vault         // seals client private keys (§10)
    Accounts     Accounts      // who is asking in the browser; SingleOwner for a self-hosted relay
    SetupURL     string        // where to get the Mac app (default the GitHub releases; the hosted center: its console's onboarding)
    VerifiedClients []string   // CIMD hosts shown as verified (default claude.ai, chatgpt.com, openai.com)
    Logger       *slog.Logger
}

func New(Config) (*Gateway, error)
func (g *Gateway) Mount(mux *http.ServeMux)                   // OAuth, consent page and MCP routes
func (g *Gateway) OwnerLinked(ctx context.Context, accountID string) // an account just got its owner
func (g *Gateway) Close()
```

The consent page is the gateway's own (Go templates, English and Chinese
by `Accept-Language`), on both the hosted center and a self-hosted relay.

**Tools are the CLI.** `internal/cli` runs a command for an identity the
caller holds (`cli.Exec` with a key, root chain and grant in memory, no
files) and lists its commands as tools (`cli.Tools`, from the cobra tree:
flags become parameters). Each MCP tool call runs the matching `hermesh`
command with `-o json` and answers with its document, so the tools, their
parameters and their output contract (docs/cli-output.md) stay the CLI's.
The command dials the relay through an in-memory listener (`net.Pipe`), so
it passes the relay's normal admission, grant and revocation checks; the
connection keeps that relay connection and its tunnels for its next calls
(§8).

What the gateway reuses:

| Need | Existing piece |
|---|---|
| Ask the owner for a Grant | `PairingRequest{Kind: CLIENT}`, submitted with relay `SubmitCustodialPairing`, approved by `Owner.ApproveClientPairing` (owner.go), picked up with relay `PairingResult` |
| Know whether the owner's Mac is reachable | relay `OwnerOnline(ownerID)` |
| Run tools | `cli.Exec` (the CLI's commands, its connection and grant sync) |
| Revoke | relay `ApplyRevocations` (server side) and owner `RevokeClient` (device side) |

## 4. Identity: one client per MCP connection

An **MCP connection** is one (account, agent product) pair, created by one
consent. It owns:

- a P-256 client key generated by the gateway (an `identity.SoftwareKey`),
  stored sealed (§10);
- once the owner approves, the Grant they signed for that key;
- its OAuth tokens. Tokens belong to the connection, not to the Grant, so a
  connection that is approved, renewed or re-approved keeps its tokens: the
  agent product never has to reconnect.

Because sessions belong to the client that started them (`canWrite`,
internal/machine/sessions.go:230) and `max_concurrent_sessions` counts per
client, separate identities keep two agent products (or two people's Claude
accounts) from steering each other's sessions.

### 4.1 Protocol change: who holds the key

The approving device must say plainly that the server, not the agent product,
will hold this client's key. Add to `identity.proto`:

```proto
message PairingRequest {
  ...
  // Set when a server holds this client's private key on behalf of a remote
  // agent product (the MCP gateway): the server's public URL.
  string custodian = 9;
}

message Grant {
  ...
  // Copied from the PairingRequest: the server that holds the client key.
  string custodian = 15;
}
```

- The relay accepts `custodian` only on CLIENT requests for a named owner
  submitted in-process by the gateway (`Server.SubmitCustodialPairing`);
  `SubmitPairing` over HTTP rejects it, so a network client can't fake the
  badge.
- The signer (`SigningPreview.swift`) and the app's pairing sheet show, for a
  request with `custodian`: "“Claude” connects through hermesh.io.
  hermesh.io holds this agent's key and can act as it within this access, and
  sees the sessions it reaches." The app's pairing sheet says the same
  ("“Claude (claude.ai)” connects through hermesh.io: …"), and the grant's
  page in Settings › Access shows "Connects through hermesh.io, which holds
  its key". `ListClients` carries the custodian.
- `label` carries the product name and the redirect host, e.g.
  `Claude (claude.ai)`, at most 64 bytes.

Default spec for an MCP pairing, adjustable in the sheet: machines `*` (so
machines added later are included), projects `*`, agents `claude, codex`,
**visibility ALL** (an MCP connection sees every session in its projects,
including ones started from the app or other agents; machines let it read
them, and only steer the ones it started), TTL 30 days.

## 5. Authorization server

### 5.1 Endpoints and metadata

Under `Issuer`:

| Path | Purpose |
|---|---|
| `GET /.well-known/oauth-authorization-server` | RFC 8414 metadata |
| `GET /oauth/authorize` | starts an authorization (§5.4) |
| `POST /oauth/token` | code exchange and refresh (§5.5) |
| `POST /oauth/register` | DCR, public clients only (§5.3) |
| `POST /oauth/revoke` | RFC 7009 revocation (§11) |

```json
{
  "issuer": "https://hermesh.io",
  "authorization_endpoint": "https://hermesh.io/oauth/authorize",
  "token_endpoint": "https://hermesh.io/oauth/token",
  "registration_endpoint": "https://hermesh.io/oauth/register",
  "revocation_endpoint": "https://hermesh.io/oauth/revoke",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"],
  "revocation_endpoint_auth_methods_supported": ["none"],
  "client_id_metadata_document_supported": true,
  "authorization_response_iss_parameter_supported": true
}
```

Notes:

- Claude uses CIMD only when both `client_id_metadata_document_supported`
  and `"none"` in `token_endpoint_auth_methods_supported` are present; else it
  falls back to DCR. ChatGPT prefers CIMD and, with RFC 9207 support, uses its
  stable redirect `https://chatgpt.com/connector_platform_oauth_redirect`.
- No scopes: none are advertised, a `scope` parameter is ignored, tokens
  carry none, and every token gets a refresh token.
- Discovery, registration and token endpoints answer within 1 s (Claude gives
  up after 10 s, refresh after 30 s). `/oauth/token` and `/oauth/revoke`
  accept `application/x-www-form-urlencoded`; `/oauth/register` accepts JSON.

### 5.2 Client registration: CIMD

A `client_id` that is an `https://` URL with a path is a CIMD client. The
gateway fetches the document when it first sees the URL and on cache expiry:

- Only `https`, port 443, no userinfo, no fragment. Resolve the host, refuse
  loopback, private, link-local, CGNAT and multicast addresses, and connect to
  the address that was checked (no second lookup, so no DNS rebinding).
- No redirects. 5 s timeout. At most 16 KiB. JSON only.
- `client_id` in the document must equal the URL byte for byte;
  `client_name` and `redirect_uris` are required;
  `token_endpoint_auth_method` must be absent or `none`.
- Cache per HTTP caching headers, clamped to [5 min, 24 h]. A failed refetch
  keeps the cached document until its clamp runs out.
- **Verified clients:** a document whose URL host is in `VerifiedClients`
  (initially `claude.ai`, `chatgpt.com`) shows as verified on the consent
  page and in the approval sheet. Any other host is shown with its domain and
  a warning. Logos are not shown in the first version.

### 5.3 Client registration: DCR (deprecated, kept for older clients)

- Public clients only: `token_endpoint_auth_method` `none`; a request for
  anything else gets `invalid_client_metadata`.
- `redirect_uris`: each `https`, or `http` on a loopback host.
- 10 registrations per IP per hour; responses at most 4 KiB.
- The `client_id` is `dcr_` + 128 random bits. DCR clients are always
  unverified. A DCR client never used for an authorization in 7 days, or
  unused for 90 days, is deleted by `Sweep`.

### 5.4 The authorization request

`GET /oauth/authorize` with `response_type=code`, `client_id`,
`redirect_uri`, `state`, `code_challenge`, `code_challenge_method=S256`,
`resource`.

1. **Validate before anything can redirect.** Resolve the client (§5.2,
   §5.3). `redirect_uri` must match a registered one exactly, except that for
   loopback hosts (`127.0.0.1`, `[::1]`, `localhost`) the port is ignored
   (RFC 8252 §7.3; Claude Code uses an ephemeral port). Until the client and
   redirect URI are known good, every error is an error page, never a
   redirect (no open redirector).
2. **Validate the rest**, answering errors by redirect with `error`,
   `state` and `iss`: `response_type=code`; a 43–128 character
   `code_challenge` with method `S256`; `resource` equal to `ResourceURL`
   (scheme and host compared case-insensitively, a trailing slash tolerated).
3. **Record** an authorization: id = 128 random bits (stored hashed), the
   validated parameters, expires in 30 minutes. Set a
   `__Host-hermesh_mcp_authz` cookie (`hermesh_mcp_authz` on a plain-http
   issuer) with the id, so the consent page only works in the browser that
   started the flow.
4. **Who is asking.** `Accounts.Browser(r)` returns the account and its owner
   id, which may be empty. Hosted: the `hermesh_session` cookie; with no
   session it redirects to sign-in with `next=/oauth/consent?id=…`. Signing
   in for the first time creates the account, as everywhere else. Self-hosted:
   the relay's one owner.
5. **Consent page** (§5.6): who asks, what it will be able to do, Connect or
   Cancel. Cancel redirects with `error=access_denied`.
6. **Connect** creates the MCP connection: the gateway generates the client
   key, seals it, and stores the connection in state `awaiting_setup` (the
   account has no owner yet) or `awaiting_approval`. In `awaiting_approval`
   it submits the pairing request now if the owner's Mac is online (§5.7).
7. **Finish on the page.**
   - If the owner's Mac is online, the page shows "Approve on your Mac" with
     the pairing code the Mac shows and waits up to its 10 minutes; Touch ID
     there makes the connection `active` before the agent product even gets
     its code. A "Finish later" button stops waiting.
   - Otherwise (no Mac set up, or Mac offline), and after "Finish later", the
     page shows what is missing and how to fix it (§5.7), with
     "Continue to Claude".
8. **Redirect** to `redirect_uri?code=…&state=…&iss=<Issuer>`. The code:
   256 bits, single use, 60 s, bound to the connection, `client_id`,
   `redirect_uri`, `code_challenge` and `resource`.

The authorization always completes once the person clicks Connect; whether
the owner has approved yet only changes what the tools can do.

### 5.5 Token endpoint

`grant_type=authorization_code`: `client_id`, `code`, `redirect_uri`,
`code_verifier`, `resource`. Check every binding and
`BASE64URL(SHA256(code_verifier)) == code_challenge`. A code is spent by its first
presentation, right or wrong; presented again, the tokens it gave (its
token family) are revoked (OAuth 2.1 §4.1.3).

`grant_type=refresh_token`: `client_id`, `refresh_token`, optional
`resource`.

Tokens are opaque, so revocation takes effect at once and nothing about the
owner leaks into the token:

| Token | Form | Lifetime |
|---|---|---|
| Access | `hm_at_` + 256 random bits | 1 hour, never past the Grant's `expires_at` once there is a Grant |
| Refresh | `hm_rt_` + 256 random bits | rotated on every use; dies after 30 days unused, at the Grant's `expires_at`, or when a connection that was never approved ends (§5.7) |

Only `SHA-256("hermesh-mcp-token-v1\0" + token)` is stored. The prefixes let
secret scanners recognise leaked tokens.

Refresh rotation and reuse:

- Every refresh returns a new refresh token and marks the old one rotated.
- A rotated token presented again within 60 s is a retry (clients do retry
  after a timeout): it is honoured once more, and the pair issued in between
  is revoked.
- Presented again later, or once more after its retry: the whole token
  family is revoked and the connection must be re-authorized
  (`invalid_grant`).

Errors are RFC 6749 codes (`invalid_grant`, `invalid_client`,
`invalid_request`, `unsupported_grant_type`). Responses carry
`Cache-Control: no-store`.

### 5.6 Consent page content

- The product name, and its client id host with "verified" or "unverified".
- The redirect URI's host, in full; a warning when every redirect URI is
  loopback ("any program on that computer could claim to be this app").
- The account it connects to (masked email).
- In plain words what it will be able to do once approved on the Mac: see
  your machines, projects and sessions; start sessions with Claude Code or
  Codex and talk to them — with the reminder that those agents run with full
  permissions on the projects you choose.
- The custodian sentence from §4.1.
- Which step comes next: "Approve on your Mac" when the owner's Mac is
  online, otherwise what §5.7 says is missing.
- `frame-ancestors 'none'`; actions are same-origin POSTs bound to the
  authorization id and the browser cookie.

### 5.7 Connecting before the owner can approve

A connection can be made while nothing on the owner's side is ready. Its
state says what is missing, and it moves on by itself:

| State | Meaning | Moves on when |
|---|---|---|
| `awaiting_setup` | The account has no owner: Hermesh isn't set up on a Mac | The Mac app sets up and the server links the account to its new owner → `OwnerLinked` → `awaiting_approval` |
| `awaiting_approval` | No Grant yet: the owner hasn't approved on a Mac | Touch ID on any trusted device → `active` |
| `active` | Has a Grant | — (machines may still be offline; that is reported per call) |
| `revoked`, `expired` | Ended | — |

**Getting the approval in front of the owner.** A pairing request lives 10
minutes, so the gateway doesn't submit one and hope:

- A request is sent **only when the app uses the connection**: when it is
  made on the consent page, and at each tool call while it waits. Nothing
  is sent on a timer. A use while the owner is offline is remembered
  (`Connection.Asked`) and the request goes out when the owner's trusted
  device next connects to the relay. At most one pending request per
  connection; one dismissed or left to expire comes back only when the app
  is used again (or the person presses "Send to Mac again" in the console).
- The Mac app shows the request like any pairing request (its notification
  and approval sheet).
- There is no deadline: a connection waits as long as the app keeps using
  it. One unused for a day ends with its tokens; the agent product will
  offer to reconnect.
- Only approved connections count against the 10 per owner. Up to 10 more
  may wait besides; a new one ends the oldest waiting one (`revoked`). On a
  self-hosted relay anyone can connect (§13), so waiting connections must
  not be able to keep the owner's own apps out.

**What the agent product sees meanwhile.** `tools/list` is the same in every
state (2026-07-28 wants lists that don't vary per connection). The tools
answer as the CLI would for an owner with nothing in it: `project_list` and
`session_list` succeed with empty lists and a `note` saying what to do;
`auth_status` fails with `not_paired`; every other tool fails with
`not_found` (retryable) and the same words in its `hint`. No special error
kinds for the model to learn. The note reads, for example:

- `awaiting_setup`: "Hermesh isn't set up yet. Install Hermesh for Mac from
  <setup URL>, sign in with the same account
  (y***@gmail.com), and approve “Claude” when it asks. Then try again; no
  need to reconnect."
- `awaiting_approval`: "Approve “Claude” in Hermesh on your Mac: the request
  is waiting there now. Then try again.", or, while the
  Mac is offline, "Open Hermesh on your Mac and approve “Claude” there: the
  request goes to your Mac as soon as it is online. Then try again."
- `active` with no machine online: the commands answer as the CLI does:
  `project_list` lists the machines offline, with when each was last seen;
  a command that needs one fails with `unreachable` (retryable).

`hermesh_status` always works and returns the state, the next step, the
setup URL, and once active the connection's `auth status`. The
server's `instructions` tell the model to pass a list's note on to the user
and that `hermesh_status` shows where the connection stands. Texts are English; the model relays them in the user's
language.

**What a connection can do before approval.** Nothing on any machine: it has
no Grant, so the relay doesn't admit it. It can read its own state and the
setup link, nothing else — no machine names, no account data beyond the
masked email the person already saw.

**Consent phishing gets one more angle.** Someone could get a victim to
connect an attacker's client and rely on the victim approving the request
days later without remembering. The approval sheet therefore shows the
client host, verified or not, and when and in which browser the connection
was made; unverified clients get the warning there too.

## 6. Resource server

### 6.1 Discovery and challenges

`GET https://mcp.hermesh.io/.well-known/oauth-protected-resource/mcp` (and
the same at `/.well-known/oauth-protected-resource`):

```json
{
  "resource": "https://mcp.hermesh.io/mcp",
  "authorization_servers": ["https://hermesh.io"],
  "bearer_methods_supported": ["header"],
  "resource_name": "Hermesh"
}
```

`resource` equals the URL the person pastes, exactly; Claude uses only the
first authorization server.

A request without a valid token gets `401` with
`WWW-Authenticate: Bearer resource_metadata="https://mcp.hermesh.io/.well-known/oauth-protected-resource/mcp"`
(and `error="invalid_token"` when a token was sent). Claude only starts
sign-in on a 401.

### 6.2 Per request

1. Token from the `Authorization` header only (never a query parameter).
2. Look up the hash: present, not expired, not revoked; its connection not
   `revoked` or `expired`; its `resource` equal to `ResourceURL`.
3. Reject a browser `Origin` that isn't one of ours (DNS-rebinding and CSRF
   protection the transport spec asks of servers). Agent products send no
   `Origin`.
4. Limits per connection: 120 requests/min, 20 `start_session`/hour,
   8 concurrent tool calls; per owner: 10 MCP connections. Bodies at most
   1 MiB (attachments are not accepted over MCP in the first version).
5. Tracing, off unless asked for: `HERMESH_MCP_TRACE` lists connection,
   account or owner ids whose requests the gateway logs, to learn what an
   agent product sends besides the call (headers, `clientInfo`, `_meta`,
   the tool and its argument names). Never the token, cookies, client
   addresses or argument values.

### 6.3 Transport

Streamable HTTP via `github.com/modelcontextprotocol/go-sdk` (v1.8 or later)
with `Stateless: true`: 2026-07-28 clients are served statelessly; older
clients negotiate down to 2025-11-25. No session state is kept between
requests, so any instance can answer any request. Results are listed in a
fixed order with `ttlMs` set, as 2026-07-28 asks.

## 7. Tools

`hermesh_status`, then every command of `cli.Tools`, named by its path:

| Tool | Command |
|---|---|
| `hermesh_status` | the connection's state and next step (§5.7); once active, also `auth status` and the inbox (`messages`: its webhook, waiting messages) |
| `auth_status` | `hermesh auth status` |
| `project_list` | `hermesh project list` |
| `session_start`, `session_get`, `session_list`, `session_events`, `session_answer`, `session_interrupt`, `session_archive`, `session_unarchive`, `session_diff` | `hermesh session <verb>` |
| `message_send`, `message_list`, `message_ack`, `message_status`, `message_contacts` | `hermesh message <verb>`, with the connection's inbox key ([agent-messages.md](agent-messages.md)); `message_send` is how a session is told anything more (`session_send` is gone) |
| `message_wake` | the gateway's own: a one-time link for the owner to set up the caller's doorbell, the webhook rung when messages arrive ([agent-names.md](agent-names.md) §7) |

- Parameters are the command's flags (`--question-timeout` →
  `question_timeout`; durations as strings like `30s`) and its positional
  argument (`session_id`). Flags that read this computer's files or stdin
  (`--file`, `--prompt-file`, `--message-file`) are left out; the pairing
  commands and `version` are the local CLI's own.
- Tool and parameter descriptions have MCP-specific overrides in
  `pkg/mcpgw/descriptions.go`; CLI help is unchanged. They use MCP names,
  describe the returned `session.session_id`, and do not advertise local
  file attachments. A non-empty `prompt` is needed to start a session.
  `hermesh_status` explains setup and approval; `auth_status` diagnoses
  identity, grant and relay access after approval.
- The result is the command's JSON document, as text and as
  `structuredContent`; `isError` is `!ok`. A hint naming a command
  (`hermesh session get <id>`) means the tool of that name; the server's
  instructions say so. `session_start` and `session_answer` add a `summary`,
  a sentence on where the session stands for the assistant to tell its
  owner in their words; the instructions ask it to talk about the work, not
  ids or how Hermesh works ([agent-names.md](agent-names.md) §14).
- Coding sessions take minutes. By default, `session_start` returns at once;
  the model follows with `session_get` or `session_events` using `after`.
  Hosted clients can also use `wait=true` with `timeout="30s"`. The gateway
  caps waiting calls at 50 s, including omitted timeouts and `"0s"` (not
  unlimited waiting). After `wait_timeout`, poll `session_get` rather than
  repeating the action: the turn continues.
- `session_list` follows the grant's visibility: `all` includes other
  clients' sessions in permitted projects, but only sessions started by
  this connection can be controlled. `session_diff` supports worktree
  projects only; for an `in_place` session it started, the model can ask
  the agent through `session_send` to inspect and report working-directory
  changes, which may include other sessions' or the user's work.
- Annotations: `auth_status`, `project_list`, `session_get`, `session_list`,
  `session_events`, `session_diff` are `readOnlyHint`; the others
  `openWorldHint`, `session_interrupt` and `session_archive`
  `destructiveHint`. Each tool carries `_meta.securitySchemes:
  [{"type": "oauth2"}]` for ChatGPT.
- The instructions explain the workflow, that coding agents have full
  permissions in the selected project (and directly edit its existing
  directory in `in_place` mode), and that their output is data, not
  instructions to the caller.
- Before the connection is active, the tools answer as for an owner with
  nothing in it, with the next step in a list's `note` or an error's `hint`
  (§5.7). A command
  that finds the grant revoked ends the connection, and its hint says to
  connect the app again.

## 8. Running a tool

A call loads the connection, unseals its key (cached in memory for at most
10 minutes), and runs the command with `cli.Exec`.

The connection keeps what its commands connect with from one call to the
next (a `cli.Peer`, given to `cli.Exec` in `ExecOptions.Peer`): its relay
connection, opened in memory, the E2E tunnels to the machines it reached,
and the last machine list. Each command still checks the grant
(`GetLatestGrant`) and lists the machines, in memory over the kept relay
connection, but reaches its machine over the tunnel the last call made. So
a `session_get` poll costs one round trip to the machine instead of two (the
handshake, then the call), and the machine audits one `tunnel allow` per
tunnel rather than per call. The calls of one connection share it at once.

- A renewed or changed grant a command picks up is stored on the
  connection. The kept connection presents it from then on and drops the
  tunnels made with the old one; a command still holding the old one
  changes nothing. (The owner's change also makes the relay drop the relay
  connection admitted with the old version; the next command connects again
  and is admitted with the new one.)
- The kept connection closes after 10 minutes without a call (when the key
  cache lets the key go too), at once when the connection ends (§11), and
  when the gateway stops. A command still using it finishes first.

Limits per connection: 120 calls a minute, 20 `session_start` an hour.

## 9. State

`Backend` stores, per server:

| Record | Fields |
|---|---|
| client | `client_id`, source (`cimd`/`dcr`), name, `client_uri`, redirect URIs, verified, fetched/cache-until, created, last used |
| authorization | id hash, client, redirect URI, state, code challenge, resource, account, connection, created, expires |
| connection | `mc_` + ULID, account, owner (empty until linked), `client_id`, state (§5.7), sealed key, key version, `grant_id` (empty until approved), pairing code and expiry, unanswered requests today, browser and time of consent, created, approved, last used, ended |
| code | hash, connection, client, redirect URI, code challenge, resource, expires, used |
| token | hash, kind, connection, family, created, expires, rotated, revoked |

The Grant itself stays where the relay keeps grants. `Sweep` (every 10
minutes, with the server's existing sweep) deletes expired authorizations,
codes and tokens, deletes ended connections after 30 days, and removes stale
DCR clients. The gateway's loop ends connections never approved and unused
for 30 days.

## 10. Key custody

```go
type Vault interface {
    Seal(plaintext, aad []byte) (sealed []byte, err error)
    Open(sealed, aad []byte) ([]byte, error)
}
```

- The default vault is AES-256-GCM under a 32-byte master key read from a
  file at start (mode 0600, not in the database, not in the environment
  file). The sealed form starts with a key version byte, so the master key
  can rotate (re-seal on read).
- `aad` = `"hermesh-mcp-key-v1\0" + connection_id`, so a sealed key copied
  to another row doesn't open.
- Database backups therefore hold only sealed keys. A cloud KMS can implement
  the same interface later.
- **Destroying the sealed key is the revocation that needs no one else**: no
  copy of the private key exists outside the vault, the 10-minute cache and
  the connection's kept relay connection (§8), which closes 10 minutes after
  the last call and when the connection ends.

## 11. Lifecycle

| Event | What happens |
|---|---|
| Agent product disconnects (RFC 7009 on the refresh token) | connection ended: tokens revoked, sealed key deleted, Grant (if any) revoked through the relay (`ApplyRevocations`) |
| Owner revokes in the app (Grants list, MCP badge) | existing `RevokeClient`: relay revocation + end-to-end push to machines; the gateway sees the Grant revoked and ends the connection |
| Owner disconnects in the hosted console ("Connected Apps"), approved or not | same as the first row |
| Owner dismisses the approval sheet | the request expires; it is sent again the next time the app is used |
| Owner renews or changes access in the app | `UpdateClient` re-signs at version+1; the gateway adopts it via `GetLatestGrant`; tokens keep working |
| Grant expires | refresh answers `invalid_grant`; the agent product sends the person through consent again, which makes a new connection |
| Not approved, and unused for a day | `expired`, as in §5.7 |
| Not approved, and 10 newer ones waiting | `revoked`, as in §5.7 |
| Owner reset (`ForgetOwner`) | all of the owner's connections end |

The app reminds the owner 7 days before an MCP Grant expires, with Renew (the
CLI already warns; the app gains the same for MCP connections).

## 12. Security analysis

| Threat | Mitigation |
|---|---|
| Server compromise | Acts as each approved MCP connection within its Grant, sees those sessions (accepted, disclosed in §4.1); cannot make a connection reach a machine without Touch ID, widen access, touch non-MCP clients or machine administration, or outlive the Grant's expiry. Self-hosting removes the third party. |
| Stolen access or refresh token (agent product breach, logs) | Opaque, 1-hour access tokens; refresh rotation with reuse detection; revocation in three places; bound to `resource` |
| Consent phishing (attacker's client, victim approves), including late approval | Client and redirect host shown, verified/unverified, loopback warning; product, host and time of connection in the Touch ID sheet; the custodian sentence |
| Authorization code interception | PKCE S256 only; single-use 60 s codes bound to client, redirect URI and resource |
| Mix-up between authorization servers | `iss` in every authorization response |
| Open redirect | Errors before redirect URI validation never redirect; exact matching |
| CSRF / clickjacking on consent | Browser-bound authorization id, same-origin POSTs, `frame-ancestors 'none'` |
| SSRF through CIMD fetches | §5.2 network rules |
| DCR flooding, connection flooding before approval | per-IP limits, 10 approved connections per owner, at most 10 waiting besides (the oldest makes room), unapproved connections end after a day unused, approval requests only when the app is used, at most one pending per connection |
| Prompt injection in the agent product | Access narrowed in the approval sheet; agent output labelled untrusted; per-connection rate limits; machine pause still works |
| Token passthrough | The gateway never forwards OAuth tokens; machines see only Hermesh credentials |

## 13. Self-hosted relays

`hermesh-relay serve --mcp` mounts the gateway with its state in
`<data-dir>/mcp.db` (SQLite) and the vault key in `<data-dir>/mcp-vault.key`
(created on first start). Issuer and resource are the relay's public URL
(`--public-url`, default from `--domain` or the listen address) and that URL
plus `/mcp`; `--setup-url` says where people get the Mac app. There are no
accounts: `SingleOwner` answers with the relay's one owner (none before it
is claimed, which gives `awaiting_setup`), and anyone who can reach the
relay can start a connection, exactly as with CLI pairing today; the
limits of §5.7 and the per-IP registration limit bound it, waiting
connections can't keep the owner's own apps out, and nothing reaches a
machine without Touch ID.

## 14. Testing

- `pkg/mcpgw` unit tests: authorization request validation (errors shown
  versus redirected with `state` and `iss`, loopback ports, the consent page
  bound to its browser, CSRF), code replay revoking its tokens, refresh
  rotation with the retry window and theft detection, unapproved
  connections expiring, Client ID Metadata Documents (verified, unverified,
  and each malformed kind), the fetch client refusing private addresses,
  vault binding, redirect matching, labels; a connection's calls sharing one
  relay connection, closed after 10 minutes without a call and when the
  connection ends, and going on after the owner re-issued the grant.
- `internal/cli`: the tools mirror the commands; arguments map to flags
  (values starting with "-" stay values); `Exec` answers with documents; a
  `Peer` keeps one relay connection for its commands, presents the newest
  grant any of them picked up, and stays open for a command still using it.
- Signer: a custodial grant's card says who holds the key.
- `e2e/mcp_test.go` (`TestMCPGateway`), real binaries: an app discovers the
  server from a 401, registers, and connects before Hermesh is set up (every
  tool says how to set it up); the owner sets up the Mac and the approval
  request reaches it by itself with its custodian; approval makes the same
  tokens work; `session_start --wait`, `session_send`, `session_events`, a
  missing session; the Mac going offline (machine listed offline, a session
  unreachable) and an app connected meanwhile, whose request reaches the Mac
  when it is back; a session another agent started is visible and not
  steerable; refresh; a second app approved on the consent page itself, its
  code replayed, then disconnecting (RFC 7009) and its grant revoked; the
  owner revoking the first connection.
- `e2e/latency_test.go` (`TestMCPRoundTrips`), real binaries, the Mac's link
  behind a proxy that delays every byte: a connection's first call costs the
  E2E handshake and the call, later ones one round trip to the machine; after
  the owner renames the connection, the next call makes a tunnel with the
  new grant, and the connection stays active.
- Manually with Codex (`codex mcp add <name> --url <relay>/mcp`, then
  `codex mcp login`): Codex registered with its Client ID Metadata Document
  (`https://chatgpt.com/oauth/codex/client.json`, shown verified), and
  `codex exec` ran `project_list` and `session_start` against the relay.

## 15. Milestones

1. Done: protocol (`custodian`), relay (`SubmitCustodialPairing`,
   `PairingResult`), owner (custodian carried into the grant, visibility ALL
   by default), signer and app display, `cli.Exec`/`cli.Tools`, `pkg/mcpgw`,
   `hermesh-relay serve --mcp`, tests.
2. Hosted wiring in the center (PostgreSQL backend, routing for
   `mcp.hermesh.io`, vault key file, sign-in for the consent page,
   threat-model docs).
3. Before release: a Connected Apps page in the console, renewal reminders
   for MCP grants, monitoring, then directory submissions (Claude's
   directory takes one universal URL; ChatGPT apps).

## 16. Open questions

- Should the first version accept attachments (images for the agent)? They
  need a size policy and push the body limit up.
- Verified-client list governance: who adds hosts, and do we show logos for
  verified ones?
