#!/usr/bin/env python3
"""Drives her over its JSON-RPC protocol: start a session, send a task, answer its approvals,
print the reply. Needs nothing but Python and her.

    python3 minimal.py "fix the failing test"
    python3 minimal.py "fix the failing test" /path/to/her
"""

import json
import os
import subprocess
import sys

prompt = sys.argv[1] if len(sys.argv) > 1 else "say hello"
program = sys.argv[2] if len(sys.argv) > 2 else "her"

server = subprocess.Popen([program, "serve", "--stdio"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
next_id = 0


def send(message):
    server.stdin.write(json.dumps(message) + "\n")
    server.stdin.flush()


def call(method, params):
    """Sends a request. Replies can arrive with events in between, which are kept for later."""
    global next_id
    next_id += 1
    send({"jsonrpc": "2.0", "id": next_id, "method": method, "params": params})
    while True:
        message = json.loads(server.stdout.readline())
        if message.get("id") == next_id:
            if "error" in message:
                sys.exit(f"{method}: {message['error']['message']}")
            return message["result"]
        pending.append(message)


pending = []
call("initialize", {"protocol_version": 2, "client_info": {"name": "minimal.py"}})
session = call("session/new", {"cwd": os.getcwd()})["session_id"]
call("session/op", {"session_id": session, "op": {"type": "prompt", "input": [{"type": "text", "text": prompt}]}})

reply = []
while True:
    message = pending.pop(0) if pending else json.loads(server.stdout.readline())
    if message.get("method") != "session/event":
        continue
    event = message["params"]["event"]
    kind = event["type"]
    if kind == "message_delta" and event["delta"]["type"] == "text":
        reply.append(event["delta"]["text"])
    elif kind == "approval_requested":
        # A real client would ask someone. This one says yes to everything.
        print(f"approving: {event['summary']}", file=sys.stderr)
        answer = {"type": "approval", "call_id": event["call_id"], "decision": {"type": "approve"}}
        call("session/op", {"session_id": session, "op": answer})
    elif kind == "task_failed":
        sys.exit(event["error"]["message"])
    elif kind == "idle":
        break

print("".join(reply))
call("session/close", {"session_id": session})
server.stdin.close()
server.wait()
