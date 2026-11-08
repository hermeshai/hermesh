#!/usr/bin/env python3
"""Fresh ACP/native server -> initialize -> new session -> real model HTTP request.

Each sample starts a new process, finishes a validated prompt, then creates another
session on the same connection to measure reuse separately. Normal logging, skills,
configuration and persistence stay enabled. No resident process is used for startup.
Socket retries sleep 100 us (actual scheduling delay may be larger); this observer
cost is included, never subtracted. Use --launcher on Linux for a pre-fork clock.
Only Python's standard library is required; bench_startup.py must be alongside it.
"""

import argparse
import hashlib
import json
import math
import os
import pathlib
import platform
import select
import socket
import statistics
import subprocess
import tempfile
import time

from bench_startup import Mock, prepare, run_once, stats


class Peer:
    def __init__(self, reader, writer, timeout):
        self.reader, self.writer = reader, writer
        self.timeout = timeout
        self.pending = b""
        self.sequence = 0

    def send(self, method, params):
        self.sequence += 1
        data = json.dumps({"jsonrpc": "2.0", "id": self.sequence,
                           "method": method, "params": params}).encode() + b"\n"
        view = memoryview(data)
        while view:
            n = os.write(self.writer, view)
            view = view[n:]
        return self.sequence

    def read(self, deadline):
        while b"\n" not in self.pending:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([self.reader], [], [], remaining)[0]:
                raise TimeoutError("protocol response deadline")
            chunk = os.read(self.reader, 65536)
            if not chunk:
                raise RuntimeError("protocol stream closed before completion")
            self.pending += chunk
        line, _, self.pending = self.pending.partition(b"\n")
        msg = json.loads(line)
        if "method" in msg and "id" in msg:
            raise RuntimeError(f"unexpected client request: {msg}")
        return msg

    def reply(self, request, notification=lambda _: None):
        deadline = time.monotonic() + self.timeout
        while True:
            msg = self.read(deadline)
            if "method" in msg:
                notification(msg)
            elif msg.get("id") == request:
                if "error" in msg:
                    raise RuntimeError(f"RPC error: {msg}")
                return msg["result"]
            else:
                raise RuntimeError(f"unexpected response: {msg}")

    def rpc(self, method, params):
        return self.reply(self.send(method, params))


def task(peer, protocol, cwd, mock):
    before = len(mock.arrivals)
    start = time.perf_counter_ns()
    params = {"cwd": str(cwd)}
    if protocol == "acp":
        params["mcpServers"] = []
    created = peer.rpc("session/new", params)
    ready = time.perf_counter_ns()
    sid = created["sessionId" if protocol == "acp" else "session_id"]
    prompt_at = time.perf_counter_ns()
    if protocol == "acp":
        text = []

        def notification(msg):
            update = msg.get("params", {}).get("update", {})
            if update.get("sessionUpdate") == "agent_message_chunk":
                text.append(update["content"].get("text", ""))

        result = peer.reply(peer.send("session/prompt", {
            "sessionId": sid, "prompt": [{"type": "text", "text": "hello"}]
        }), notification)
        if result.get("stopReason") != "end_turn" or "".join(text) != "ok":
            raise RuntimeError(f"incorrect ACP result: {result}, {text}")
    else:
        ended = []

        def notification(msg):
            event = msg.get("params", {}).get("event", {})
            if event.get("type") in ("task_completed", "task_failed", "task_interrupted"):
                ended.append(event)

        peer.reply(peer.send("session/op", {
            "session_id": sid, "op": {"type": "prompt", "input": [{"type": "text", "text": "hello"}]}
        }), notification)
        deadline = time.monotonic() + peer.timeout
        while not ended:
            notification(peer.read(deadline))
        if ended[0].get("type") != "task_completed" or ended[0].get("last_message") != "ok":
            raise RuntimeError(f"incorrect native result: {ended}")
    completed = time.perf_counter_ns()
    if len(mock.arrivals) != before + 1 or mock.errors:
        raise RuntimeError(f"expected one valid model request: {mock.errors}")
    arrival = mock.arrivals[before]
    if not start <= ready <= prompt_at <= arrival <= completed:
        raise RuntimeError("model arrival outside task lifetime")
    return {"start": start, "session_ready": ready, "prompt": prompt_at,
            "first_request": arrival, "completed": completed}


def measure(her, protocol, env, cwd, mock, timeout, launcher, sockpath):
    cmd = [her, "acp"] if protocol == "acp" else [her, "serve"]
    cmd += ["--listen", f"unix://{sockpath}"] if protocol == "socket" else (["--stdio"] if protocol == "stdio" else [])
    if launcher:
        cmd = [launcher, str(math.ceil(timeout)), *cmd]
    start = time.perf_counter_ns()
    proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdin=subprocess.PIPE,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
    conn = None
    try:
        if protocol == "socket":
            deadline = time.monotonic() + timeout
            while True:
                conn = socket.socket(socket.AF_UNIX)
                try:
                    conn.connect(str(sockpath))
                    break
                except (FileNotFoundError, ConnectionRefusedError):
                    conn.close()
                    conn = None
                    if proc.poll() is not None or time.monotonic() >= deadline:
                        raise RuntimeError("socket service did not become ready")
                    time.sleep(0.0001)
            peer = Peer(conn.fileno(), conn.fileno(), timeout)
        else:
            peer = Peer(proc.stdout.fileno(), proc.stdin.fileno(), timeout)
        params = {"protocolVersion": 1, "clientCapabilities": {}} if protocol == "acp" else {
            "protocol_version": 2, "client_info": {"name": "startup-bench"}}
        peer.rpc("initialize", params)
        initialized = time.perf_counter_ns()
        first = task(peer, protocol, cwd, mock)
        reused = task(peer, protocol, cwd, mock)
        if protocol == "socket":
            peer.rpc("server/shutdown", {})
        proc.stdin.close()
        proc.stdin = None
        stdout, stderr = proc.communicate(timeout=timeout + 5)
        if proc.returncode:
            raise RuntimeError(f"process exit {proc.returncode}: {stderr.decode(errors='replace')}")
        if stdout:
            # Shutdown can emit notifications, but must not emit unframed output.
            for line in stdout.splitlines():
                json.loads(line)
        if launcher:
            clock = json.loads(stderr.rsplit(b"\nHER_STARTUP_CLOCK ", 1)[1])
            start = clock["start_ns"]
        if not start <= initialized <= first["start"]:
            raise RuntimeError("initialize outside process lifetime (clock mismatch)")
        return {
            "initialize": (initialized - start) / 1e6,
            "session_ready": (first["session_ready"] - start) / 1e6,
            "first_request": (first["first_request"] - start) / 1e6,
            "completed": (first["completed"] - start) / 1e6,
            "session_new": (first["session_ready"] - first["start"]) / 1e6,
            "prompt_to_request": (first["first_request"] - first["prompt"]) / 1e6,
            "reuse_session_new": (reused["session_ready"] - reused["start"]) / 1e6,
            "reuse_new_to_request": (reused["first_request"] - reused["start"]) / 1e6,
            "reuse_prompt_to_request": (reused["first_request"] - reused["prompt"]) / 1e6,
        }
    finally:
        if conn:
            conn.close()
        if proc.poll() is None:
            proc.kill()
            proc.communicate(timeout=timeout + 5)
        sockpath.unlink(missing_ok=True)


def summarize(values):
    return {**stats(values), "mean": statistics.mean(values)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--her", required=True, action="append", metavar="LABEL=PATH")
    parser.add_argument("--protocol", nargs="+", choices=["acp", "socket", "stdio"], default=["acp", "socket"])
    parser.add_argument("--runs", type=int, default=100)
    parser.add_argument("--warmup", type=int, default=10)
    parser.add_argument("--skills", type=int, default=0)
    parser.add_argument("--timeout", type=float, default=30)
    parser.add_argument("--launcher", type=pathlib.Path)
    parser.add_argument("--json", required=True, type=pathlib.Path)
    args = parser.parse_args()
    if args.runs < 1 or args.warmup < 0 or args.skills < 0 or not math.isfinite(args.timeout) or not 0 < args.timeout <= 86400:
        parser.error("invalid runs/warmup/skills/timeout")
    if len(set(args.protocol)) != len(args.protocol):
        parser.error("protocols must be unique")
    if args.launcher and platform.system() != "Linux":
        parser.error("native clock requires Linux")
    launcher = str(args.launcher.resolve()) if args.launcher else None
    binaries = []
    for spec in args.her:
        label, sep, path = spec.partition("=")
        if not sep:
            label, path = pathlib.Path(spec).name, spec
        binaries.append((label, str(pathlib.Path(path).resolve())))
    if len(set(label for label, _ in binaries)) != len(binaries):
        parser.error("binary labels must be unique")
    mock = Mock()
    # Keep socket paths below macOS's sockaddr_un limit, even with long TMPDIRs.
    with tempfile.TemporaryDirectory(prefix="fxp-", dir="/tmp") as tmp:
        root = pathlib.Path(tmp)
        cases = [(f"{label}/{protocol}", path, protocol,
                  *prepare(root / str(i) / protocol, mock.port, args.skills,
                           'model = "mock/bench"\npermission_mode = "manual"\n'))
                 for i, (label, path) in enumerate(binaries) for protocol in args.protocol]
        samples = {name: [] for name, *_ in cases}
        first, floor = {}, []
        for i in range(args.runs + args.warmup):
            base = run_once(["/usr/bin/true"], cases[0][3], cases[0][4], timeout=args.timeout, launcher=launcher)
            if i >= args.warmup:
                floor.append(base["exit"])
            shift = i % len(cases)
            for name, path, protocol, env, cwd in cases[shift:] + cases[:shift]:
                row = measure(path, protocol, env, cwd, mock, args.timeout, launcher, root / "s.sock")
                first.setdefault(name, row)
                if i >= args.warmup:
                    samples[name].append(row)
    summaries = {name: {key: summarize([row[key] for row in rows]) for key in rows[0]}
                 for name, rows in samples.items()}
    boundary = "native parent immediately before fork" if launcher else "Python parent before Popen"
    report = {
        "schema_version": 1, "units": "ms", "clock": "CLOCK_MONOTONIC / perf_counter_ns",
        "definition": f"{boundary} -> initialize reply -> session/new reply -> first model HTTP bytes at loopback mock",
        "reuse_definition": "second new session on the same initialized connection/process; excludes process startup",
        "cache": "warm filesystem; fresh process each sample; first_invocation uses a fresh private HER_HOME, not a claim of OS-cold cache",
        "socket_poll_interval_us": 100, "observation_cost": "protocol client round trips, socket discovery and mock scheduling included; no floor subtraction",
        "platform": {"system": platform.system(), "release": platform.release(), "machine": platform.machine(), "cpus": os.cpu_count(), "python": platform.python_version()},
        "workload": {"runs": args.runs, "warmup": args.warmup, "skills": args.skills, "protocols": args.protocol, "prompt": "hello", "network": "loopback HTTP, no DNS/TLS/inference", "sessions_per_process": 2},
        "binaries": [{"label": label, "path": path, "sha256": hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()} for label, path in binaries],
        "harness_sha256": {name: hashlib.sha256(pathlib.Path(__file__).with_name(name).read_bytes()).hexdigest() for name in ["bench_protocol_startup.py", "bench_startup.py"]},
        "launcher_sha256": hashlib.sha256(pathlib.Path(launcher).read_bytes()).hexdigest() if launcher else None,
        "first_invocation": first, "spawn_floor": summarize(floor), "spawn_floor_samples": floor,
        "summaries": summaries, "samples": samples, "passed": True,
    }
    args.json.parent.mkdir(parents=True, exist_ok=True)
    args.json.write_text(json.dumps(report, indent=2) + "\n")
    print(f"{'case':24} {'metric':24} {'mean':>8} {'p50':>8} {'p95':>8} {'p99':>8} (ms)")
    for name, summary in summaries.items():
        for key, value in summary.items():
            print(f"{name:24} {key:24} {value['mean']:8.3f} {value['p50']:8.3f} {value['p95']:8.3f} {value['p99']:8.3f}")


if __name__ == "__main__":
    main()
