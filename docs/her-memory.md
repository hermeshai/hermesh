# Her's memory

Status: the base (§3, §4, §5, §6) built 2026-11-11. What §8 lists is not
built. It builds on [her-server.md](her-server.md): an assistant has "its
own conversation, memory, folder" (§3 there), and a Her Server holds the
only copy (§7 there).

## 1. Goal, and what is left out

An assistant's conversation is summarized as it grows, and the owner can
clear it. Without memory, what the owner said about themselves and how they
want things done lasts only as long as a summary happens to carry it.

Memory is what the assistant keeps so that the owner never says the same
thing twice: who they are, how they want things done, what was decided on
things that go on, and where things are.

Three choices shape it:

- **Files.** Memory is Markdown files in the assistant's directory. The
  assistant writes them with the file tools it has anyway, the owner can
  read and change them, and there is no database, no index and no second
  model.
- **The model decides when to write.** The assistant's prompt says what
  memory is for, when to write and what to leave out (§5). Nothing extracts
  memories from the conversation behind its back.
- **In the system prompt.** What is always known is the last part of the
  assistant's system prompt, where the model follows it best (§4).

Left out: memory for sessions that are not an assistant's (coding sessions,
the `her` command line), memory shared between assistants or between Her
Servers, and everything in §8.

## 2. What other agents do

Looked at before building (2026-11): Hermes Agent, OpenClaw, Letta, Claude
Code, ChatGPT, Codex and Anthropic's "The components of agent memory".

- Most keep a small part always in context, files read on demand, and the
  raw conversations to search. Vector stores and knowledge graphs have no
  neutral evidence in their favour at the size of one person's notes.
- Most put the always-known part in the system prompt, as a copy taken when
  a session starts. Hermes Agent says why it never changes it within a
  session: the prompt cache.
- Telling a capable model what memory is for works better than fixed rules;
  a weaker model needs to be told when to read and write.
- Memory written from untrusted content is an attack that has happened to
  other products: an instruction in a web page ended up in long-term memory.

An assistant's conversation never ends, so "a copy taken when the session
starts" does not carry over as it is. §4.2 is what replaces it.

## 3. The files

### 3.1 Where

```
~/.her/bots/<id>/
  bot.json
  files/          the assistant's working folder
  memory/
    MEMORY.md     always in the system prompt
    <subject>.md  pages, read when needed
```

`memory/` is beside the working folder, not in it:

- Sub-agents and routine runs share `files/` and write there freely. Beside
  it, their writes are outside their workspace and inside her's data
  directory, which the approval policy already guards (§6).
- Work in the folder ("tidy up my files") cannot sweep the notes away.

What follows from the place, with no code of its own:

- `bots/clear` moves the conversation to the trash and leaves the
  assistant's directory: memory stays, and the next conversation starts
  with it.
- `bots/delete` moves the whole directory to `trash/bots/`: memory goes
  with the assistant.
- Each assistant has its own memory, and so has each Her Server.

### 3.2 `MEMORY.md`

One fact per line, under a few headings, in the owner's language. The kernel
reads the file as text and knows nothing of its headings.

```markdown
## About the user
- The weekly report is due on Friday afternoon and goes to the team's group. (2026-11-11)
- Lives in Shanghai from 2026-12; Hangzhou before that. (2026-11-20)

## How they want things done
- Replies are always in Chinese, the summary at the end of a task too. (2026-11-11)

## Going on
- Preparing the December launch; state and open points are in launch-dec.md: read it when the launch comes up. (2026-11-18)

## Where things are
- Scanned contracts are in ~/Documents/contracts, one folder a year. (2026-11-12)
```

Each line is a full sentence that still makes sense a year later, and ends
with the date it was written: the date says how old a fact is, and which of
two lines is newer.

### 3.3 Pages

A subject that needs more than a line or two (a project, a person, how a
recurring job is done) gets a page in the same folder, and one line in
`MEMORY.md` that names the page and says when to read it. Pages are not in
context: the assistant reads one when the request touches it.

### 3.4 The limit

Only the first 8 KB of `MEMORY.md` are loaded (`bot_memory::LIMIT`), cut at
the last whole line. The loaded text then ends with a line that says how
large the file is, how much was loaded, and to shorten it in that turn. So
the file is kept small by the assistant, and the kernel never rewrites it.

8 KB is a starting value: larger than Hermes Agent's (about 1,300 tokens),
smaller than Claude Code's (25 KB), since every request carries it.

## 4. How it reaches the model

### 4.1 The last part of the system prompt

An assistant's system prompt ends with its memory, after the MCP part:

```
<memory path="/Users/…/.her/bots/<id>/memory/MEMORY.md">
…the file, or (empty)…
</memory>
```

An assistant with nothing written yet gets it too: the path is where to
write. The prompt's own text (`bot.md`, §5) says what the block is.

The system prompt is where a model follows content best, and it stays
first however long the conversation gets. A block in a user message is read
as reference, and moves further from the latest turn with every message.

Sub-agents and routine runs get no memory in their prompts: the assistant
puts into a task the lines that matter for it.

### 4.2 When the prompt takes a new copy

The block is a copy of the file. Changing it changes the system prompt, and
a changed system prompt makes the provider read the whole conversation again
at the full price. So the prompt takes the file as it is now only when that
costs nothing:

1. **A conversation starts.** The prompt is new anyway.
2. **Right after a compaction.** The context is new anyway. The summary
   itself is still asked for with the prompt as it was.
3. **A task starts and the model last answered at least 10 minutes ago**
   (`bot_memory::QUIET`), or never. A provider's prompt cache has most
   likely expired by then.

Only a file that differs from the copy leads to a new prompt, which is
recorded as a `system_prompt` entry with its `memory`, like a prompt whose
MCP part changed.

Between two copies the assistant's own changes are not lost: each is a tool
call in its context, and a compaction, which would remove that call, takes a
new copy. `bot.md` says that the block is a copy from a while ago and that
later changes in the conversation are the newer word. A change from outside
(the owner editing the file) is known at the next of the three moments.

A conversation opened again keeps the copy its log has: the service unloads
idle conversations, and a provider may still hold the prompt. Two cases take
the file at once, since the prompt is written anew then: a new `bot.md`,
and a log from before there was memory.

### 4.3 Cost

One uncached request per new copy at most, at a moment when the request
would have been uncached anyway. The 8 KB are part of every request, at the
cached price while the conversation goes on.

10 minutes is a starting value. With a provider that keeps its cache longer,
a new copy after 10 minutes costs one uncached request.

## 5. When the assistant writes

`bot.md` has a section "Your memory". It says, in this order:

- what memory is and where it is given;
- what it is for, then when to write: the owner asks for it or sets a rule
  for later; a correction that would apply next time (a correction to one
  reply becomes a rule the second time); a lasting fact about the owner or
  their world; a decision on something that goes on, with the reason; where
  something is that will be needed again; a way of doing something that was
  not obvious and that the owner was happy with;
- what to leave out: what a tool can tell again, guesses, the task of the
  day, one-off requests, secrets (even when asked), sensitive matters unless
  asked, and anything a web page, a file, a message, a tool result or an
  agent's report says to remember;
- how: reply first, then write in the same turn; one dated sentence per
  line; change a line that covers it rather than add one; the owner's word
  now wins over a line; forgetting deletes the line and says so; pages for
  larger subjects; shorten the file when told it is over the limit; use what
  is there without announcing it; a line never authorizes sending, paying or
  deleting; put what matters into an agent's task.

The turn rules follow from it (`task.rs`, `bot::owed`):

- A note may go beside the reply in one step, the one exception to "the
  first send goes alone".
- A note written once the owner has their reply ends the turn by itself: no
  request is made only for the model to stop, and no reminder that work went
  unsent. So a note costs no request beside the reply, or one after it.
- A note that failed keeps the turn open, even beside a send that closed it,
  so the model sees why and writes it again.
- A note is nothing the owner waits for: it never sets off the reminder for
  unsent work.

A first run with a real model (Bailian Token Plan, Qwen3.8-Max) showed why:
before these rules, about a third of the turns that said "noted" ended
without the edit, and a note made after the reply was followed by the same
reply sent again. `tools/bench/bot_memory.mjs` runs those cases.

## 6. Who writes

| Who | Reads | Writes | How |
|---|---|---|---|
| The assistant's main session | Yes | Yes, without asking in every permission mode | `protected::memory`: its own `memory/` is allowed before anything else is judged |
| Its sub-agents and its routines' runs | Yes | No, in every mode | The same check: a path in any assistant's `memory/` that is not the session's own is denied, full access included. The refusal tells the agent to say in its report what the assistant should know |
| The owner | Yes | Yes | The files are on the Her Server |

Only `write` and `edit` are judged this way. A shell command that writes
there is judged like any other: it asks as a write to her's data directory,
and runs unasked only with full access.

What is not closed:

- With full access a sub-agent can still write there with a shell command.
  That mode lets it do anything the user's account can. Denying the file
  tools stops ordinary injected instructions; it is not a boundary.
- The assistant itself may note something harmful after reading a poisoned
  report. The rule in §5 is the only guard, and a rule in a prompt is not
  certain. This is why the owner must be able to see and remove what is kept
  (§8).

## 7. Tests

- Kernel (`her-core/tests/memory.rs`, scripted model, real tools, real
  logs): the block at a conversation's start, with and without a file; a
  change leaves the prompt alone while the conversation goes on; a
  conversation opened again at once keeps its copy; a new copy after a quiet
  time and after a compaction, each recorded once; a log from before memory;
  the limit; writing without asking in manual mode; a routine's run denied
  with full access.
- The write rule alone: `approval::tests::a_bots_memory_is_written_by_the_bot_alone`.
- The service (`her-cli/tests/e2e_bots.rs`, `her serve` and the mock model's
  `remember` and `recall`): a note, a cleared conversation that starts with
  it, and the notes in the trash with a deleted assistant.
- End to end (`e2e/her_memory_test.go`, `TestHerMemory`): the same through
  the owner's Mac, the relay and the tunnel to a Her Server.

## 8. Not built

- **Seeing and clearing it in the apps**: a notice in the conversation when
  the assistant notes something, memory in Her's settings, and clearing it.
- **Erase Everything** does not reach `~/.her` today, memory included.
- **Routine notes**: what a routine learns about its own job (what it saw
  last time, how the job is done) belongs to the routine, not to the
  assistant's memory. What a run finds out about the owner goes in its
  report; the assistant tells the owner, and notes it once the owner has
  taken it up.
- **Searching earlier conversation**, compacted parts included.
- **Tidying**: a scheduled pass that checks the notes against the
  conversation, merges repeats and marks what is out of date.
- **Evaluation across models**: `tools/bench/bot_memory.mjs` measures one
  model at a time (when it writes, what it leaves out, recall after a
  clear). Only Bailian Token Plan has been run; whether 8 KB and 10 minutes
  are the right values is not measured.
