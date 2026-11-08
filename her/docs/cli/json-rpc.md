# JSON-RPC

`her serve` speaks JSON-RPC 2.0, one JSON object per line. [examples/rpc](../../examples/rpc) has a complete client in Python and in Node, with no dependencies.

## Connecting

```bash
her serve --stdio
```

Or a Unix socket, which several clients can share and which only your user can reach:

```bash
her serve --listen unix://
```

No port is opened. Remote access goes through ssh, which brings its own login and encryption:

```bash
ssh host her serve --stdio
```

## A session

1. `initialize` with `{"protocol_version": 2, "client_info": {"name": "yours"}}`.
2. `session/new` with `{"cwd": "/abs/path"}` gives a `session_id` and a snapshot. Events follow as `session/event` notifications.
3. `session/op` with `{"session_id": …, "op": {"type": "prompt", "input": [{"type": "text", "text": "…"}]}}`.
4. Read events. `message_delta` carries the reply as it comes; `tool_started`, `tool_completed` the tools; `task_completed`, `task_failed` or `task_interrupted` the end; `idle` means nothing is left to do.
5. On `approval_requested`, answer with `session/op`: `{"type": "approval", "call_id": …, "decision": {"type": "approve"}}`. Other decisions: `approve_for_session`, `deny`, `abort`.
6. `session/close`.

Every event has a growing `seq`, and `session/attach` gives a snapshot to continue from. `$/cancel_request` cancels a request.

## Errors

Errors have a stable `data.kind`: go by that, not by the code or the words. `no_model` (-32046) means nothing is set up: send the person to `her login`, or use `models/list` and `auth/set`.

## Compatibility

Methods have tiers, in the schema's `x-stability`:

- **stable**: within a protocol version, things are only added: methods, optional fields, event types, error codes. **Ignore what you do not recognize.** Incompatible changes come with a new protocol version, and the old one stays for at least two minor releases. The events and the `her exec` result follow the same rule.
- **experimental**: may change with any version.

The full list is in [protocol-reference.md](protocol-reference.md). `her protocol schema` prints the JSON Schema of everything, from which you can generate types.
