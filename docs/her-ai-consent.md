# Her and third-party AI sharing

## Model inference

The iPhone talks to its embedded Go owner service through the in-process Protobuf interface or authenticated loopback, depending on the client version. Her's messages, images, and file attachments can go through the owner's encrypted machine connection to the execution host. The kernel there assembles inference requests from messages, history, images, file contents, memory, instructions, and tool results. Compaction, Her's sub-agents, scheduled runs, and runs started by mesh news also use the host's model registry. The destination is the provider's effective API endpoint, which may be a user-configured gateway.

On iPhone (before the first actual send to a third-party model) and on desktop, sending a draft, retrying, or tapping a suggested answer checks the execution host before submitting content. The localized confirmation discloses the provider and API endpoint, the selected model, the categories of data, and the purpose, including scheduled work. Users can also review or revoke sharing in Her's Settings (iPhone) or Model page (desktop). Declining or closing the prompt keeps the draft and blocks only Her's model inference; it does not affect direct Coding Agent sessions or answering coding agent questions.

The execution host refuses unapproved prompts before they enter conversation history. It requires a current approval matching both provider and API address (`Recipient { provider, endpoint }`) before it lets the inference adapter send a request. The check applies to retries and to the endpoint returned by a managed connection. Redirects are disabled. Remote HTTP endpoints and managed transports with an unverified destination are blocked. Qoder's alternate transport and QwenWork routes that differ from their displayed endpoint are blocked until verified. Approval covers one recipient per execution host, persists across Her restarts (`ai-consent.json` in Her's home directory), and ends immediately when revoked, when the session switches to a different recipient, or when a custom endpoint address changes. Revocation requires a connection to the execution host; it removes the persisted grant, cancels in-flight inference streams, and prevents retries holding the previous approval. Requests already handed off and data already received remain with the recipient. A new daemon requires a sidecar that understands `--require-ai-consent`.

A prompt refused for missing model inference approval stays out of Her's history. A connected client keeps its draft. If an older outbox item receives this refusal on reconnect, the updated owner holds it and gives the user a discard action. Regranting access does not replay a refusal recorded by the updated owner.

## Coding agents and existing authorization rules

Coding-specific consent and the standalone desktop **Coding agent sharing** UI introduced in #25 have been removed. Direct Coding Agent sessions, iPhone/desktop answers, and Her's coding handoffs (`session_start`, `session_send`, `session_answer`) use Hermesh's existing authorization model:

- **Owner device trust**: Paired trusted owner devices (Mac, Windows, Linux, iPhone) can view sessions and answer coding agent questions directly. Declining a question still rejects non-empty answer payloads.
- **Per-operation approval**: Her's permission mode (`manual`, `accept-edits`, `bypass-permissions`) governs tool calls; in `manual` mode, Her waits for explicit owner approval before invoking coding tools.
- **Signed mesh grants**: Her's access to machines, projects, and coding agents is enforced by the owner-signed mesh grant (and ends immediately when Her is turned off or its grant is revoked).
- **No silent bypass**: Neither clients nor Her ever bypass an active authorization rule (including Her model inference consent, per-operation tool approval, or an un-upgraded `v0.1.7` host's active coding-consent gate).

## Client and host interoperability

- **Current clients ↔ current host**: Her model inference uses `ai_consent/*` with provider, endpoint, model, data, and purpose disclosure. Coding agents and iPhone answers work directly under existing device trust, per-operation approval, and signed grants.
- **Pre-#25 (`v0.1.6`) clients ↔ current host**: Fully compatible. Standard `StartSession`, `DeliverMessage`, `SendMessage`, and `AnswerQuestion` RPCs work directly.
- **Current clients ↔ pre-#25 (`v0.1.6`) host**: Direct coding agent sessions and iPhone answers work directly (`AnswerQuestion`). For Her model inference, `herAnswers(machine.her, "ai_consent/status") == false` prompts the iPhone user to update Her's computer before sending.
- **Installed `v0.1.7` clients ↔ current host**: The legacy `v0.1.7` RPCs (`AnswerLocalQuestion`, `CheckLocalCodingRecipient`, `CodingConsent`) and `Question.local_answer_available` remain wire-compatible on the host. `AnswerLocalQuestion` delegates to `AnswerQuestion` (preserving owner/grant/decline checks), `CheckLocalCodingRecipient` enforces session read access when given a session ID, and `CodingConsent` stays strictly owner-only (rejected for non-owners and machine-only local APIs) while reporting approved for non-revoke calls so already-installed `v0.1.7` iPhone and Her clients continue working without errors.
- **Current clients ↔ un-upgraded `v0.1.7` execution host**: Current clients do not attempt to bypass an un-upgraded `v0.1.7` execution host's active coding-consent gate. On an un-upgraded `v0.1.7` host, iPhone answers and Her coding handoffs remain subject to that host's `v0.1.7` rules until the execution host is updated.

## Apple Guideline 5.1.2(i) scope and remaining undisclosed data paths

App Store review decisions rest solely with Apple; implementing the Her model inference disclosure does not guarantee approval. The following data paths are **outside** Her's model inference disclosure gate and should be reviewed explicitly against Apple's Guideline 5.1.2(i) and App Privacy disclosures:

- **Direct coding agent sessions and answers**: Local coding agent subprocesses (Claude Code, Codex, Gemini CLI, Antigravity, Cursor, GitHub Copilot, and other ACP adapters) run on the user's computer, manage their own accounts and network connections, and transmit prompts, repository context, and answers (from desktop, iPhone, or Her's approved tool calls) to their configured AI providers without a coding-specific disclosure gate inside Hermesh.
- **Her tools and connectors**: MCP services, web search providers, browser and desktop control, shell commands, and installed connectors have their own permission and network paths and can transmit data independently of Her's model inference (their results are covered by Her's model disclosure only when included in model context).
- **Provider setup and metadata calls**: Provider catalog fetches, API key verification/discovery, and subscription sign-in (such as ChatGPT OAuth) exchange network metadata and credentials with their configured provider and identity endpoints outside the chat inference gate.
- **Custom gateways and downstream processors**: When the user configures a custom model endpoint or gateway, Hermesh discloses and verifies the immediate endpoint URL, but downstream forwarding and retention depend on that operator.
- **Standalone `her` CLI and earlier versions**: Standalone `her` runs without `--require-ai-consent` unless passed explicitly, and earlier daemons/clients keep their earlier behavior when used together.
