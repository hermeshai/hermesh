# Troubleshooting

<!-- doc-test: run -->
```bash
her doctor
her doctor --json
```

`her doctor` checks configuration, credentials, directories and components, and lists warnings. `--online` makes one real request. `--strict` counts warnings as failures, for CI.

## Common messages

| Message | What to do |
|---|---|
| `no model is set up` | `her login` |
| `no credentials for provider/model` | `her login provider`, or set the provider's variable |
| `this session is open in another her or Hermesh` | One process at a time per session. Close the other, or start a new one |
| `the interactive mode needs a terminal` | In a script, use `her exec "PROMPT"` |
| `PDF support is not installed` | `her install pdf`. Workbooks do not need it |
| Exit code 3 | Something needed approval. Set `--permission-mode` or a rule |
| `unknown setting` as a warning | A newer version wrote it, or a typo: the warning gives the line |

Set `HER_LOG=debug` for details in `~/.her/logs`.

## Living beside Hermesh

The app and the CLI share `~/.her`: settings, sessions and keys. A key saved in one shows up in the other. `her exec` and conversations do not go through a service. A `her serve --listen unix://` shares the app's default socket, and the app replaces what runs there with its own when idle; to run your own, use `--listen unix:///your/path`.

## Upgrading

`her upgrade`. From Homebrew: `brew upgrade her`. A running `her serve` stays on its version until it restarts. An older her reading settings written by a newer one warns about the keys it does not know and goes on.
