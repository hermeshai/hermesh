# Her and third-party AI sharing

## iPhone first-use local informed consent

Before the first actual Her message is sent from iPhone (including sending a draft, retrying, or tapping a suggested answer), the iPhone app shows a localized informed consent prompt stored locally on the iPhone (`UserDefaults`, key `her-ai-consent`). The disclosure names the configured provider and model when loaded locally, explains that Her sends data through the user's computer to the AI model service configured there (including any custom provider or gateway), and states the categories of data (messages, chat history, images, attachment contents, memory, and tool results) and purpose (generating replies and performing Her's tasks, including scheduled tasks).

Declining or closing the prompt keeps the draft untouched and sends no data. Approving saves the local iPhone choice so subsequent sends proceed directly without re-prompting. Neither iPhone nor desktop exposes a user-facing button to revoke AI sharing.

## Removal of `--require-ai-consent` and backend provider verification

The `--require-ai-consent` startup flag, backend recipient/endpoint verification (`her-llm::consent`), and `ai_consent/*` RPCs (`ai_consent/status`, `ai_consent/allow`, `ai_consent/revoke`) have been completely removed from `her`, `hermeshd`, and desktop:

- `hermeshd` launches `her serve --listen unix://...` without `--require-ai-consent`.
- Custom model services (including LAN HTTP gateways, HTTPS endpoints, and loopback servers configured in `models.toml`) work without backend endpoint gating.
- Any legacy `~/.her/ai-consent.json` left by `v0.1.7` or `v0.1.8` is ignored and preserved untouched on disk (not destructively deleted).
- Transport security (HTTP redirects disabled in `her-llm`, system TLS verification, and end-to-end encrypted owner-to-machine mesh tunnels) remains unchanged.

## Coding agents and existing authorization rules

Direct Coding Agent sessions, iPhone/desktop answers, and Her's coding handoffs (`session_start`, `session_send`, `session_answer`) continue to use Hermesh's existing authorization model unchanged:

- **Owner device trust**: Paired trusted owner devices (Mac, Windows, Linux, iPhone) can view sessions and answer coding agent questions directly. Declining a question still rejects non-empty answer payloads.
- **Per-operation approval**: Her's permission mode (`manual`, `accept-edits`, `bypass-permissions`) governs tool calls; in `manual` mode, Her waits for explicit owner approval before invoking tools.
- **Signed mesh grants**: Her's access to machines, projects, and coding agents is enforced by the owner-signed mesh grant (and ends immediately when Her is turned off or its grant is revoked).

## Client and host upgrade compatibility

- **Current clients ↔ current host**: iPhone uses local first-use consent before sending; desktop sends directly; custom models (including LAN HTTP) work without backend consent gating.
- **Pre-#25 (`v0.1.6`) clients ↔ current host**: Fully compatible. Standard Her and coding RPCs work directly.
- **Current clients ↔ pre-#25 (`v0.1.6`) host**: Fully compatible. iPhone's local first-use consent does not depend on host `ai_consent/*` RPCs.
- **Installed `v0.1.7` / `v0.1.8` desktop ↔ current host**: Because the current host no longer advertises `ai_consent/status`, `v0.1.7`/`v0.1.8` desktop skips `ai_consent/*` and sends directly.
- **Current clients ↔ un-upgraded `v0.1.7` / `v0.1.8` host**: If the old host already has a saved grant for the active model, sending succeeds; if the old host refuses an unapproved prompt with `ai_consent_required`, the owner outbox still recognizes that refusal cleanly without retrying in a loop until the host is updated.
- **New `hermeshd` ↔ old `v0.1.7` / `v0.1.8` `her` binary**: `hermeshd` no longer passes `--require-ai-consent`, so an older `her` sidecar starts cleanly without enabling backend consent gating.
- **Installed `v0.1.7` / `v0.1.8` iPhone ↔ current host**: Earlier `v0.1.7`/`v0.1.8` iPhone builds explicitly require `ai_consent/status` on the host; updating the iPhone app switches to local first-use consent.
- **Installed `v0.1.7` clients ↔ current host (coding wire-compat)**: Legacy `AnswerLocalQuestion`, `CheckLocalCodingRecipient`, `CodingConsent`, and `Question.local_answer_available = true` remain wire-compatible while enforcing owner/grant/decline checks.

## Apple Guideline 5.1.2(i) scope and review notes

App Store review decisions rest solely with Apple; implementing iPhone's local first-use disclosure does not guarantee approval.

- **No in-app revocation UI**: Neither iPhone nor desktop provides a dedicated revoke button (see #45). Under Apple App Review Guidelines 5.1.2(i) and 5.1.1(ii), reviewers may still ask how users withdraw consent.
- **Desktop and direct coding agents**: Desktop Her, direct coding agent subprocesses (Claude Code, Codex, Gemini CLI, Antigravity, Cursor, GitHub Copilot, and other ACP adapters), MCP tools/connectors, provider metadata/discovery calls, and custom gateways operate outside iPhone's local first-use prompt.
