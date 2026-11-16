# Replying to a message of Her's conversation (2026-11-16)

Status: accepted 2026-11-16 with the owner's decisions (§7), and built the
same day (§8). Prototype: [her-reply-demo.html](her-reply-demo.html) (it can
be used; the bar at its foot switches between the Mac and the iPhone, and
between today and the new design).

Scope: Her's conversation, on the Mac and the iPhone. Coding sessions
(Claude Code, Codex and the others) are not part of it (§9).

## 0. Summary

The owner can reply to any message of Her's conversation, or to words
selected in one. Once the reply is sent:

1. Its bubble carries a quote of what it replies to. A tap on the quote
   brings that message into view and marks it a moment.
2. The user message Her gets has one more text block after the owner's
   words. It says which earlier words the message replies to, and carries
   those words. Her needs no new tool, and the kernel `her` does not change.

One place writes the block: the core on the device (`hermeshd`, or the
iPhone's core) puts it together when it delivers the message. Two places
read it: `her-client` (the Mac) and `HermeshHer` (the iPhone) turn it back
into the quote.

## 1. Before

| | Mac | iPhone |
|---|---|---|
| What a message offered | Its time and Copy, on hover | Copy, in the menu of touch and hold |
| Selecting a part | Possible, for the system's Copy only | Not possible |
| Pointing at earlier words | Copy, paste and describe them again | Describe them from memory |
| Bringing a message into view, marked | Built, for a scheduled task's report (`rtn-flash`) | Built, for the same (`HerRow`'s `marked`) |

The longer the conversation, the more "the second option you gave" points
at the wrong thing, and Her can only guess.

## 2. What others do

- **Grok Bot:** selected text shows an "Add to chat" pill (Cmd/Ctrl+L on the
  desktop; touch and hold, Select Text, Add to Chat on the phone), which
  quotes the selection in the next message. Reply on a whole message sends
  one that carries a quote, and a click on the quote goes back to the
  message. Source: xAI's changelog and documentation.
- **Meta's Muse:** no documentation of it. What can be seen from outside is
  a "Replying to …" bar above the composer, and a quote in the bubble that
  goes back to the message. How Muse is told is not known.
- **Everyone:** a model's interface has no notion of a reply. Every product
  ends up putting the quoted words into this turn's user input. They differ
  in which layer does it, whether the owner sees the text, and whether the
  interface keeps the structure for itself.

Here the structure travels from the interface to the core, the core writes
a text block a person can read too, and the interface reads it back.

## 3. The interaction

### 3.1 Rules both share

- A message replies to at most one thing. Picking another while one is in
  the composer replaces it.
- A reply quotes a whole message, or the words selected in one. A selection
  counts when it lies within one message's own words. One that runs into
  another message, into a quote, or over a message's buttons offers no
  Reply.
- Her's messages and the owner's own can both be replied to. The composer
  says "Replying to Her" (the assistant's name) or "Quoting your own
  message".
- What can be replied to: Her's words, a message of files that has words, a
  question, a card that has words, and the owner's messages that have
  words. A message with no words, a session's card, a scheduled task's
  line, a failure, and a message still being sent or waiting on this device
  offer no Reply.
- A quote is at most 2000 characters. A longer one is cut, ends in "…", and
  counts as a part. The composer shows two lines of it (one on the iPhone),
  a bubble three; the rest is a tap away, at the message itself.
- A reply alone, with no words and nothing attached, is not sent, as an
  empty message is not.

### 3.2 The Mac

| Where | What happens |
|---|---|
| Hover over a message | Reply, before Copy |
| Select words in a message | Once the mouse is let go, a Reply pill shows over the selection. It goes when the selection does, on a scroll, and on a press elsewhere |
| The composer | What the next message replies to shows above the text: a bar in the accent colour, who is replied to, the words on two lines, and ×. The text takes the keyboard |
| Taking it away | ×, or Esc in the text |
| Sent | The bubble quotes it at its top. A click on the quote scrolls to the message and marks it (`rtn-flash`) |

No keyboard shortcut. Grok Bot's Cmd+L has no precedent here.

### 3.3 The iPhone

As the iPhone does things: the menu of touch and hold is the way in, and
everything can be reached from it. A gesture is a shorter way, never the
only one.

| Where | What happens |
|---|---|
| Touch and hold a message | The system's menu: **Reply**, **Copy**, **Select Text** |
| Select Text | A sheet rises from the foot of the screen, half its height, more when pulled. It holds the message's words, which are selected as anywhere on the iPhone. The edit menu of what is selected begins with **Reply**, then the system's own (Copy, Look Up, Translate) |
| Swipe a message left | The bubble follows the finger a little, an arrow shows where it was, and once it is far enough to reply on letting go, the arrow fills and a tap is felt. Let go there, it replies to the whole message |
| The composer | What the next message replies to shows above the words: a bar, who is replied to, one line of the words, and ×. The keyboard comes up |
| Sent | The bubble quotes it at its top. A tap on the quote scrolls to the message and marks it (`marked`) |

Why selecting happens on a sheet and not in the bubble:

- Her's bubbles are Markdown drawn in SwiftUI, whose text can be selected
  only whole on the iPhone, not by a range.
- Selecting in place in a scrolling list competes with the scroll and with
  touch and hold.
- The sheet holds the system's text view: the loupe, the handles, the edit
  menu and what VoiceOver does with them are the iPhone's own.

Accessibility: VoiceOver reaches Reply and Select Text through the same
menu; the bar above the composer and the quote in a bubble are each an
element, read as who was quoted and the words; text follows Dynamic Type.

The swipe goes left (the owner's decision): it does not meet the edge swipe
that goes back, which starts at the left edge and goes right. A drag that
begins up or down is the conversation's scroll and is left to it.

### 3.4 The quote in a bubble

At the top of the bubble, inside it (the owner's decision): a lighter panel
with a bar at its side, who was quoted, and three lines of the words. The
quote and the reply stay one thing while the message is queued, being sent,
or waiting on the device.

### 3.5 States

| When | What shows |
|---|---|
| Her is busy | The message waits its turn as any other, with its quote |
| Being sent, or waiting on the device | The bubble has its quote. The reply is kept with the message in the device's outbox, so a delivery tried again has it |
| Not sent, then Edit | The words go back into the composer, and what they replied to goes back above them |
| A task fails or is interrupted, and Her hands back what waited | The same. The block's text never shows in the composer |
| The message replied to is not among those read here (earlier history) | The quote shows its words. A tap says the message isn't here anymore |
| Another device's app is older | It shows the message's words followed by the block as text (§4.6) |

## 4. How it works

### 4.1 What Her is told

A user message is a list of content blocks (`Op::Prompt.input`,
`her/crates/her-protocol/src/op.rs`). What it replies to is a text block of
its own, right after the owner's words:

```
<resource uri="her-reply:call_8Kd2">
Replying to this earlier message of yours, quoted in part:
> the hotel by the lake, 25 minutes by car
</resource>
```

| Part | What it is |
|---|---|
| `<resource uri="her-reply:<ref>">` around it | How the kernel carries an embedded resource in a text block (`resource_text` in `her-core/src/commands.rs`): what such a block holds is not the owner's own words |
| `ref` | The quoted message's key in the conversation: the owner's message's `message_id`, or the id of Her's `send_to_user` call. Both apps give a message the same key (`botChat.ts`, `Chat.swift`). 1 to 128 of `A-Z a-z 0-9 _ : . -` |
| `of yours` / `of mine` | The quoted message is Her's, or the owner's own |
| `in part` / `in full` | A selection or a message that was cut, or a whole message |
| The words | Each line after `> `; an empty line is `>`. At most 2000 characters |

Why this shape:

- **The words themselves.** The model does not see message keys, and once
  the conversation is compacted or a message is sent again, the message
  replied to may not be in its context.
- **After the owner's words.** The kernel takes a `/command` only at the
  start of the first block (`resolve` in `commands.rs`). The block before
  the words would stop `/compact`.
- **As a resource.** The kernel looks for `/skill:name` in every text block
  of the input but the resources, and loads the skill. Without the wrapper,
  quoting words that mention `/skill:xxx` would load that skill, or warn
  that there is none. Today's `her` knows this wrapper: nothing in the
  kernel changes.
- **Words a person can read, not JSON.** The blocks beside it (the attached
  files, a web page's comments) are a line of English and JSON. This one is
  shown as text by an app that is not updated yet (§4.6), so it has to
  read. A model knows `>` quotes well.
- **A mark on every line.** Whatever the quoted words hold, each line
  begins with `> `: nothing in them can end the block early.
- **Added, not changed.** The block is part of a new user message only. The
  history is not rewritten, and the prompt cache keeps what it had.
- **`ref` is for the apps,** to find the message. The model needs none.

Her's system prompt is unchanged: the block says what it is.

### 4.2 Sending

Both apps send through the core on their device (`HerSend`,
[her-sending.md](her-sending.md)), so one place writes:

```
the composer (a reply and words) → HerSend (reply) → the outbox, HerWaiting (reply)
  → herDeliver: the words, the reply block, the pictures, the attached files → her's session/op
```

The protocol (`proto/hermesh/local/v1/local.proto`): `HerReply { ref, own,
text, partial }`, as `HerSendRequest.reply` and `HerWaiting.reply`; and
`GetStatusResponse.abilities`, which says `her_reply`.

The core (`internal/owner/her.go`):

- `HerSend` keeps the reply with the waiting message, so a message written
  while the machine is away, or tried again, goes with it.
- `herDeliver` adds `replyBlock(w.Reply)` after the words. It cuts at 2000
  characters and checks `ref`. A reply that names no message or quotes
  nothing adds no block, and the message goes as its words.

The kernel `her` is unchanged, but for one more case in its tests.

### 4.3 Reading it back

- **The Mac** (`packages/her-client/src/steps.ts`): `replyOf(text)` reads a
  text block that is such a block from its first character to its last.
  `userText()` gives it as `reply`, and keeps it out of `text`. `botChat`'s
  `user` item carries it.
- **The iPhone** (`packages/HermeshKit/Sources/HermeshHer/Chat.swift`):
  `herReply(_:)` reads the same. A row (`HerItem`) carries it as `reply`.

Only a block of its own that is one whole counts. The same sentence typed
by the owner is part of their words, in another block, and stays words.
Text that only looks like one shows as the text it is.

The three (Go writes; TypeScript and Swift read) run the same cases,
`internal/owner/testdata/her_reply.json`, as `/skill:` mentions do with
`mentions.json`.

### 4.4 The Mac

| File | What changed |
|---|---|
| `her/reply.tsx` (new) | `quoteOf` (a whole message or a selection, cut at 2000), `Replying` (what the conversation does for a reply), `ReplyButton`, `QuoteCard`, `ReplyPill` (the selection, and whether it is one message's words) |
| `her/Conversation.tsx` | Reply among a message's buttons; the draft has a `reply`; `deliver` hands it to `HerSend`; what a task hands back is split so the reply goes back above the words; bringing a row into view and marking it takes any key, not only a run's |
| `her/Composer.tsx` | `quote` and `onUnquote`: the bar, ×, Esc |
| `her/waiting.ts` | A message being sent or waiting carries its `reply` |
| `her/her.css`, `icons.tsx`, `locales/*/her.json` | The bar, the quote, the pill; the Reply icon; the words |

### 4.5 The iPhone

| File | What changed |
|---|---|
| `Views/Her/HerReplying.swift` (new) | A message's menu (`herMessageActions`), the swipe, the quote (`HerQuoteCard`), the sheet (`HerSelectText`, a `UITextView` whose `editMenuForTextIn` puts Reply first) |
| `Views/Her/HerConversation.swift` | A row says how its message is replied to; the owner's bubble quotes; a tap on a quote brings the message into view (the run's code, now shared); the sheet |
| `Views/Her/HerComposer.swift` | The bar above the words |
| `Views/Her/HerChat.swift` | `HerDraft.reply`; `deliver` hands it to `HerSend`; Edit and what a task hands back put it back; `revealMessage` |
| `HermeshHer` (`Chat.swift`, `Kept.swift`, `Session.swift`) | `HerReply`, `herReply`, `herQuote`, `herPlainText`, `herReturnedReply`; a row and an outgoing message carry the reply |
| `Localizable.xcstrings`, `Haptic.swift` | The words; the tap of a swipe that went far enough |

### 4.6 Different versions

As [version-skew.md](version-skew.md) asks: no protocol error on the
screen, and what is missing reads.

| When | What happens |
|---|---|
| The machine's `her` is older | Nothing: the kernel need not know the block |
| Another device's app is older | That message shows its words, then `<resource …>`, `Replying to this earlier message of yours …` and the quoted lines after `> `. Not pretty, but it reads, and it becomes a quote once the app is updated |
| The Mac app is updated and its own `hermeshd` has not restarted yet | An older core does not know `reply` and would drop it without a word. version-skew.md §6.1 left a list of `hermeshd`'s own abilities for the first feature that needs one: this is it. The core's status says `her_reply`. Without it, Reply stays where it is and says Hermesh is finishing an update, instead of sending a message that lost what it replies to. The iPhone's core is part of its app, and is never behind it |

### 4.7 Safety

- The quoted words were in this conversation already. Nothing from outside
  comes in with them.
- They may be words Her passed on from a web page or a mail. They reach her
  as a quote inside a **user message**, said to be what the message replies
  to, not as an instruction.
- `ref` is checked against its characters, every quoted line is marked, and
  the core is the only place that writes the block: what an app sends is
  never taken as structure.

## 5. Tests

| Layer | What |
|---|---|
| The core (Go, `internal/owner/her_test.go`) | `TestHerReplyBlock`: the shared cases, a long quote cut, line ends, a reply that names nothing. `TestHerSendReplies`: the order of the blocks (words, reply, attached files), a reply alone refused, and a message that waits for the machine goes with its reply |
| The kernel (Rust, `commands.rs`) | A `/skill:` mention quoted in the block loads nothing and warns of nothing |
| The Mac's reading (`her-client`) | The shared cases both ways; a quote, not words, in `userText`, in the conversation's steps and in `botChat`, sent or still queued |
| The iPhone's reading (`HermeshHerTests`) | The shared cases; rows, queued ones too; the outbox; `herQuote`, `herPlainText`, what a task hands back |
| End to end, the core (`e2e`, `TestHerReply`) | The real `hermeshd` and `her` with her-mock as the model: the status says `her_reply`, and the model's prompt has the owner's words, then the block |
| End to end, the Mac (`TestDesktopHerReply`, `apps/desktop/e2e/her-reply.spec.ts`) | The app against the real `her`: Reply from a message's buttons, Esc, the quote in the bubble, what Her was told; words selected with the mouse and the pill; a selection across two messages; the quote brings the message into view; a reply to the owner's own message |
| End to end, the iPhone (`TestIPhone` with `HERMESH_IPHONE_GREP=testHerReplying`) | The app in the Simulator against the real `her`: the menu, the bar and ×, the quote, what Her was told; Select Text and Reply in the edit menu; the swipe; the quote brings the message into view |

her-mock sends back the prompt it got (`[[mock:send]]`), so what the model
was told shows in Her's answer, in all three end-to-end tests.

Not tested here: how a real model answers a reply. `her-eval`'s cases are
browser tasks, and it has no place for a conversation yet.

## 6. What was built, in order

```
1. The core: the protocol, HerSend, herDeliver, the outbox, abilities  → Go unit tests, TestHerReply
2. Reading: her-client, HermeshHer, the shared cases                  → the three run the same cases
3. The Mac: Reply, the bar, the quote, bringing a message into view    → TestDesktopHerReply
4. The Mac: the pill over a selection                                  → TestDesktopHerReply
5. The iPhone: the menu, the bar, the quote, bringing into view        → testHerReplying
6. The iPhone: Select Text                                             → testHerReplying
7. The iPhone: the swipe                                               → testHerReplying
```

## 7. The owner's decisions

| Question | Decision |
|---|---|
| The quote: inside the bubble, or above it | Inside |
| One reply to a message, or several | One. Several make the interface and its words harder, and nobody asked |
| Can the owner's own messages be replied to | Yes |
| The iPhone's swipe | In this version, to the left |
| How much a quote holds | 2000 characters |
| This document | In English, as the others here |

## 8. Implementation record (2026-11-16)

Built as §4 to §6 say. What ran on the owner's Mac, all passing:

- `go test ./cmd/... ./internal/... ./pkg/...`, `go vet` for this Mac and
  for Windows, `gofmt`, `buf lint`.
- `pnpm -r typecheck`, and the tests of `her-client` (153), the desktop app
  (127) and `core` (80).
- `swift test` in `packages/HermeshKit` (51 in `HermeshHerTests`).
- `cargo test -p her-core --lib commands::`.
- `go test ./e2e -run 'TestHerReply$'`.
- `go test ./e2e -run 'TestDesktopHerReply$|TestDesktopHer$'`: the new
  story, and Her's long one, which is as it was.
- `HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerReplying go test ./e2e -run 'TestIPhone$'`,
  on an iPhone 17 Pro Simulator named with `HERMESH_IPHONE_SIMULATOR`.

Not run: the rest of `make e2e`, the iPhone's long stories (`testHer`,
`testTheIPhoneApp`), and anything on Windows or Linux.

What the work turned up:

- **A story of its own on the Mac.** The reply steps first went into Her's
  long story (`her.spec.ts`). The messages they add push the first ones out
  of what the conversation draws at its end, and a later step looks for
  them. They are a spec of their own, as Progress is.
- **No VoiceOver action on the iPhone.** An action of its own made a bubble
  one accessibility element, where its words are read block by block (and
  where `herChat` measures them). VoiceOver reaches Reply through the menu.
- **The swipe's arrow is there only during a swipe.** At rest it was an
  image named Reply in every bubble.
- **A last message's line** (`lastText` in `her-client`) took the block's
  first line for the owner's words when a message had none of its own. It
  leaves the block out.
- **TestIPhone's own Simulator can't be made on this Mac:** it picks the
  newest "iPhone N Pro" (18 here), and the installed runtime is iOS 26.0.
  Not changed here; `HERMESH_IPHONE_SIMULATOR` names one that works.

## 9. Not in this version

- Replies in coding sessions. A message there goes to a coding agent that
  is not ours, so text is the only way; the block of §4.1 would do, written
  by `newMessage` in `internal/machine/attach.go`, and the interaction
  would be much the same. After this one has been lived with.
- Her replying to one of the owner's messages (a reply parameter of
  `send_to_user`).
- Threads, reactions, forwarding.

## 10. What was not chosen

- **Only the message's id, for Her to find the words.** The model does not
  see ids, and after a compaction the words may be gone.
- **A Markdown quote in the composer's own text** (Grok Bot's selections).
  No protocol change, but nothing to go back to, no telling a quote the
  owner typed from a reply, and a leading `>` in the way of `/commands`.
- **A new kind of `UserContent`.** The cleanest, but it changes the kernel,
  the protocol and every model adapter, and an older `her` would refuse the
  message. The attached files are a text block today for the same reason,
  with a note to move once the protocol has a kind for them; this block
  moves with them.
- **The apps writing the block into the message's text, with no change to
  the core or the protocol.** The least code, and no core to be behind the
  app. Not chosen for two reasons: inside the words it is no block of its
  own, so a `/skill:name` in the quote would be taken as the owner's; and
  two apps writing it would drift. The core is already the one place that
  writes the attached files' block.
