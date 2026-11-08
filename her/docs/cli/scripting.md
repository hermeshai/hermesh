# Scripting

## One task

<!-- doc-test: run -->
```bash
her exec "say hello"
her exec --json "say hello"
her exec --output stream-json "say hello"
```

The reply goes to stdout and everything else to stderr. `--json` prints one object at the end; `--output stream-json` prints every event as a line.

<!-- doc-test: run -->
```bash
echo "say hello" | her exec -
her exec --tools read,bash "say hello"
her exec --no-save "say hello"
her exec --max-turns 3 "say hello"
her exec --resume last "and again"
```

Other options: `-m MODEL`, `--thinking LEVEL`, `--permission-mode MODE`, `--cwd DIR`, `--add-dir DIR`, `--image PATH`, `--output-last-message FILE`, `--ops-stdin` (answer approvals as JSON lines).

## The result object

```json
{
  "final_output": "…",
  "exit_code": 0,
  "error": null,
  "session_id": "…",
  "session_file": "…",
  "model": "…",
  "turns": 1,
  "compactions": 0,
  "duration_ms": 1200,
  "usage": { "input_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0, "output_tokens": 0, "cost": [] },
  "tool_calls": [],
  "refused": [],
  "agents": []
}
```

Failures under `--json` are JSON too, on stdout: `{"error": {"kind": "no_model", "message": "…", "help": "…"}, "exit_code": 2}`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | The task finished |
| 1 | The task failed |
| 2 | Bad usage or setup, no model or no credentials among them. No session was started |
| 3 | The task finished, but something it tried was refused because nobody could approve it. `refused` lists what |
| 130 | Interrupted |

Why 3: a pipeline that forgot to set a permission mode would otherwise show success while nothing changed.

## In CI

```yaml
- run: curl -fsSL https://github.com/hermeshai/her-releases/releases/latest/download/install.sh | sh
- run: ~/.local/bin/her exec --permission-mode accept-edits --json "fix the failing test" > result.json
  env:
    DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}
    HER_MODEL: deepseek/deepseek-flash
```

`bypass-permissions` belongs only in a container that exists for this run.

## Configuration

<!-- doc-test: run -->
```bash
her config
her config get model --show-origin
her -c thinking=low config get thinking
her config path
```

`-c key=value` sets any value for one command. Values come from, lowest first: `~/.her/config.toml`, the project's `.her/config.toml` when trusted, your own settings for the project (`--local`), `HER_MODEL` and `HER_PERMISSION_MODE`, then `-c`. A key her does not know is a warning with its line number, because a newer version may have written it; `her doctor --strict` counts warnings as failures.

## Environment variables

| Variable | What it does |
|---|---|
| `HER_MODEL`, `HER_PERMISSION_MODE` | The settings of those names, for a whole process tree |
| `HER_HOME` | Where her keeps its data (default `~/.her`) |
| `HER_LANG` | `zh` or `en`, else `LC_ALL`, `LC_MESSAGES`, `LANG` |
| `HER_CREDENTIAL_STORE` | `file` keeps keys in a 0600 file instead of the keychain |
| `HER_NO_BROWSER` | Do not open a browser for sign-ins; the address is printed |
| `HER_PDFIUM` | Where the PDFium library is |
| `HER_MODEL_CATALOG_URL` | Another address for the public model catalog |
| `HER_LOG` | Log filter, such as `debug` |
| `AI_AGENT`, `HER_CALL_ID` | Set for the commands a tool runs |
| `NO_COLOR`, `TERM=dumb` | No colors |
