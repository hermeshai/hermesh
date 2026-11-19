# Her and third-party AI sharing

## Model inference

The iPhone talks to its embedded Go owner service through the in-process Protobuf interface or authenticated loopback, depending on the client version. Her's messages, images, and file attachments can go through the owner's encrypted machine connection to the execution host. The kernel there assembles inference requests from messages, history, images, file contents, memory, instructions, and tool results. Compaction, Her's sub-agents, scheduled runs, and runs started by mesh news also use the host's model registry. The destination is the provider's effective API endpoint, which may be a user-configured gateway.

On the iPhone, sending a draft, retrying, or tapping a suggested answer checks the execution host before submitting content. The prompt gives the provider and API address, the categories of data, and the purpose, including scheduled work. The user can also make the decision in Her's Settings. The desktop app offers the same disclosure and controls on its Model page. Closing or declining the prompt keeps the draft. When the host is unreachable, the composer keeps new drafts until it can check the recipient.

The execution host refuses unapproved prompts before they enter conversation history. It requires a current approval matching both provider and API address before it lets the inference adapter send a request. The check applies to retries and to the endpoint returned by a managed connection. Redirects are disabled. Remote HTTP endpoints and managed transports with an unverified destination are blocked. Qoder's alternate transport and QwenWork routes that differ from their displayed endpoint require follow-up work. Approval covers one recipient per execution host, ends when the approved model switches to a different recipient, and expires when Her restarts. Revocation requires a connection to the execution host. It also cancels the inference stream and prevents retries that still hold the previous approval. Requests already handed off and data already received remain with the recipient. A new daemon requires a sidecar that understands the consent flag.

## Coding agents

Her’s mesh tools can start sessions, send follow-ups, and answer questions. The execution host discloses the configured coding service, the categories of data, and the reason for sharing. For each host and agent, desktop Her settings offer separate approvals for Her’s handoffs and for iPhone answers. This approval is additional to Her’s tool approval and mesh grant. On iPhone, the question panel offers the answer disclosure at first use, and the agent’s settings on that device can revoke it.

The host recognizes Codex’s built-in OpenAI service, Claude Code’s built-in Anthropic service, supported explicitly selected Claude cloud services, and the built-in services for Gemini CLI, Antigravity, Cursor, and GitHub Copilot. The host reads local route settings and checks the installed adapter. Codex also reports the provider for the thread through app-server. The coding process manages its own connections and can keep account or downstream processing details private. These controls establish the configured service and do not attest the exact network endpoint. The host refuses content for routes it cannot identify, including opaque custom gateways and ACP agents that do not expose an identifiable service through the supported configurations.

Approvals are bound to the host, agent, observed route, and purpose. Approval expires when the execution host restarts. Her obtains a version from the authenticated execution host before forwarding content. The host verifies it again when dispatching queued work, starting or resuming an adapter, or sending answers and follow-ups. Revocation invalidates that version, including for queued or retried messages. Authorizing the route again does not release older content that still carries the withdrawn version. Previously delivered data remains with the recipient. The fixed decline remains available and carries only an instruction to proceed.

Only a trusted owner device can query, grant or revoke coding consent. The local API on a machine-only installation cannot manage it. New iPhone clients use a dedicated answer RPC and an owner-only disclosure RPC. Older iPhone clients can use a standing approval previously created on a current owner device. The new host applies the approval check to the old RPC using the signed device platform over TCP and QUIC. Desktop replies initiated independently by the owner use their existing path. New clients with pre-consent hosts can still decline but cannot send substantive answers. Her does not use offline relay delivery for protected coding content. Existing sessions stay readable and the offline demo keeps its scripted, in-process agent.

A prompt refused for missing model inference approval stays out of Her’s history. A connected client keeps its draft. If an older outbox item receives this refusal on reconnect, the updated owner holds it and gives the user a discard action. Regranting access does not replay a refusal recorded by the updated owner.

## Other routes and remaining risk

- MCP services, browser and desktop control, shell commands, web search, and installed connectors have their own permission and network paths. They can transfer data independently of inference. The model disclosure covers connector results when Her includes them in model context.
- Provider catalog requests, key verification and discovery, and subscription sign-in are separate setup calls. They can exchange network metadata and credentials with their configured provider and identity service. Changing a custom endpoint address requires an explicit new key when the old address had a stored key. This change gates conversational inference, including against providers added through setup.
- A custom gateway may forward requests after receiving them. Hermesh verifies the immediate endpoint, and downstream processing depends on that operator.
- Earlier daemons and clients still have their earlier behavior when used together. A third-party coding process that was already started before upgrade can retain or transmit context already handed to it. Standalone coding from other clients and standalone `her` CLI use their own controls.


## Reproducing the checks

Use fictional input, a local relay and scripted coding providers, and two loopback model receivers. Run these from the PR checkout.

```sh
go test ./internal/machine ./internal/owner ./internal/herhost ./internal/herlink ./internal/mobile \
  -run 'Test(ServeDirectOwnerOnlyAndSignedAnswerPolicy|HerCannotSendToAnUnverifiedCodingAgent|PersistedHerCodingMessageStaysHeld|HerConsentRefusalIsNotReplayedAfterRegrant|CoreRejectsDeclineWithAnswerContent|CardsAnswer)$'
go test ./internal/provider -run '^TestCodingRouteBindsConfiguredRecipient$'
go test ./e2e -run '^Test(MobileAnswersVerifiedLocalCodingAgent|MobileCodingConsentWithRecordingACP|HerCodingConsentWithRecordingACP)$'
go test ./e2e -run '^TestPushHerNotices$'
cargo test --manifest-path her/Cargo.toml -p her-llm --test http_adapters
cargo test --manifest-path her/Cargo.toml -p her-cli --test e2e_ai_consent
apps/ios/scripts/test-demo.sh
```

For the version-skew checks, build the current Her and mock first. The older daemon and mobile core come from the PR’s pre-consent base and run only in the test fixture.

```sh
cargo build --manifest-path her/Cargo.toml -p her-cli -p her-mock
fixture="$(mktemp -d)"
git archive ef3f8a81 | tar -xf - -C "$fixture"
go -C "$fixture" build -o "$fixture/hermeshd" ./cmd/hermeshd
go -C "$fixture" test -c -o "$fixture/mobile.test" ./e2e
HERMESH_TEST_LEGACY_CODING_HOST="$fixture/hermeshd" \
HERMESH_TEST_LEGACY_MOBILE_CORE="$fixture/mobile.test" go test ./e2e \
  -run '^Test(MobileCodingConsentWithRecordingACP|MobileBlocksCodingAnswerToLegacyHost|HerMeshBlocksHandoffToLegacyHost|HerMesh|HerCommandConnector)$' -count=1
```
