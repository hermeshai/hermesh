#!/usr/bin/env python3
"""Memory benchmark for long `her exec` tasks, without a real model.

A local mock of an OpenAI-compatible endpoint drives one task turn after turn: every turn it
asks her to `read` a file that changes each time, until the last turn answers
"LONG_TURN_COMPLETE". The full history is sent with every request, as with any chat API, so
the task's working set grows with the number of turns.

Scenarios:
  short  1 turn: memory of a minimal task
  long   1000 turns, each reading a ~1 KiB file
  big    100 turns, each reading a 60 KiB file cut to the read tool's output cap
         (10,000 tokens, ~40 KB; 50 KB before the cap was counted in tokens)

Reported per binary and scenario:
  peak RSS         highest resident set size, from the kernel's accounting (wait4)
  peak footprint   macOS only: lifetime maximum physical footprint, which is what Activity
                   Monitor shows and excludes clean or reusable pages
  turn 1 / last    RSS when the first and the last model request arrive
  history          size of the last request body, i.e. the history the task carries

    cargo build --release
    python3 tools/bench/bench_memory.py --her new=target/release/her --her old=/path/to/old/her

Uses only the Python standard library. Runs in a temporary HER_HOME.
"""

import argparse
import ctypes
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler
from http.server import ThreadingHTTPServer

SCENARIOS = {
    "short": {"steps": 1, "bytes": 1024, "lines": False},
    "long": {"steps": 1000, "bytes": 1024, "lines": False},
    "big": {"steps": 100, "bytes": 60 * 1024, "lines": True},
}
PROMPT = "Read the changing evidence file until the fixture finishes."
DONE = "LONG_TURN_COMPLETE"
USAGE = {"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}


def memory_sampler():
    """Returns sample(pid) -> {"rss", "footprint", "peak_footprint"} in bytes, or None."""
    if sys.platform == "darwin":
        libc = ctypes.CDLL("/usr/lib/libSystem.B.dylib")

        class RUsageInfoV4(ctypes.Structure):
            _fields_ = [("uuid", ctypes.c_uint8 * 16), ("fields", ctypes.c_uint64 * 40)]

        def sample(pid):
            info = RUsageInfoV4()
            if libc.proc_pid_rusage(pid, 4, ctypes.byref(info)) != 0:
                return None
            # ri_resident_size, ri_phys_footprint, ri_lifetime_max_phys_footprint
            return {"rss": info.fields[6], "footprint": info.fields[7], "peak_footprint": info.fields[28]}

        return sample

    def sample(pid):
        try:
            text = pathlib.Path(f"/proc/{pid}/status").read_text()
        except OSError:
            return None
        fields = dict(line.split(":", 1) for line in text.splitlines() if ":" in line)
        return {"rss": int(fields["VmRSS"].split()[0]) * 1024, "footprint": None, "peak_footprint": None}

    return sample


def sse(*chunks):
    out = "".join("data: " + json.dumps(c) + "\n\n" for c in chunks) + "data: [DONE]\n\n"
    return out.encode()


def chunk(delta, finish=None, usage=None):
    body = {
        "id": "r",
        "object": "chat.completion.chunk",
        "created": 0,
        "model": "bench",
        "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
    }
    if usage:
        body["usage"] = usage
    return body


class Fixture:
    def __init__(self, workspace, steps, size, lines):
        self.workspace, self.steps, self.size, self.lines = workspace, steps, size, lines
        self.count = 0
        self.pid = None
        self.turns = []
        self.sample = memory_sampler()

    def evidence(self, n):
        if self.lines:
            return f"{n}\n" + ("x" * 63 + "\n") * (self.size // 64)
        return f"{n} " + "x" * self.size + "\n"

    def respond(self, request_bytes):
        self.count += 1
        memory = self.sample(self.pid) if self.pid else None
        self.turns.append({"turn": self.count, "request_bytes": request_bytes, **(memory or {})})
        if self.count > self.steps:
            return sse(chunk({"role": "assistant", "content": DONE}), chunk({}, "stop", USAGE))
        (self.workspace / "evidence.txt").write_text(self.evidence(self.count))
        call = {"index": 0, "id": f"read_{self.count}", "type": "function", "function": {"name": "read", "arguments": ""}}
        arguments = json.dumps({"path": "evidence.txt"})
        return sse(
            chunk({"role": "assistant", "content": None, "tool_calls": [call]}),
            chunk({"tool_calls": [{"index": 0, "function": {"arguments": arguments}}]}),
            chunk({}, "tool_calls", USAGE),
        )


def serve(fixture):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_POST(self):
            remaining = int(self.headers.get("Content-Length", "0"))
            length = remaining
            while remaining:  # the body is discarded, like a real provider's would be
                remaining -= len(self.rfile.read(min(remaining, 1 << 20)))
            body = fixture.respond(length)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def run(her, scenario, tmp):
    spec = SCENARIOS[scenario]
    home, workspace = tmp / "home", tmp / "workspace"
    home.mkdir()
    workspace.mkdir()
    fixture = Fixture(workspace, spec["steps"], spec["bytes"], spec["lines"])
    server = serve(fixture)
    (home / "models.toml").write_text(
        f"""[providers.mock]
api = "openai-chat"
base_url = "http://127.0.0.1:{server.server_address[1]}"
auth = "none"

[[providers.mock.models]]
id = "bench"
context_window = 1000000
max_output_tokens = 8192
"""
    )
    env = {k: v for k, v in os.environ.items() if not k.endswith("_API_KEY") and not k.startswith("HER_")}
    env.update(HER_HOME=str(home), NO_COLOR="1")
    cmd = [her, "exec", "--output", "stream-json", "--permission-mode", "bypass-permissions", "-m", "mock/bench", PROMPT]
    started = time.monotonic()
    with open(tmp / "stdout.jsonl", "wb") as out, open(tmp / "stderr.log", "wb") as err:
        proc = subprocess.Popen(cmd, cwd=workspace, env=env, stdin=subprocess.DEVNULL, stdout=out, stderr=err)
        fixture.pid = proc.pid
        peaks = {"rss": 0, "footprint": 0}
        stop = threading.Event()

        def poll():
            while not stop.wait(0.02):
                memory = fixture.sample(proc.pid)
                if memory:
                    peaks["rss"] = max(peaks["rss"], memory["rss"])
                    peaks["footprint"] = max(peaks["footprint"], memory["peak_footprint"] or 0)

        threading.Thread(target=poll, daemon=True).start()
        _, status, rusage = os.wait4(proc.pid, 0)
        stop.set()
        proc.returncode = os.waitstatus_to_exitcode(status)
    elapsed = time.monotonic() - started
    server.shutdown()

    events = [json.loads(line) for line in (tmp / "stdout.jsonl").read_text().splitlines() if line.strip()]
    tools = sum(1 for e in events if e.get("type") == "tool_completed")
    finished = [e for e in events if e.get("type") == "task_completed"]
    completed = (
        proc.returncode == 0
        and tools == spec["steps"]
        and bool(finished)
        and finished[-1].get("last_message") == DONE
    )
    if not completed:
        sys.exit(f"{scenario}: task did not complete (exit {proc.returncode}, {tools} tool calls); see {tmp}")
    unit = 1 if sys.platform == "darwin" else 1024  # ru_maxrss is bytes on macOS, KiB on Linux
    peak_footprint = max([peaks["footprint"]] + [t.get("peak_footprint") or 0 for t in fixture.turns])
    return {
        "turns": fixture.count,
        "seconds": elapsed,
        "history": fixture.turns[-1]["request_bytes"],
        "rss_first": fixture.turns[0].get("rss"),
        "rss_last": fixture.turns[-1].get("rss"),
        "peak_rss": rusage.ru_maxrss * unit,
        "peak_footprint": peak_footprint or None,
    }


def mib(value):
    return f"{value / 1048576:8.1f}" if value else f"{'-':>8}"


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--her", action="append", required=True, metavar="LABEL=PATH", help="binary to measure (repeatable)")
    parser.add_argument("--scenario", action="append", choices=sorted(SCENARIOS), help="default: all")
    args = parser.parse_args()
    scenarios = args.scenario or ["short", "long", "big"]

    print(
        f"{'binary':<12} {'scenario':<8} {'turns':>5} {'secs':>6} {'history':>8} {'rss@1':>8} "
        f"{'rss@last':>8} {'peak rss':>8} {'peak fp':>8}   (MiB)"
    )
    for spec in args.her:
        label, sep, path = spec.partition("=")
        if not sep:
            label, path = pathlib.Path(spec).name, spec
        her = str(pathlib.Path(path).resolve())
        for scenario in scenarios:
            with tempfile.TemporaryDirectory(prefix="her-mem-") as tmp:
                r = run(her, scenario, pathlib.Path(tmp))
            print(
                f"{label:<12} {scenario:<8} {r['turns']:>5} {r['seconds']:>6.1f} {mib(r['history'])} "
                f"{mib(r['rss_first'])} {mib(r['rss_last'])} {mib(r['peak_rss'])} {mib(r['peak_footprint'])}"
            )


if __name__ == "__main__":
    main()
