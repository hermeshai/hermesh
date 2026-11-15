# Sending a message to Her: one way, by its id (2026-11-15)

How a message the owner writes reaches Her, on the iPhone and the Mac:
what went wrong, what replaces it, and how it is tested.

## 0. Summary

Until now a message was a call. The composer emptied, called, and put the
words back when the call threw; which way it went depended on whether the
conversation was open at that moment. A call that throws does not say the
message did not arrive, and nothing named the message, so a lost answer left
the owner with a bubble and the words still in the composer, and the core
could hand her the same message twice.

Now a message has an id, given when Send is tapped, and keeps it all the
way: in the outbox on the device, in her's queue, and as its `message_id` in
the conversation. Sending is one thing, whatever the connection:

1. The composer empties and the message shows at once, as sending.
2. The core keeps it in its outbox (HerSend), then delivers it, on the app's
   conversation when it is open, else on a connection of its own.
3. her takes a message by its id once: a delivery tried again is answered
   but not added.
4. The bubble says where the message is: sending, in the conversation,
   delivered when the machine is back, or not delivered with why.

Words go back into the composer only when they were certainly not sent:
when her hands back what was waiting at a task that failed or was
interrupted, or when the owner taps Edit on a message that was refused.

## 1. What went wrong

| What the owner saw | Why |
|---|---|
| A bubble, and the words still in the composer | `HerChat.send` put the draft back whenever the call threw (apps/ios/Hermesh/Views/Her/HerChat.swift), though her may have taken it: the answer lost when the connection reopened, or late. The Mac did the same (Conversation.tsx `Dock`). |
| The same message twice | The core (`herDeliver`, internal/owner/her.go) kept a message whose answer came late, and delivered it again. |
| Send greyed out after sending | `sending` held Send until the whole call ended: up to 20 s reaching the machine, 20 s for her, 120 s for each file. |
| The composer faded | `noModel`, while the machine's status said no model for a moment. |
| Slow, now and then | Two ways: on the open conversation (one call), or through the core (reach the machine, open a connection, `bots/open` with the last 100 items, then the call). The second was taken whenever the conversation's connection was reopening (backoff up to 30 s), after any change of the machine's `modelReady` (which reopened it), and for every message while one waited. |
| Words gone (iPhone) | her hands back what waited for a task that failed or was interrupted (`returned_input`); the Mac put it back in the composer, the iPhone dropped it. |
| Typing Chinese, the composer not emptied | Send read and emptied the field while pinyin was still being composed; the keyboard wrote it back. |
| A photo landing after the message went | Picked photos load in the background; Send did not wait for them. |
| A picture read differently by the way it went | On the conversation it went to the model as a picture; through the core, as a file the model was told the path of. |
| A waiting message hidden, or shown twice | Waiting messages were matched to the conversation by their words and a minute's slack. |

## 2. Principles

- **A message has an id from the moment it is sent.** Everything that shows
  or moves it uses that id.
- **One way.** The app always sends through the core; the core always keeps
  the message before delivering it.
- **Delivering twice is safe.** her takes a message by its id once, so the
  core may try again whenever it does not know.
- **The composer is the owner's.** Sending never greys it out; words come
  back into it only when they were certainly not sent.
- **The bubble tells the truth.** Its state is where the message is, not how
  the last call went.

## 3. her: a message by its id

`Op::Prompt` takes an `id` (her/crates/her-protocol/src/op.rs):

```json
{"type": "prompt", "id": "hw_01jcx…", "input": [{"type": "text", "text": "hi"}]}
```

- It becomes the input's id, and so the message's `message_id` once it joins
  the history, and the queued input's `id` while it waits (`queue_changed`).
- A prompt whose id the session knows already, in its history or waiting in
  its queues, is answered as taken and changes nothing: no event, no task.
- At most 64 characters of `A-Z a-z 0-9 _ -`; another is refused as
  invalid params (her-server `hub.rs`, before the op reaches the session).
- Left out, her gives the id as before. Front ends of her's own (the CLI,
  ACP, messages between agents) leave it out.

An older her ignores the field (serde drops unknown fields of a tagged
enum): the message gets her's own id and is taken again if delivered again,
as before. Apps fall back to matching by words for messages whose id is
her's (§5.3).

## 4. The core: an outbox

### 4.1 HerSend

`HerSendRequest` (proto/hermesh/local/v1/local.proto) gains:

- `id`: the message's id, given by the app (`hw_` and a ULID or UUID,
  lowercase). Left out by an older app, the core gives one.
- `connection_id`, `session_id`: the app's open conversation, when it has
  one, to deliver on (§4.3).

HerSend:

1. Checks the request (as before), and keeps the pasted pictures.
2. With the assistant's delivery lock held (one per machine and assistant):
   keeps the message in `her_waiting`, unless one with its id is kept
   already (a send tried again).
3. Delivers what that assistant has waiting, oldest first, this message
   among them (§4.2).
4. Answers `delivered`, or `waiting` with the message as kept (its `error`
   says why it waits), or an error.

An error means the message is not kept and was not taken: the request was
wrong, or her refused it while the machine answered (an assistant that is
not there, input her does not take). Everything after the message is kept
answers `waiting` or `delivered`.

### 4.2 Delivering

`flushWaiting` (every 30 s, and when woken) and HerSend deliver the same
way, an assistant at a time under its lock, so a message is never delivered
by two at once and an assistant's messages arrive in the order they were
written. Machines are delivered to side by side, so one that does not answer
holds no other back.

For each message, oldest first:

- The machine offline: it waits (`error` "offline"), and so do the rest.
- Delivered: dropped from the outbox, its pictures deleted.
- Lost on the way (a tunnel error, no answer in `herAnswerWithin`): it
  waits with why, and so do the rest; it is tried again later, by its id.
- Refused by her: the message HerSend was called for is dropped and the
  refusal answered; an older one waits with why, holding back the rest,
  until the owner discards it (as before).

### 4.3 On the app's connection

The app's conversation is open most of the time; delivering on it is one
call, as the fast way was. With `connection_id` and `session_id`, the core
sends `session/op` on that connection. If that fails other than on the way
(the connection is gone, her says it is not attached), it opens one of its
own: `bots/open` with `history_limit: 0`, so that no history comes back,
then `session/op`, then closes it.

### 4.4 What is delivered

One `session/op` prompt, with the message's id, in the content the apps'
composers gave it when they sent on the conversation themselves:

- its words, trimmed;
- its pictures as pictures (`image`, base64, the type read from the bytes),
  as the conversation shows and the model reads them;
- its files uploaded into the assistant's folder first, then named in a
  block of their own (her-client's `attachedBlock`: "Attached files:" and a
  path a line, with how to read them when one is a workbook).

A picture kept with the message that is not an image is uploaded as a file.
Messages the core delivered before named their files after the words
("Attached files:" and "- path" lines); the apps still read those.

## 5. The apps

### 5.1 The composer

- Send needs words, a photo or a file, and a model; nothing else. It never
  waits for a message before.
- What the keyboard is composing goes with the message, and nothing comes
  back into the field. On the iPhone the keyboard keeps what it composed and
  writes it back into a field emptied under it (seen in HermeshUITests: the
  message went, "ni hao" came back, and Send was greyed out under it, the
  field's value being empty); so with marked text, the field lets go of the
  keyboard first, which commits it, then the message goes and the field takes
  the keyboard again. Committing the marked text alone (`unmarkText`) was not
  enough. The Mac sends nothing on Enter while composing.
- Photos still loading show in the composer, and Send waits for them.
- Words her hands back (`returned_input` of `task_failed`,
  `task_interrupted`) go back into the composer, before what is there, once
  for each task (the iPhone now does as the Mac did).

### 5.2 A message's states

| State | Shown | From |
|---|---|---|
| Sending | faded bubble, "Sending…" | the app, while HerSend has not answered |
| In the conversation | a bubble (faded while queued for the task under way) | her's conversation, by its id |
| Waiting | faded bubble, "Delivered when … is back" or "Not delivered: why", Discard | the outbox (ListHerWaiting) |
| Not sent | bubble, "Not delivered: why", Try Again, Edit | the app, after HerSend refused it |

A message keeps its id, and its row its key, from one state to the next, so
the bubble stays where it is and only its caption changes.

- HerSend answered `delivered`: the bubble stays as sending until the
  conversation shows the id (its event may come after the answer), or its
  words for an older her (§5.3); from then on it is the conversation's,
  even when her hands it back. It goes too when the conversation will not
  show it: five seconds after the answer with the conversation open (a
  command such as `/context`, which her takes without it joining the
  conversation), or when the conversation opens afresh without it. Before
  2026-11-16 such a message said "Sending…" until the app was closed.
- Answered `waiting`: the outbox list is read again, and the row is the
  outbox's.
- Answered an error: the outbox list is read again; if it holds the id (the
  answer was lost, not the message), the row is the outbox's, else the
  message is not sent. Try Again sends it again with the same id; Edit puts
  it back into the composer.

### 5.3 Matching

A row of the outbox or the app is hidden when the conversation (its history
or its queue) has a message with its id. A message her gave its own id (an
older her, §3) is matched as before, by its words, sent at or after the
message was written less a minute; a message with an `hw_` id is only ever
matched by its id.

### 5.4 The connection

- The conversation's connection opens again when the assistant, its
  conversation or whether her runs changes, and when a model is connected
  while her refused it for having none; not when `modelReady` merely changes.
- While the machine answers but the conversation's connection is opening
  again, a line under the top says so ("Connecting to …"), after a moment,
  so a slow reply is not a mystery.

## 6. Older peers

| Peer | What happens |
|---|---|
| An older her | ignores `id`: a delivery tried again may be taken twice, as before; apps match by words. |
| An older app, this core | sends without `id`: the core gives one; the app shows the outbox as before. |
| This app, an older core | not possible: on the iPhone the core is the app's own; the Mac's app and hermeshd update together. |

## 7. Tests

- her (Rust, her-core and her-server): a prompt with an id joins the history
  under it; the same id again, waiting or in the history, changes nothing;
  a wrong id is refused.
- Core (Go, internal/owner): HerSend keeps before delivering and drops what
  her took; passes the id; the same id twice keeps one; delivers on the app's
  connection, and on its own when that is gone (without history); delivers
  older waiting messages first; pictures as images; a refusal is answered
  and not kept; HerSend and flushWaiting never deliver one message at once.
- HermeshKit (Swift) and her-client/desktop (TS): matching by id, by words
  for her's ids; what is handed back; the states.
- End to end, with the real her, relay and core:
  - iPhone (HermeshUITests via TestIPhone): after Send the composer is empty
    and the bubble shows once; sending while pinyin is being composed; with
    the conversation's connection gone, a message still goes and shows
    once.
  - Mac (apps/desktop/e2e via TestDesktopHer): the same, and a message
    written while the machine is offline waits and is delivered once when it
    is back.

## 8. Implementation record (2026-11-15)

- her: `Op::Prompt.id` (her-protocol `op.rs`, `valid_input_id`), checked in
  her-server's hub; her-core keeps the ids it took since the session loaded
  (`SessionState::taken_inputs`) besides those in its history, because a
  task's first input joins the history only once its task has started, and
  a copy arriving meanwhile would otherwise be taken again. Schema and the
  TypeScript types regenerated.
- Core: `HerSend` keeps first (`herKeptOne`, `addHerWaiting`), then
  `flushBot` under `herBotLock`; `flushWaiting` runs machines side by side.
  `herDeliver` builds the apps' content (`attachedBlock`, `herPicture`) and
  tries the app's connection first; `onBot` asks for `history_limit: 0`.
- iPhone: `HerChat.send`/`retry`/`edit`/`deliver`, `sending` and
  `reconnecting`; `HerComposer` lets go of the keyboard while it composes
  (`UIResponder.herFirst`) and waits for photos; rows by `herOutbox` (HermeshKit `Kept.swift`), a
  `.sending` row, `HerConnectingLine`; what a task hands back
  (`herReturned`) goes into the composer.
- Mac: the outbox store, `SendingBubble` and `ConnectingLine` in
  `Conversation.tsx`, `outboxFor` (`waiting.ts`); `openCallTransport` hands
  `onOpened` the connection's id, which `useConversation` keeps, and
  `modelReady` alone no longer reopens the conversation.
- The apps' own `toInput` and the iPhone's `attachedBlock` went with the way
  that used them; her-client keeps `attachedBlock`, the format's definition.
