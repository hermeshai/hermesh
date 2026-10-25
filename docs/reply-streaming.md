# Hermesh reply streaming: show an agent's reply as it is written

Status: proposed, 2026-10-25. For review before any code.

The owner watches a coding agent answer. Today the reply shows up all at
once when the agent finishes it, sometimes after a minute of nothing but
"Gemini CLI is working". Chat apps show text as it is written, and owners
expect it. This document adds that to every agent, both owner apps and both
paths (the owner's own Mac and remote machines), without storing more and
without the relay learning more.

## 1. What happens today

```
agent CLI ──chunks──► provider adapter ──whole message──► Sessions.emit ──► writer (SQLite) ──► deliver ──► StreamEvents subscribers
                       (buffers or waits)                  (seq, persisted)                      (after commit)
```

| Provider | What the agent sends | What we do with it |
|---|---|---|
| ACP ([acp.go](../internal/provider/acp/acp.go)) | `agent_message_chunk` as it writes | Buffered in `s.msg`; `flush()` emits one `EventAgentMessage` at the next tool call or at the end of the turn |
| Claude Code ([claude.go](../internal/provider/claude/claude.go)) | Whole `assistant` messages (stream-json without `--include-partial-messages`) | One `EventAgentMessage` per text block |
| Codex ([codex.go](../internal/provider/codex/codex.go)) | `item/agentMessage/delta`, then `item/completed` | Deltas skipped unparsed (`skipNotifications`, they only mark progress); one `EventAgentMessage` on completion |

[`Sessions.emit`](../internal/machine/sessions.go) gives every event a seq
and queues it for the writer. Subscribers get it once it is committed, in
seq order. The Mac app ([events.ts](../apps/desktop/src/renderer/src/session/events.ts))
and the iPhone app ([Service.swift](../apps/ios/Hermesh/App/Service.swift))
both read `LocalService.StreamSessionEvents` with `follow`, and resume by
`after_seq`. For the owner's own Mac, [`Owner.StreamSessionEvents`](../internal/owner/owner.go)
reads the machine in process. For a remote machine it calls
`SessionService.StreamEvents` through the relay tunnel, whose TLS runs end
to end: the relay copies bytes it cannot read.

Bots read the same events through the CLI and the MCP gateway.

## 2. Decision

**A draft travels next to the stored events but is never stored. The final
message replaces it.**

1. Each provider reports partial reply text as `EventAgentDraft{Text}`,
   pieces in order.
2. hermeshd keeps the draft of each live session in memory, and sends its
   new text to the subscribers that ask for drafts at most every 100 ms. The
   pieces go through the writer's queue without being written, so they
   reach subscribers in order with the stored events around them.
3. The stored `AgentMessage` stays exactly as today. Any stored event ends
   the draft on the screen: the provider always stores the finished text
   before the next tool call, question or end of the turn.
4. If a turn ends with a draft that was never finished (interrupted,
   crashed, stalled), hermeshd stores the draft as an `AgentMessage` first,
   so what the owner saw stays in the conversation.
5. Drafts are opt-in on the stream (`partial = true`). Bots, the CLI and the
   MCP gateway don't ask, and see exactly what they see today.

Nothing new is stored, the session list does not change while a draft
grows, and the relay carries more bytes of the same end-to-end stream.

## 3. Protocol (session.proto, local.proto)

```proto
message SessionEvent {
  ...
  oneof kind {
    ...
    // Reply text the agent is still writing. Never stored, and seq is 0:
    // it is not a position to resume from. Sent only to streams that ask
    // (StreamEventsRequest.partial). Any stored event that follows ends the
    // draft; its text is then in that event or was dropped by the agent.
    AgentDraft agent_draft = 20;
  }
}

message AgentDraft {
  // Text to append to the draft.
  string text = 1;
  // Set on the first draft a stream sends: text is the whole draft so far
  // (a stream that starts, or resumes, mid-reply).
  bool reset = 2;
}

message StreamEventsRequest {
  ...
  // Also send AgentDraft events while the agent writes.
  bool partial = 5;
}

// local.proto: the same flag, passed through to the machine.
message StreamSessionEventsRequest { ...; bool partial = 5; }
```

Field 4 of both requests is left for `view` ([state-sync.md](state-sync.md) §3.1, §3.4).

- **Appends, not snapshots.** A reply of 20 KB re-sent ten times a second
  would cost about 1 MB per reply on a phone. Appends are safe because one
  stream is ordered: if a subscriber falls behind, its stream is closed as
  today, and the new stream starts with a `reset` draft.
- **seq 0.** A client never moves `after_seq` on a draft. Clients that don't
  set `partial` never get one, so older iPhone builds and bots are safe.

## 4. hermeshd

- **`provider.EventAgentDraft`** joins `provider.Event` (field `Text`).
- **`live.draft`** (a `strings.Builder`) and `live.draftSent` (how much of it
  each subscriber has, kept with the subscriber's channel).
- **Pacing.** A draft piece marks the session dirty. A 100 ms tick, the same
  pattern as `Sessions.Watch` ([sessionlist.go](../internal/machine/sessionlist.go)),
  queues one `writeOp{l, draft}` with the text added since the last tick.
  The writer skips persistence for it and hands it to `deliver`, which
  sends it only to subscribers with `partial`.
- **Ending a draft.** `EventAgentMessage` clears `live.draft`. Before
  `TurnCompleted`, `Error` or a crash status, a non-empty draft is emitted
  as an `AgentMessage` (persisted) and cleared.
- **Subscribing mid-reply.** `Stream` sends the stored events up to
  `persistedSeq`, then, with `partial`, one `reset` draft carrying
  `live.draft`, then follows.
- **Progress.** Draft pieces count as activity for the stall watchdog (Codex
  already counts its deltas). `LastMessageExcerpt` keeps following stored
  messages only, so the session list does not churn.

## 5. Providers

| Provider | Change |
|---|---|
| ACP | In `agent_message_chunk`, also emit `EventAgentDraft{c.Text}`. `flush()` is unchanged. |
| Claude Code | Add `--include-partial-messages`. On `stream_event` with `content_block_delta` / `text_delta`, emit `EventAgentDraft`; ignore events with `parent_tool_use_id` (subagents). The `assistant` message stays the final text. Check the flag against the oldest Claude Code we support. |
| Codex | Parse `item/agentMessage/delta` (`params.delta`) instead of skipping it; keep skipping command output and reasoning deltas. `item/completed` stays the final text. |

The fake ACP agent gets `stream <text>`: the text in small chunks with
pauses (`\n` for newlines, as `say`), so tests can watch a draft grow.

## 6. Mac app

- **Events.** [events.ts](../apps/desktop/src/renderer/src/session/events.ts)
  asks with `partial: true` and keeps the draft apart from the events: a
  `reset` replaces it, other drafts append, any stored event clears it. Draft
  changes ride the existing 30 ms batch, so one render per batch at most.
- **Rendering.** The draft shows after the last row as a growing agent
  message, with the same type and colour as a finished one. The
  conversation follows it while the owner is at the end (the scroll effect
  also depends on the draft's length). No per-word fade-in, no cursor: the
  quiet style the app keeps.
- **Unfinished Markdown.** react-markdown parses a growing text correctly at
  every step but shows its unfinished parts raw: `**bol`, `[link](htt`, a
  table header before its `|---|` row. Two small layers fix this:
  1. **Blocks.** Split the draft into blocks (blank lines outside code
     fences). Finished blocks render through the memoised `Markdown`, so a
     tick re-parses only the last block, not the whole reply.
  2. **The last block.** Close what is open with
     [`remend`](https://www.npmjs.com/package/remend) (Vercel's "self-healing
     markdown", the piece Streamdown uses for this): dangling `**`, `*`,
     `` ` ``, `~~` and links. Hold back a block that starts as a table until
     its delimiter row arrives, so it appears as a table once and doesn't jump
     from text. An open code fence already renders as code, so nothing to do
     there.

  The stored message, when it arrives, renders through the same `Markdown`,
  so it looks the same and nothing jumps.

## 7. iPhone app

It reads the same `StreamSessionEvents` in process
([internal/mobile](../internal/mobile/mobile.go)), so it only has to set
`partial`, keep the draft as the Mac does, and show it as the last agent
message. It renders agent text inline-only (`AttributedString(markdown:)`),
so unfinished Markdown is a smaller problem there: no tables or fences. No
held-back blocks on the phone for now.

## 8. Performance and data

- **Renderer.** At most 10 draft updates a second, each re-parsing one block:
  within the "live ≤ 5 % busy" budget of [desktop-performance.md](desktop-performance.md).
  `pnpm perf` gets a draft-growing scenario.
- **Daemon.** No SQLite writes for drafts. The writer's queue carries about
  10 extra ops a second per writing session.
- **Network.** Appended text only. Over the relay, about the size of the
  reply, once.
- **Center.** Nothing new: the relay sees encrypted bytes, as today.

## 9. Tests

- **Go, provider level.** Each adapter emits drafts that add up to the final
  message: ACP with the fake agent; Claude and Codex with recorded stdout
  fixtures.
- **Go, daemon.** A subscriber with `partial` gets drafts in order, between
  the right stored events; one without gets none. One that joins mid-reply
  gets a `reset` draft. An interrupted turn stores its draft. Stored events
  are byte-identical with and without drafts.
- **Go e2e, remote.** A client device reads a remote machine through the
  relay and sees the draft grow before the final message.
- **Desktop e2e** (`session.spec.ts`). A session runs `stream` with a long
  reply that has a table and a code fence. The page shows part of the reply
  while the session runs, never a raw `|---|` or `**`, follows it to the
  end, and ends with the same text as the stored message. Interrupt mid-reply
  keeps what was shown.
- **iPhone UI test.** The same session, checked for a partial reply.

## 10. Options considered

| Option | Why not |
|---|---|
| Store each chunk as an event | Thousands of rows per reply, the session list churns on every chunk, and bots and the MCP gateway would read chunks they don't want |
| Send the whole draft each tick (snapshots) | Simplest client, but quadratic bytes: too much for the phone on long replies |
| A separate `StreamDrafts` RPC | A second stream to keep in step with the first; ordering against stored events would have to be rebuilt on the client |
| Streamdown instead of react-markdown | Brings rehype-raw (HTML, then sanitised; we render no HTML at all), Tailwind class merging, Shiki and Mermaid. We only need its unfinished-Markdown piece, `remend`, which ships on its own |
| Fade words in as they arrive | Motion on every token; the owner reads, doesn't watch an animation. The quiet default |

## 11. Not doing (now)

- Streaming the agent's thinking (reasoning deltas), or command output as it
  runs: same mechanism later, if wanted.
- Drafts in the session list excerpt or in notifications.
- Streaming the user's own messages (they arrive whole).

## 12. Open questions for review

1. 100 ms pacing: smooth enough on the Mac? The phone could take 250 ms.
2. Should an interrupted reply show as cut short (a quiet mark after it), or
   look like any other message?
3. Ship provider by provider (ACP and Codex first, Claude Code after
   checking `--include-partial-messages` across versions), or all at once?
