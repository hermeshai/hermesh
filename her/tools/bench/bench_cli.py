#!/usr/bin/env python3
"""Local commands: a page-fault budget, and their wall time by fx's hyperfine -N / raw-mean method.

No model requests are measured here. Use bench_startup.py for a real task's first request.
Each binary gets private configuration and eight long sessions. Her fixtures are valid
JSONL; optional fx fixtures use fx's own session_list_fixture.py from a pinned checkout.
Cold first invocations (including projection creation) are retained separately. Everything
else uses warm filesystem/projections with a fresh process every time, without subtraction.

A mean of 2 ms is the product's target, measured interleaved on one machine. It cannot gate
here: on CI runners one binary's time moves by more than a command's headroom, and far more
than a real regression (technical design D-98). What gates, on Linux:
- page faults against local-command-budget.json, which a given binary all but repeats;
- a limit on the fastest run, for slowdowns that touch no new pages. Noise can raise the mean
  and the median of a run without raising its fastest process.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import tempfile
import time

from bench_startup import prepare

BUDGET = Path(__file__).with_name("local-command-budget.json")
TARGET_MEAN_MS = 2


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def cpu_model():
    """The artifact says which kind of runner a run got."""
    try:
        for line in Path("/proc/cpuinfo").read_text().splitlines():
            if line.startswith("model name"):
                return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return platform.processor() or None


def page_faults(command, env, cwd, runs):
    """Minor page faults of one fresh process, spawn to exit, over `runs` processes."""
    silent = [(os.POSIX_SPAWN_OPEN, 0, os.devnull, os.O_RDONLY, 0),
              (os.POSIX_SPAWN_OPEN, 1, os.devnull, os.O_WRONLY, 0),
              (os.POSIX_SPAWN_OPEN, 2, os.devnull, os.O_WRONLY, 0)]
    previous = os.getcwd()
    os.chdir(cwd)  # posix_spawn has no directory of its own to offer
    try:
        samples = []
        for _ in range(runs):
            pid = os.posix_spawn(command[0], command, env, file_actions=silent)
            _, status, usage = os.wait4(pid, 0)
            if status:
                raise RuntimeError(f"{command}: wait status {status}")
            samples.append(usage.ru_minflt)
    finally:
        os.chdir(previous)
    samples.sort()
    return dict(median=samples[len(samples) // 2], min=samples[0], max=samples[-1])


def write_sessions(home, workspace, count, size):
    directory = home / "sessions" / "2026" / "11" / "03"
    directory.mkdir(parents=True)
    for index in range(count):
        session = f"00000000-0000-7000-8000-{index:012d}"
        header = dict(type="session", version=2, id=session, created_at="2026-11-03T00:00:00Z",
                      cwd=str(workspace), her_version="0.1.0")
        log = directory / f"20261103T000000-{session}.jsonl"
        with log.open("w") as output:
            def line(value):
                output.write(json.dumps(value, separators=(",", ":")) + "\n")
            line(header)
            line(dict(id="first", parent=None, ts=0, type="message", message=dict(
                role="user", content=[dict(type="text", text=f"Benchmark session {index}")], ts=0)))
            parent = "first"
            number = 0
            while output.tell() < size:
                entry = f"message-{number}"
                line(dict(id=entry, parent=parent, ts=number + 1, type="message", message=dict(
                    role="assistant", provider="mock", api="openai-chat", model="bench",
                    content=[dict(type="text", text="x" * 16384)], stop_reason="stop", ts=number + 1)))
                parent = entry
                number += 1
            line(dict(id="title", parent=parent, ts=number + 1, type="title", title=f"Benchmark session {index}"))
        log.chmod(0o600)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--her", action="append", required=True, metavar="LABEL=PATH")
    parser.add_argument("--fx", type=Path, help="optional freshly built fx reference binary")
    parser.add_argument("--fx-fixture", type=Path, help="fx's benchmarks/session_list_fixture.py")
    parser.add_argument("--hyperfine", default="hyperfine")
    parser.add_argument("--runs", type=int, default=100)
    parser.add_argument("--warmup", type=int, default=10)
    parser.add_argument("--sessions", type=int, default=8)
    parser.add_argument("--log-mib", type=int, default=8)
    parser.add_argument("--fault-runs", type=int, default=50)
    parser.add_argument("--budget", type=Path, default=BUDGET, help="page-fault budget of each command")
    parser.add_argument("--max-min-ms", type=float, default=3, help="limit on a command's fastest run")
    parser.add_argument("--json", type=Path, required=True)
    args = parser.parse_args()
    if args.runs < 2 or args.warmup < 1 or args.fault_runs < 1 or args.sessions < 1 or args.log_mib < 1:
        parser.error("runs >= 2; warmup, fault-runs, sessions and log-mib >= 1 required")
    if not 0 < args.max_min_ms < float("inf"):
        parser.error("max-min-ms must be finite and positive")
    if bool(args.fx) != bool(args.fx_fixture):
        parser.error("--fx and --fx-fixture must be supplied together")
    hyperfine = shutil.which(args.hyperfine)
    if hyperfine is None:
        parser.error("hyperfine is required (fx uses 1.19.0)")
    hyperfine = str(Path(hyperfine).resolve())
    binaries = []
    for spec in args.her:
        label, separator, path = spec.partition("=")
        if not separator:
            label, path = "her", spec
        binaries.append((label, Path(path).resolve(), False))
    if args.fx:
        binaries.append(("fx", args.fx.resolve(), True))
    if len({label for label, _, _ in binaries}) != len(binaries):
        parser.error("labels must be unique")
    budgets = json.loads(args.budget.read_text())["page_faults"]
    report = dict(schema_version=2, definition="hyperfine -N, raw process wall-time; no shell or floor subtraction. "
                  "Page faults: ru_minflt of one process, spawn to exit, with output to /dev/null",
                  platform=platform.platform(), cpu=cpu_model(), runs=args.runs, warmup=args.warmup,
                  fault_runs=args.fault_runs, sessions=args.sessions, log_mib=args.log_mib,
                  target_mean_ms=TARGET_MEAN_MS, max_min_ms=args.max_min_ms, page_fault_budget=budgets,
                  cache="warm filesystem and session projections; no resident agent; cold first invocations reported separately",
                  hyperfine=subprocess.check_output([hyperfine, "--version"], text=True).strip(),
                  harness_sha256=digest(__file__), binaries=[dict(label=l, path=str(p), sha256=digest(p)) for l, p, _ in binaries],
                  fx_fixture_sha256=digest(args.fx_fixture) if args.fx_fixture else None,
                  first_invocations={}, results=[], page_faults={}, failures=[])
    with tempfile.TemporaryDirectory(prefix="her-cli-bench-") as temporary:
        root = Path(temporary).resolve()  # macOS: /var is a link, and sessions match by the real path
        for index, (label, binary, is_fx) in enumerate(binaries):
            env, cwd = prepare(root / str(index), 1, config='model = "mock/bench"\npermission_mode = "manual"\n')
            if is_fx:
                env = {key: value for key, value in env.items() if not key.startswith("FX_")}
                fx_home = Path(env["HOME"]) / ".fx"
                fx_home.mkdir(mode=0o700)
                settings = fx_home / "settings.json"
                settings.write_text(json.dumps(dict(model="openai/gpt-5.4", effort="high", fast_mode=False,
                    startup_scrollback=True, prompt_history=dict(enabled=True),
                    statusLine=dict(sandbox=True, context=True), permission=dict(bash={"git status *": "allow"}))))
                settings.chmod(0o600)
                subprocess.run(["python3", str(args.fx_fixture.resolve()), "--home", env["HOME"],
                                "--workspace", str(cwd), "--sessions", str(args.sessions),
                                "--log-size", str(args.log_mib * 1024 * 1024)], check=True)
            else:
                write_sessions(Path(env["HER_HOME"]), cwd, args.sessions, args.log_mib * 1024 * 1024)
            commands = [("help", ["help"] if is_fx else ["--help"]), ("status", ["status", "--json"]),
                        ("doctor", ["doctor", "--json"]), ("sessions", ["sessions", "--json"])]
            for name, suffix in commands:
                command = [str(binary), *suffix]
                start = time.perf_counter_ns()
                first = subprocess.run(command, env=env, cwd=cwd, capture_output=True, timeout=60)
                report["first_invocations"][f"{label}/{name}"] = (time.perf_counter_ns() - start) / 1e6
                if first.returncode:
                    raise RuntimeError(f"{command}: {first.stderr.decode(errors='replace')}")
                if name != "help":
                    result = json.loads(first.stdout)
                    if name == "sessions":
                        rows = result if isinstance(result, list) else result.get("sessions", [])
                        if len(rows) != args.sessions:
                            raise RuntimeError(f"{label}: expected {args.sessions} sessions, got {len(rows)}")
                output = root / "measurement.json"
                subprocess.run([hyperfine, "-N", "--runs", str(args.runs), "--warmup", str(args.warmup),
                                "--export-json", str(output), "--command-name", f"{label}/{name}", shlex.join(command)],
                               env=env, cwd=cwd, check=True, stdout=subprocess.DEVNULL, timeout=max(60, args.runs * 5))
                result = json.loads(output.read_text())["results"][0]
                report["results"].append(result)
                faults = page_faults(command, env, cwd, args.fault_runs)
                report["page_faults"][f"{label}/{name}"] = faults
                gated = platform.system() == "Linux" and not is_fx
                budget = budgets.get(name) if gated else None
                fastest = result["min"] * 1000
                print(f"{label}/{name}: mean {result['mean'] * 1000:.3f}ms, median {result['median'] * 1000:.3f}ms, "
                      f"min {fastest:.3f}ms, {faults['median']} page faults"
                      + (f" (budget {budget})" if budget else ""), flush=True)
                if budget and faults["median"] > budget:
                    report["failures"].append(f"{label}/{name}: {faults['median']} page faults > budget {budget} "
                                              f"(see {args.budget.name})")
                if gated and fastest > args.max_min_ms:
                    report["failures"].append(f"{label}/{name}: fastest run {fastest:.3f}ms > {args.max_min_ms}ms")
        floor_file = root / "floor.json"
        subprocess.run([hyperfine, "-N", "--runs", str(args.runs), "--warmup", str(args.warmup),
                        "--export-json", str(floor_file), "/usr/bin/true"], check=True, stdout=subprocess.DEVNULL)
        report["spawn_floor"] = json.loads(floor_file.read_text())["results"][0]
        report["spawn_floor_page_faults"] = page_faults(["/usr/bin/true"], dict(os.environ), root, args.fault_runs)
    report["passed"] = not report["failures"]
    args.json.parent.mkdir(parents=True, exist_ok=True)
    args.json.write_text(json.dumps(report, indent=2) + "\n")
    if report["failures"]:
        raise SystemExit("\n".join(report["failures"]))


if __name__ == "__main__":
    main()
