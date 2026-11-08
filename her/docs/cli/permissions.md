# Permissions

**her has no operating-system sandbox.** A command it runs can do what you can do. What stands in the way is below; decide how much you need.

## Permission modes

| Mode | What runs without asking |
|---|---|
| `manual` (default) | Reading, and commands known to only read |
| `accept-edits` | Also file changes and simple file commands inside the workspace |
| `bypass-permissions` | Everything, protected paths included. Only in a container or a throwaway machine |

Deny and ask rules apply in every mode.

Set it for one run with `--permission-mode`, inside a conversation with `/permissions`, for good with `her config set permission_mode accept-edits`. Nothing else saves it: run flags are for that run.

## Rules

```toml
[[rules]]
prefix = ["git", "push"]
decision = "ask"
reason = "Pushing is shared"

[[rules]]
prefix = ["cargo", "test"]
decision = "allow"

[[rules]]
prefix = ["mcp__mail__send"]
decision = "deny"
```

`prefix` matches a command's first words, or the full name of an MCP tool. `decision` is `allow`, `ask` or `deny`. In a conversation, answering `a` keeps a rule for the session.

## The workspace and protected paths

File changes outside the project's folder ask, whatever the mode but `bypass-permissions`; `--add-dir` adds folders. `.git`, shell profiles and her's own settings are protected: changing them asks even in `accept-edits`.

## Trusting a project

A project's `.mcp.json`, and the settings in its `.her/config.toml` that decide what runs, are ignored until you trust the project. In a conversation her asks once, showing what the servers would run. `her trust` trusts the folder; `her trust --remove` takes that back.

`AGENTS.md` and a project's Skills are always read. A repository can use them to try to influence the model, so read what you clone.

## When nobody can approve

`her exec` without a terminal cannot ask, so what would ask is refused, and the task exits 3 once it has finished. Set a mode or a rule. See [scripting](scripting.md).

## An agent running her

`her config set` and `unset` refuse settings about permissions while `AI_AGENT` is set, and say to run them in a terminal. This is one more step, not a boundary.
