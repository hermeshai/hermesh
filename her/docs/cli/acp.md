# Editors (ACP)

`her acp` serves the Agent Client Protocol on stdio. In Zed's settings:

```json
{
  "agent_servers": {
    "her": { "command": "her", "args": ["acp"] }
  }
}
```

An editor started from your desktop usually does not get your shell's variables; use the full path of `her` if it is not found, and `her login` rather than a key in the environment.

## Signing in

Without credentials, `session/new` answers `auth_required`, and the editor offers these:

| Method | What happens |
|---|---|
| Sign in from a terminal | The editor runs `her acp --login` in a terminal: the same as `her login` |
| Sign in with ChatGPT | Opens your browser |
| Use the credentials saved for her | After `her login` in any terminal |

A key cannot be typed into ACP's own flow; the terminal method handles that.

## What it does and does not

Sessions can be listed, loaded, resumed, closed and deleted. Model, thinking level and permission mode are configuration options. Images and embedded context reach the model. `/compact` and `/skill:name` are in the command list. Edits show as diffs, and approvals ask in the editor.

Files and commands run locally, not through the editor, so **the model does not see unsaved changes** in the editor.

Set defaults with `-c`: `her -c permission_mode=accept-edits acp`.

**State:** tested against the official ACP schema. Not yet tried in a real editor.
