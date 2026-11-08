#!/usr/bin/env python3
"""Start-up benchmark for `her exec`, without a real model.

A local mock of an OpenAI-compatible endpoint stands in for the provider. For every run the
script records, relative to the moment the process is spawned:

  first_request  the first byte of the first model request reaches the mock
  exit           the process has exited (the mock answers with one short message)

`/usr/bin/true` is measured alongside as the cost of spawning any process at all. Several
binaries can be compared; they run interleaved so that background load affects them alike.
`--skills 0 25 200` repeats every binary with that many skills installed in the user's
`~/.agents/skills`, each with a description of typical length.

    cargo build --release
    python3 tools/bench/bench_startup.py --her new=target/release/her --her old=/path/to/old/her

Uses only the Python standard library. Runs in a temporary HER_HOME and HOME (so the skills
of the machine stay out) and never contacts a real provider.

For a Linux process-start clock without Python's Popen setup overhead:
    cc -O2 -Wall -Wextra -Werror tools/bench/startup_launcher.c -o /tmp/startup-launcher
    python3 tools/bench/bench_startup.py --her target/release/her --launcher /tmp/startup-launcher \
        --runs 200 --json target/startup.json --max-first-request-p50-ms 2

The native clock includes fork, exec and the loader. Neither clock subtracts the true-process
floor. TCP receive time includes scheduling the mock on this machine. The default workload
uses loopback HTTP, not DNS, TLS or a real provider's inference latency.
"""

import argparse
import hashlib
import json
import math
import platform
import os
import pathlib
import socket
import statistics
import subprocess
import sys
import tempfile
import threading
import time

SSE = (
    'data: {"id":"b","object":"chat.completion.chunk","created":0,"model":"bench",'
    '"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}\n\n'
    'data: {"id":"b","object":"chat.completion.chunk","created":0,"model":"bench",'
    '"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],'
    '"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}\n\n'
    "data: [DONE]\n\n"
).encode()
RESPONSE = (
    b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n"
    + f"Content-Length: {len(SSE)}\r\n\r\n".encode()
    + SSE
)


class Mock:
    """One request at a time on a blocking socket, so the arrival time is taken promptly."""

    def __init__(self):
        self.listener = socket.create_server(("127.0.0.1", 0))
        self.port = self.listener.getsockname()[1]
        self.arrivals = []
        self.errors = []
        threading.Thread(target=self._serve, daemon=True).start()

    def _serve(self):
        while True:
            conn, _ = self.listener.accept()
            with conn:
                data = conn.recv(65536)
                arrived = time.perf_counter_ns()
                while b"\r\n\r\n" not in data:
                    chunk = conn.recv(65536)
                    if not chunk:
                        break
                    data += chunk
                head, _, body = data.partition(b"\r\n\r\n")
                # Resident protocols may refresh the public model catalog in the
                # background. Keep that request local and out of model timings.
                if head.startswith(b"GET /catalog HTTP/1.1\r\n"):
                    conn.sendall(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
                    continue
                length = 0
                for line in head.split(b"\r\n")[1:]:
                    name, _, value = line.partition(b":")
                    if name.strip().lower() == b"content-length":
                        length = int(value)
                while len(body) < length:
                    chunk = conn.recv(65536)
                    if not chunk:
                        break
                    body += chunk
                self.arrivals.append(arrived)
                try:
                    request = json.loads(body)
                    if not head.startswith(b"POST /chat/completions HTTP/1.1\r\n"):
                        raise ValueError("unexpected endpoint")
                    if request.get("model") != "bench" or not request.get("stream") or not request.get("tools"):
                        raise ValueError("expected a streaming model request with kernel tools")
                    if not any(m.get("role") == "user" and m.get("content") == "hello"
                               for m in request.get("messages", [])):
                        raise ValueError("the task input did not reach the model")
                except (ValueError, TypeError, AttributeError) as error:
                    self.errors.append(str(error))
                conn.sendall(RESPONSE)


def install_skills(user: pathlib.Path, count: int):
    """`count` skills shaped like real ones: a folded description of about 700 characters."""
    for i in range(count):
        skill = user / ".agents" / "skills" / f"skill-{i:03}"
        (skill / "references").mkdir(parents=True)
        words = f"Builds, checks and ships widget pipeline number {i} for the team. "
        description = (words * 11)[:700]
        lines = "\n".join("  " + description[j : j + 70] for j in range(0, len(description), 70))
        body = f"# Skill {i}\n\nRead `references/guide.md` first.\n\n" + "Step text for the skill.\n" * 120
        (skill / "SKILL.md").write_text(f"---\nname: skill-{i:03}\ndescription: >-\n{lines}\n---\n{body}")
        (skill / "references" / "guide.md").write_text("Guide.\n")


def prepare(base: pathlib.Path, port: int, skills: int = 0, config: str = ""):
    home, proj, user = base / "home", base / "proj", base / "user"
    home.mkdir(parents=True)
    user.mkdir(parents=True)
    install_skills(user, skills)
    if config:
        (home / "config.toml").write_text(config)
    (home / "models.toml").write_text(
        f"""[providers.mock]
api = "openai-chat"
base_url = "http://127.0.0.1:{port}"
auth = "none"

[[providers.mock.models]]
id = "bench"
context_window = 128000
max_output_tokens = 8192
"""
    )
    # A typical project: a git checkout with an instruction file.
    (proj / ".git").mkdir(parents=True)
    (proj / ".git" / "HEAD").write_text("ref: refs/heads/main\n")
    (proj / "AGENTS.md").write_text("Use 4-space indentation.\n")
    env = {k: v for k, v in os.environ.items() if not k.endswith("_API_KEY")}
    for key in ("HER_MODEL", "HER_PERMISSION_MODE", "HER_APPROVAL", "HER_LOG", "HER_STARTUP_TRACE"):
        env.pop(key, None)
    env.update(HER_HOME=str(home), HOME=str(user), NO_COLOR="1", HER_CREDENTIAL_STORE="file",
               HER_MODEL_CATALOG_URL=f"http://127.0.0.1:{port}/catalog")
    return env, proj


def run_once(cmd, env, cwd, mock=None, timeout=30, launcher=None):
    before = len(mock.arrivals) if mock else 0
    start = time.perf_counter_ns()
    launched = [launcher, str(math.ceil(timeout)), *cmd] if launcher else cmd
    proc = subprocess.run(launched, env=env, cwd=cwd, stdin=subprocess.DEVNULL, capture_output=True,
                          timeout=timeout + 5 if launcher else timeout)
    end = time.perf_counter_ns()
    if proc.returncode != 0:
        sys.exit(f"{cmd[0]} exited with {proc.returncode}:\n{proc.stderr.decode(errors='replace')}")
    if launcher:
        marker = b"\nHER_STARTUP_CLOCK "
        clock = json.loads(proc.stderr.rsplit(marker, 1)[1])
        start, end = clock["start_ns"], clock["end_ns"]
    row = {"exit": (end - start) / 1e6}
    if mock:
        if mock.errors:
            sys.exit(f"invalid model request: {mock.errors}")
        if len(mock.arrivals) != before + 1:
            sys.exit("expected exactly one model request")
        row["first_request"] = (mock.arrivals[before] - start) / 1e6
        if not 0 <= row["first_request"] <= row["exit"]:
            sys.exit("arrival is outside the measured process lifetime (clock mismatch)")
    return row


def stats(values):
    values = sorted(values)
    def percentile(q):
        return values[min(len(values) - 1, max(0, math.ceil(q * len(values)) - 1))]
    return {"n": len(values), "min": values[0], "p50": statistics.median(values),
            "p95": percentile(0.95), "p99": percentile(0.99), "max": values[-1]}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--her", action="append", required=True, metavar="LABEL=PATH", help="binary to measure (repeatable)")
    parser.add_argument("--runs", type=int, default=100)
    parser.add_argument("--warmup", type=int, default=10)
    parser.add_argument("--skills", type=int, nargs="+", default=[0], metavar="N")
    parser.add_argument("--config", default="", metavar="TOML", help="contents of config.toml in the temporary home")
    parser.add_argument("--timeout", type=float, default=30)
    parser.add_argument("--json", type=pathlib.Path, help="write raw samples, binary hashes and environment")
    parser.add_argument("--launcher", type=pathlib.Path, help="Linux startup_launcher.c binary: clock immediately before fork")
    parser.add_argument("--max-first-request-p50-ms", type=float)
    parser.add_argument("--max-first-request-p95-ms", type=float)
    args = parser.parse_args()
    if (args.runs < 1 or args.warmup < 0 or any(n < 0 for n in args.skills)
            or not math.isfinite(args.timeout) or not 0 < args.timeout <= 86400):
        parser.error("runs/timeout must be positive; warmup/skills must be nonnegative")
    if len(set(args.skills)) != len(args.skills):
        parser.error("skill counts must be unique")
    if args.launcher and platform.system() != "Linux":
        parser.error("the native launcher clock is supported on Linux only")
    launcher = str(args.launcher.resolve()) if args.launcher else None
    for limit in [args.max_first_request_p50_ms, args.max_first_request_p95_ms]:
        if limit is not None and (not math.isfinite(limit) or limit < 0):
            parser.error("latency budgets must be finite and nonnegative")

    binaries = []
    for spec in args.her:
        label, sep, path = spec.partition("=")
        if not sep:
            label, path = pathlib.Path(spec).name, spec
        binaries.append((label, str(pathlib.Path(path).resolve())))
    if len(set(label for label, _ in binaries)) != len(binaries):
        parser.error("binary labels must be unique")
    mock = Mock()
    with tempfile.TemporaryDirectory(prefix="her-bench-") as tmp:
        cases = [(label if len(args.skills) == 1 else f"{label}/{n} skills", her,
                  *prepare(pathlib.Path(tmp) / f"case-{i}-{n}", mock.port, n, args.config))
                 for i, (label, her) in enumerate(binaries) for n in args.skills]
        floor, first = [], {}
        results = {label: [] for label, _, _, _ in cases}
        for i in range(args.warmup + args.runs):
            row = run_once(["/usr/bin/true"], cases[0][2], cases[0][3], timeout=args.timeout, launcher=launcher)
            if i >= args.warmup:
                floor.append(row["exit"])
            shift = i % len(cases)
            for label, her, env, proj in cases[shift:] + cases[:shift]:
                cmd = [her, "exec", "-m", "mock/bench", "--permission-mode", "manual", "hello"]
                row = run_once(cmd, env, proj, mock, args.timeout, launcher)
                first.setdefault(label, row)
                if i >= args.warmup:
                    results[label].append(row)

    summaries = {label: {k: stats([r[k] for r in rows]) for k in ("first_request", "exit")}
                 for label, rows in results.items()}
    failures = []
    for label, summary in summaries.items():
        for key, limit in [("p50", args.max_first_request_p50_ms), ("p95", args.max_first_request_p95_ms)]:
            value = summary["first_request"][key]
            if limit is not None and value > limit:
                failures.append(f"{label}: first_request {key} {value:.3f}ms exceeds {limit:.3f}ms")
    boundary = "native parent immediately before fork" if launcher else "Python parent before subprocess.run"
    report = {"schema_version": 1, "clock": "CLOCK_MONOTONIC / perf_counter_ns", "units": "ms",
              "definition": f"{boundary} -> first byte of first model request at loopback mock; every sample is a new process with normal logging and session persistence",
              "launcher": {"path": launcher, "sha256": hashlib.sha256(pathlib.Path(launcher).read_bytes()).hexdigest()} if launcher else None,
              "cache": "warm filesystem after warmup; no resident her process; first_invocation is not a claim of OS-cold cache",
              "platform": {"system": platform.system(), "release": platform.release(), "machine": platform.machine(), "python": platform.python_version(), "cpus": os.cpu_count()},
              "workload": {"runs": args.runs, "warmup": args.warmup, "skills": args.skills, "config": args.config},
              "binaries": [{"label": label, "path": path, "sha256": hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()} for label, path in binaries],
              "harness_sha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(),
              "spawn_floor": stats(floor), "spawn_floor_samples": floor, "first_invocation": first,
              "summaries": summaries, "samples": results, "passed": not failures, "gate_failures": failures}
    if args.json:
        args.json.parent.mkdir(parents=True, exist_ok=True)
        args.json.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    width = max(16, *(len(label) + 1 for label in results))
    print(f"{'':<{width}} {'metric':<14} {'min':>7} {'p50':>7} {'p95':>7} {'p99':>7}   (ms, {args.runs} runs)")
    def show(label, key, values):
        print(f"{label:<{width}} {key:<14} {values['min']:7.2f} {values['p50']:7.2f} {values['p95']:7.2f} {values['p99']:7.2f}")
    show("/usr/bin/true", "exit", report["spawn_floor"])
    for label, summary in summaries.items():
        for key, values in summary.items():
            show(label, key, values)
    if failures:
        sys.exit("\n".join(failures))


if __name__ == "__main__":
    main()
