# Her and third-party AI sharing

## Model inference

The iPhone talks to its embedded Go owner service over authenticated loopback. Her's messages, images, and file attachments can go through the owner's encrypted machine connection to the execution host. The kernel there assembles inference requests from messages, history, images, file contents, memory, instructions, and tool results. Compaction, Her's sub-agents, scheduled runs, and runs started by mesh news also use the host's model registry. The destination is the provider's effective API endpoint, which may be a user-configured gateway.

On the iPhone, sending a draft, retrying, or tapping a suggested answer checks the execution host before submitting content. The prompt gives the provider and API address, the categories of data, and the purpose, including scheduled work. The user can also make the decision in Her's Settings. The desktop app offers the same disclosure and controls on its Model page. Closing or declining the prompt keeps the draft. When the host is unreachable, the composer keeps new drafts until it can check the recipient.

The execution host requires a current approval matching both provider and API address before it lets the inference adapter send a request. The check applies to retries and to the endpoint returned by a managed connection. Redirects are disabled. Remote HTTP endpoints and managed transports with an unverified destination are blocked. Qoder's alternate transport and QwenWork routes that differ from their displayed endpoint require follow-up work. Approval covers one recipient per execution host, ends when the approved model switches to a different recipient, and expires when Her restarts. Revocation requires a connection to the execution host. It also cancels the inference stream and prevents retries that still hold the previous approval. Requests already handed off and data already received remain with the recipient. A new daemon requires a sidecar that understands the consent flag.

## Coding agents

Her's mesh tools can start a session, send a follow-up, or answer a coding agent. The mesh link would forward the content to the target execution machine, which would launch or write to Codex, Claude Code, or an ACP adapter. That subprocess controls its account, cloud provider, overrides, and network traffic. A model name or agent display name does not establish the inference recipient.

Her's mesh link blocks those writes before forwarding to another host. The receiving daemon checks Her's signed grant at the coding session RPC entry points. The in-process scripted agents used by regression tests have no remote inference service. On the iPhone, the embedded owner service also blocks substantive direct replies to coding sessions with an unverified recipient. The fixed decline action can dismiss a question. Her's offline demo uses its existing in-process sample tasks.

## Other routes and remaining risk

- MCP services, browser and desktop control, shell commands, web search, and installed connectors have their own permission and network paths. They can transfer data independently of inference. The model disclosure covers connector results when Her includes them in model context.
- Provider catalog requests, key verification and discovery, and subscription sign-in are separate setup calls. They can exchange network metadata and credentials with their configured provider and identity service. Changing a custom endpoint address requires an explicit new key when the old address had a stored key. This change gates conversational inference, including against providers added through setup.
- A custom gateway may forward requests after receiving them. Hermesh verifies the immediate endpoint, and downstream processing depends on that operator.
- Earlier daemons and clients still have their earlier behavior when used together. A third-party coding process that was already started before upgrade can retain or transmit context already handed to it. Standalone coding from other clients and standalone `her` CLI use their own controls.
