# Her and third-party AI sharing

## Model inference

The iPhone talks to its embedded Go owner service through the in-process Protobuf interface or authenticated loopback, depending on the client version. Her's messages, images, and file attachments can go through the owner's encrypted machine connection to the execution host. The kernel there assembles inference requests from messages, history, images, file contents, memory, instructions, and tool results. Compaction, Her's sub-agents, scheduled runs, and runs started by mesh news also use the host's model registry. The destination is the provider's effective API endpoint, which may be a user-configured gateway.

On the iPhone, sending a draft, retrying, or tapping a suggested answer checks the execution host before submitting content. The prompt gives the provider and API address, the categories of data, and the purpose, including scheduled work. The user can also make the decision in Her's Settings. The desktop app offers the same disclosure and controls on its Model page. Closing or declining the prompt keeps the draft. When the host is unreachable, the composer keeps new drafts until it can check the recipient.

The execution host refuses unapproved prompts before they enter conversation history. It requires a current approval matching both provider and API address before it lets the inference adapter send a request. The check applies to retries and to the endpoint returned by a managed connection. Redirects are disabled. Remote HTTP endpoints and managed transports with an unverified destination are blocked. Qoder's alternate transport and QwenWork routes that differ from their displayed endpoint require follow-up work. Approval covers one recipient per execution host, ends when the approved model switches to a different recipient, and expires when Her restarts. Revocation requires a connection to the execution host. It also cancels the inference stream and prevents retries that still hold the previous approval. Requests already handed off and data already received remain with the recipient. A new daemon requires a sidecar that understands the consent flag.

## Coding agents

Her's mesh tools can start a session, send a follow-up, or answer a coding agent. The mesh link would forward the content to the target execution machine, which would launch or write to Codex, Claude Code, or an ACP adapter. That subprocess controls its account, cloud provider, overrides, and network traffic. A model name or agent display name does not establish the inference recipient.

On a host with external coding adapters, Her offers session reads and control actions. Task starts, follow-ups, and free-text, selected-option, or multi-part answers to Codex, Claude Code, and ACP agents are blocked. The fixed decline action remains available and sends the agent only the instruction to continue without an answer. The receiving daemon checks Her's signed grant at the session RPCs. Her checks the destination on an authenticated tunnel before attempting a start or follow-up. A host that cannot confirm the in-process scripted adapter cannot receive those writes, including through the offline relay inbox. The in-process scripted adapter has no remote inference service and remains available in test installations configured to offer it.

On iPhone, substantive answers require the execution host to verify the running adapter. A new iPhone uses a dedicated RPC. An older execution host fails this check and still accepts a fixed decline. A new execution host checks the paired device's platform from the signed root chain on the legacy RPC over either TCP or QUIC. iPhone and unclassified remote owner devices require the same adapter verification. Desktop replies initiated independently by the owner continue through the established desktop path. Existing sessions remain readable, can be interrupted or archived, and may still contain context previously given to an agent. Persisted Her coding messages for unverified agents stay held when an execution host upgrades. The offline demo continues using its in-process sample tasks.

A prompt refused on account of missing approval stays out of Her's history. A connected client keeps its draft. If an older outbox item receives this refusal on reconnect, the updated owner holds it and gives the user a discard action. Regranting access does not replay a refusal recorded by the updated owner.

## Other routes and remaining risk

- MCP services, browser and desktop control, shell commands, web search, and installed connectors have their own permission and network paths. They can transfer data independently of inference. The model disclosure covers connector results when Her includes them in model context.
- Provider catalog requests, key verification and discovery, and subscription sign-in are separate setup calls. They can exchange network metadata and credentials with their configured provider and identity service. Changing a custom endpoint address requires an explicit new key when the old address had a stored key. This change gates conversational inference, including against providers added through setup.
- A custom gateway may forward requests after receiving them. Hermesh verifies the immediate endpoint, and downstream processing depends on that operator.
- Earlier daemons and clients still have their earlier behavior when used together. A third-party coding process that was already started before upgrade can retain or transmit context already handed to it. Standalone coding from other clients and standalone `her` CLI use their own controls.


## Reproducing the checks

Use fictional input, a local relay and scripted coding providers, and two loopback model receivers. Run these from the PR checkout.

```sh
go test ./internal/machine ./internal/owner ./internal/herhost ./internal/herlink ./internal/mobile \
  -run 'Test(ServeDirectOwnerOnlyAndSignedAnswerPolicy|HerCannotSendToAnUnverifiedCodingAgent|PersistedHerCodingMessageStaysHeld|HerConsentRefusalIsNotReplayedAfterRegrant|HerBlocksCodingWritesBeforeConnecting|CoreRejectsDeclineWithAnswerContent|CardsAnswer)$'
go test ./e2e -run '^TestMobileAnswersVerifiedLocalCodingAgent$'
go test ./e2e -run '^TestPushHerNotices$'
cargo test --manifest-path her/Cargo.toml -p her-llm --test http_adapters
cargo test --manifest-path her/Cargo.toml -p her-cli --test e2e_ai_consent
apps/ios/scripts/test-demo.sh
```

For the version-skew checks, build the current Her and mock first. The older daemon comes from the PR's base and runs only in the test fixture.

```sh
cargo build --manifest-path her/Cargo.toml -p her-cli -p her-mock
fixture="$(mktemp -d)"
git archive ef3f8a81 | tar -xf - -C "$fixture"
go -C "$fixture" build -o "$fixture/hermeshd" ./cmd/hermeshd
HERMESH_TEST_LEGACY_CODING_HOST="$fixture/hermeshd" go test ./e2e \
  -run '^Test(MobileBlocksCodingAnswerToLegacyHost|HerMeshBlocksHandoffToLegacyHost|HerMesh|HerCommandConnector)$' -count=1
```
