# Tools

<!-- doc-test: run -->
```bash
her tools
her tools bash
her tools --json
```

`her tools` shows what a session here would have and what the rest would need. `her tools NAME` prints a tool's description and the JSON Schema of its parameters. `--json` is the same as the protocol's `tools/list`.

| Group | Tools | Needs |
|---|---|---|
| base | `read`, `write`, `edit`, `bash` | Nothing; `tools.enabled` or `--tools` can take some away |
| agents | `spawn_agent`, `send_message`, `list_agents`, `stop_agents` | `agents.enabled` |
| web | `web_search` | Nothing: without a key, searches go to a service with a free allowance ([privacy](privacy.md)) |
| doc | `doc_open`, `doc_read`, `doc_find`, `doc_view`, `doc_edit`, `doc_save`, `sheet_calc`, `sheet_trace` | Workbooks need nothing; PDFs need the `pdf` component |
| browser | `browser_navigate`, `browser_snapshot`, `browser_act` and others | `browser.enabled = true`, and Chrome, Edge or Chromium, or `her install browser` |
| cua | `cua_apps`, `cua_snapshot`, `cua_act` and others | macOS, `desktop.enabled = true`, `her install computer`, and system permissions for your terminal app |

Only `read`, `write`, `edit`, `bash` and the agent tools sit in the model's tool list; the rest are found through `tool_search` and run through `call_tool`, so that they cost nothing until used.

## Components

What most people do not need stays out of the program:

```bash
her install pdf
her install browser
her install computer
her uninstall pdf
```

Downloads are checked against a signed manifest. `her doctor` lists what is there. Without PDF support, the model hears that and can tell you or read the file another way. On Linux, PDFs need glibc; Alpine does not have it.

## MCP servers

```bash
her mcp add docs --url https://example.com/mcp
her mcp add files -- npx some-mcp-server
```

`her mcp` lists them, `get` shows one with its tools, `list --check` connects to each. `enable`, `disable`, `remove`, `login` and `logout` do what they say. `--global` (the default), `--local` and `--project` say which file.

**Approvals.** A server's tools ask before running in `manual` and `accept-edits`, unless the tool says it only reads and the server is your own; a server a project brings is not taken at its word. To trust a server completely, write this in `~/.her/config.toml` (it counts nowhere else):

```toml
[mcp_servers.docs]
approval = "never"
```

## Skills

A Skill is a folder with instructions and scripts the model reads when it needs them. `her skills` lists them; `add`, `remove`, `enable` and `disable` change them. Type `/skill:name` in a conversation to use one.

## Web search

<!-- doc-test: run -->
```bash
her web-search status
```

`her web-search set-key tavily` takes a key from standard input and tries it. `her web-search check` makes a real search.
