#!/usr/bin/env python3
"""Interleaved comparison of local commands between her builds, on one Linux machine.

The fixture is bench_cli.py's. Time comes from a native clock read before fork (startup_launcher.c),
page faults from the same measurement as the CI's budget. The first build is the baseline. Between
builds the time follows the page faults closely, so read both: a time change without a fault change
is either plain computation or noise.

Each build runs from several fresh copies, because one file is a sample of one. Byte-identical
copies differ by several percent for as long as they stay in the page cache, whatever the number of
runs. So a build's number is the median of its copies' medians, and the p-value compares those.

  python3 tools/bench/local_ab.py --launcher target/startup-launcher \
      before=/path/to/her-before after=target/x86_64-unknown-linux-musl/dist/her
"""
import argparse
import json
import math
from pathlib import Path
import resource
import shutil
import statistics
import subprocess
import tempfile

from bench_cli import page_faults, write_sessions
from bench_startup import prepare

COMMANDS = {"help": ["--help"], "status": ["status", "--json"], "doctor": ["doctor", "--json"],
            "sessions": ["sessions", "--json"]}


def once(launcher, command, env, cwd):
    before = resource.getrusage(resource.RUSAGE_CHILDREN)
    run = subprocess.run([launcher, "30", *command], env=env, cwd=cwd, stdin=subprocess.DEVNULL, capture_output=True)
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    if run.returncode:
        raise RuntimeError(f"{command}: {run.returncode} {run.stderr[-300:]!r}")
    clock = json.loads(run.stderr.rsplit(b"\nHER_STARTUP_CLOCK ", 1)[1])
    return dict(ms=(clock["end_ns"] - clock["start_ns"]) / 1e6,
                user_ms=(after.ru_utime - before.ru_utime) * 1e3, sys_ms=(after.ru_stime - before.ru_stime) * 1e3)


def mann_whitney_p(a, b):
    """Two-sided p-value, normal approximation with tie correction."""
    values = sorted([(v, 0) for v in a] + [(v, 1) for v in b])
    ranks, i, ties = [0.0] * len(values), 0, 0.0
    while i < len(values):
        j = i
        while j + 1 < len(values) and values[j + 1][0] == values[i][0]:
            j += 1
        for k in range(i, j + 1):
            ranks[k] = (i + j) / 2 + 1
        ties += (j - i + 1) ** 3 - (j - i + 1)
        i = j + 1
    n1, n2 = len(a), len(b)
    u = sum(r for r, (_, group) in zip(ranks, values) if group == 0) - n1 * (n1 + 1) / 2
    n = n1 + n2
    sigma = math.sqrt(n1 * n2 / 12 * ((n + 1) - ties / (n * (n - 1))))
    return math.erfc(max(abs(u - n1 * n2 / 2) - 0.5, 0) / sigma / math.sqrt(2)) if sigma else 1.0


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("builds", nargs="+", metavar="LABEL=PATH")
    parser.add_argument("--launcher", type=Path, required=True)
    parser.add_argument("--copies", type=int, default=6, help="fresh copies of each build")
    parser.add_argument("--runs", type=int, default=100, help="runs of each command on each copy")
    parser.add_argument("--warmup", type=int, default=10)
    parser.add_argument("--fault-runs", type=int, default=50)
    parser.add_argument("--json", type=Path)
    args = parser.parse_args()
    launcher = str(args.launcher.resolve())
    builds = [(spec.split("=", 1)[0], str(Path(spec.split("=", 1)[1]).resolve())) for spec in args.builds]
    baseline = builds[0][0]
    samples = {label: {name: [[] for _ in range(args.copies)] for name in COMMANDS} for label, _ in builds}
    faults = {label: {} for label, _ in builds}
    with tempfile.TemporaryDirectory(prefix="local-ab-") as temporary:
        root = Path(temporary).resolve()
        fixtures = {}
        for index, (label, _) in enumerate(builds):
            env, cwd = prepare(root / str(index), 1, config='model = "mock/bench"\npermission_mode = "manual"\n')
            write_sessions(Path(env["HER_HOME"]), cwd, 8, 8 * 1024 * 1024)
            fixtures[label] = (env, cwd)
        # A process runs faster right after one from the same file, so no two in a row share one.
        cases = []
        for copy in range(args.copies):
            for label, path in builds:
                clone = root / f"{label}-{copy}" / "her"
                clone.parent.mkdir()
                shutil.copy2(path, clone)
                cases.append((label, copy, str(clone), *fixtures[label]))
        env, cwd = fixtures[baseline]
        floor = []
        for i in range(args.warmup + args.runs):
            row = once(launcher, ["/usr/bin/true"], env, cwd)
            if i >= args.warmup:
                floor.append(row["ms"])
            shift = i % len(cases)  # nothing always goes first
            for name, suffix in COMMANDS.items():
                for label, copy, path, env, cwd in cases[shift:] + cases[:shift]:
                    row = once(launcher, [path, *suffix], env, cwd)
                    if i >= args.warmup:
                        samples[label][name][copy].append(row)
        for label, copy, path, env, cwd in cases[:len(builds)]:
            for name, suffix in COMMANDS.items():
                faults[label][name] = page_faults([path, *suffix], env, cwd, args.fault_runs)
    print(f"/usr/bin/true: median {statistics.median(floor):.3f} ms; {args.copies} copies of each build, {args.runs} runs each")
    print(f"{'command':<10}{'build':<12}{'median':>8}{'copies from':>13}{'to':>7}{'change':>9}{'p':>8}{'faults':>8}{'user':>7}{'sys':>7}")
    for name in COMMANDS:
        reference = [statistics.median(row["ms"] for row in rows) for rows in samples[baseline][name]]
        for label, _ in builds:
            copies = [statistics.median(row["ms"] for row in rows) for rows in samples[label][name]]
            median = statistics.median(copies)
            flat = [row for rows in samples[label][name] for row in rows]
            print(f"{name:<10}{label:<12}{median:8.3f}{min(copies):13.3f}{max(copies):7.3f}"
                  f"{(median / statistics.median(reference) - 1) * 100:8.1f}%{mann_whitney_p(reference, copies):8.3f}"
                  f"{faults[label][name]['median']:8}{statistics.mean(row['user_ms'] for row in flat):7.3f}"
                  f"{statistics.mean(row['sys_ms'] for row in flat):7.3f}")
    if args.json:
        args.json.write_text(json.dumps(dict(samples=samples, page_faults=faults, floor_ms=floor)) + "\n")


if __name__ == "__main__":
    main()
