#!/usr/bin/env python3
"""Ratchet comparison of two her builds: every metric must hold, at least one should improve.

Runs base and candidate interleaved on the same machine and prints medians with a two-sided
Mann-Whitney U p-value. A change counts as a regression when it is worse by more than 2% and
p < 0.05.

Start-up metrics run each build from several fresh copies, because one file is a sample of one:
byte-identical copies differ by several percent for as long as they stay in the page cache, whatever
the number of runs. So a metric is first reduced to one median per copy, and both the change and the
p-value compare those medians. With 6 copies a side, p cannot go below about 0.005.

Chance still flags a small metric in about one start-up comparison out of ten, even between two copies
of one binary: repeat before calling one, the next run draws new copies. Fewer runs per copy flag more
and catch less (docs/kernel/benchmarks/1105-local-command-gate-report.md, section 6).

  cargo build --release -p her-cli -p her-bench
  python3 tools/bench/ratchet.py --repo . --base /path/to/her-before --cand target/release/her \
      --out target/ratchet [--linux-host HOST --linux-base LB --linux-cand LC]

The Linux part copies the binaries, bench_startup.py and bench_protocol_startup.py to
~/her-ratchet on HOST over ssh and makes the fresh copies there; a `startup-launcher` built from
startup_launcher.c must be there.
"""
import argparse, json, math, pathlib, shutil, statistics, subprocess, sys, tempfile, time


def mann_whitney_p(a, b):
    """Two-sided p-value, normal approximation with tie correction."""
    if len(a) < 2 or len(b) < 2:
        return 1.0
    allv = sorted([(v, 0) for v in a] + [(v, 1) for v in b])
    ranks = [0.0] * len(allv)
    i = 0
    ties = 0.0
    while i < len(allv):
        j = i
        while j + 1 < len(allv) and allv[j + 1][0] == allv[i][0]:
            j += 1
        r = (i + j) / 2 + 1
        for k in range(i, j + 1):
            ranks[k] = r
        t = j - i + 1
        ties += t ** 3 - t
        i = j + 1
    n1, n2 = len(a), len(b)
    r1 = sum(r for r, (_, g) in zip(ranks, allv) if g == 0)
    u = r1 - n1 * (n1 + 1) / 2
    mu = n1 * n2 / 2
    n = n1 + n2
    sigma = math.sqrt(n1 * n2 / 12 * ((n + 1) - ties / (n * (n - 1))))
    if sigma == 0:
        return 1.0
    z = (abs(u - mu) - 0.5) / sigma
    return math.erfc(max(z, 0) / math.sqrt(2))


ROWS = []


def compare(group, name, base, cand, higher_is_better=False, threshold=2.0):
    base, cand = [x for x in base if x is not None], [x for x in cand if x is not None]
    if not base or not cand:
        return
    mb, mc = statistics.median(base), statistics.median(cand)
    change = (mc - mb) / mb * 100 if mb else 0.0
    p = mann_whitney_p(base, cand)
    worse = change < -threshold if higher_is_better else change > threshold
    better = change > threshold if higher_is_better else change < -threshold
    verdict = "REGRESSION" if worse and p < 0.05 else "better" if better and p < 0.05 else "~"
    ROWS.append(dict(group=group, metric=name, base=mb, cand=mc, change_pct=change, p=p, verdict=verdict,
                     n=(len(base), len(cand))))


def run(cmd, **kw):
    print("+", " ".join(str(c) for c in cmd), flush=True)
    return subprocess.run(cmd, check=True, **kw)


PROTOCOLS = ("acp", "socket")
# Rounds per copy. A new file's first run is slow, about 40 times on macOS, which validates it,
# and there the next few still show it.
STARTUP_WARMUP = 5


def copy_labels(copies):
    """(build, label) of every copy, equally long and in the order they run: base and candidate
    alternate, because a process runs faster right after one from the same file."""
    width = len(str(copies - 1))
    return [(build, f"{build}-{i:0{width}}") for i in range(copies) for build in ("base", "cand")]


def her_args(root, copies):
    return [arg for _, label in copy_labels(copies) for arg in ("--her", f"{label}={root}/{label}/her")]


def compare_copies(group, key, samples, copies, suffix=""):
    """Compares one median per copy, not the runs: however many runs it gets, a file is a sample of one."""
    def medians(wanted):
        return [statistics.median(r[key] for r in samples[label + suffix])
                for build, label in copy_labels(copies) if build == wanted]
    compare(group, key, medians("base"), medians("cand"))


def startup_rows(prefix, stem, copies):
    samples = json.loads(pathlib.Path(f"{stem}-cli.json").read_text())["samples"]
    for key in ("first_request", "exit"):
        compare_copies(f"{prefix} cli", key, samples, copies)
    for proto in PROTOCOLS:
        protocol_rows(json.loads(pathlib.Path(f"{stem}-{proto}.json").read_text()), prefix, proto, copies)


def protocol_rows(d, prefix, proto, copies):
    for key in ("initialize", "session_ready", "first_request", "session_new", "prompt_to_request",
                "reuse_new_to_request", "reuse_prompt_to_request"):
        compare_copies(f"{prefix} {proto}", key, d["samples"], copies, f"/{proto}")


def startup_mac(args, out):
    with tempfile.TemporaryDirectory(prefix="her-ratchet-") as root:
        for build, label in copy_labels(args.copies):
            clone = pathlib.Path(root) / label / "her"
            clone.parent.mkdir()
            shutil.copy2(args.base if build == "base" else args.cand, clone)
        common = [*her_args(root, args.copies), "--runs", str(args.startup_runs), "--warmup", str(STARTUP_WARMUP)]
        tools = args.repo / "tools/bench"
        run([sys.executable, tools / "bench_startup.py", *common, "--json", out / "mac-cli.json"],
            stdout=subprocess.DEVNULL)
        # One protocol at a time: run together, each socket run would follow an ACP run of the same file.
        for proto in PROTOCOLS:
            run([sys.executable, tools / "bench_protocol_startup.py", *common, "--protocol", proto,
                 "--json", out / f"mac-{proto}.json"], stdout=subprocess.DEVNULL)
    startup_rows("mac", out / "mac", args.copies)


def startup_linux(args, out):
    host, rdir = args.linux_host, "her-ratchet"
    run(["scp", "-q", "-o", "BatchMode=yes", args.linux_base, f"{host}:{rdir}/her-rb-base"])
    run(["scp", "-q", "-o", "BatchMode=yes", args.linux_cand, f"{host}:{rdir}/her-rb-cand"])
    run(["scp", "-q", "-o", "BatchMode=yes", args.repo / "tools/bench/bench_startup.py",
         args.repo / "tools/bench/bench_protocol_startup.py", f"{host}:{rdir}/"])
    clones = " && ".join(f"mkdir copies/{label} && cp her-rb-{build} copies/{label}/her"
                         for build, label in copy_labels(args.copies))
    common = (f"{' '.join(her_args('copies', args.copies))} --launcher ./startup-launcher "
              f"--runs {args.linux_runs} --warmup {STARTUP_WARMUP}")
    protocols = " && ".join(f"python3 bench_protocol_startup.py {common} --protocol {proto} "
                            f"--json results/rb-{proto}.json >/dev/null" for proto in PROTOCOLS)
    # sync keeps the writeback of the new files out of the measurements on a small host.
    remote = (f"cd ~/{rdir} && chmod +x her-rb-* && rm -rf copies && mkdir copies && {clones} && sync && "
              f"python3 bench_startup.py {common} --json results/rb-cli.json >/dev/null && "
              f"{protocols} && rm -rf copies")
    run(["ssh", "-o", "BatchMode=yes", host, remote])
    for name in ("cli", *PROTOCOLS):
        run(["scp", "-q", "-o", "BatchMode=yes", f"{host}:{rdir}/results/rb-{name}.json",
             out / f"linux-rb-{name}.json"])
    startup_rows("linux", out / "linux-rb", args.copies)


LOAD_MODES = {
    # Realistic waits, all sessions on one connection (a desktop window with many chats).
    "realistic": ["--sessions", "200", "--concurrency", "200", "--repeats", "1", "--ttft-ms", "100",
                  "--chunk-ms", "1", "--tool-ms", "10"],
    # No waits, each session on its own connection (many clients), three waves.
    "saturated": ["--sessions", "200", "--concurrency", "200", "--repeats", "3", "--connection-per-session"],
}


def load(args, out):
    for mode, extra in LOAD_MODES.items():
        rows = {"base": [], "cand": []}
        order = ["base", "cand", "cand", "base"] * math.ceil(args.load_runs / 2)
        for i, label in enumerate(order[: args.load_runs * 2]):
            j = out / f"load-{mode}-{i}-{label}.json"
            binary = args.base if label == "base" else args.cand
            r = subprocess.run([args.sim, "--her", binary, "--protocol", "native", "--scenario", "macro", *extra,
                                "--json", j], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            d = json.loads(j.read_text()) if j.exists() else None
            if not d:
                rows[label].append(None)
                continue
            m = d["protocols"][0]["metrics"]
            waves = m["waves"]
            rows[label].append(dict(failed=0 if d["passed"] else 1,
                                    task_p50=m["task_ms"]["p50"], task_p95=m["task_ms"]["p95"],
                                    task_p99=m["task_ms"]["p99"], new_p50=m["session_new_ms"]["p50"],
                                    new_p95=m["session_new_ms"]["p95"], tps=m["turns_per_second"],
                                    cpu=m["memory"]["sampled_cpu_ms"], peak=m["memory"]["peak_footprint_mib"],
                                    close_fp=waves[-1]["after_close"]["footprint_mib"],
                                    close_fds=waves[-1]["after_close"]["fds"]))
        for key in ("task_p50", "task_p95", "task_p99", "new_p50", "new_p95", "tps", "cpu", "peak", "close_fp",
                    "close_fds"):
            b = [r[key] for r in rows["base"] if r]
            c = [r[key] for r in rows["cand"] if r]
            compare(f"load {mode}", key, b, c, higher_is_better=(key == "tps"))
        fails = (sum(r["failed"] if r else 1 for r in rows["base"]), sum(r["failed"] if r else 1 for r in rows["cand"]))
        ROWS.append(dict(group=f"load {mode}", metric="failed runs", base=fails[0], cand=fails[1], change_pct=0, p=1,
                         verdict="REGRESSION" if fails[1] > fails[0] else "~", n=(len(rows["base"]), len(rows["cand"]))))


def correctness(args, out):
    j = out / "suite.json"
    r = subprocess.run([args.sim, "--her", args.cand, "--protocol", "all", "--scenario", "suite", "--sessions", "2",
                        "--context-window", "16000", "--json", j], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)
    ok = r.returncode == 0 and json.loads(j.read_text()).get("passed")
    ROWS.append(dict(group="correctness", metric="suite (4 protocols)", base="-", cand="pass" if ok else "FAIL",
                     change_pct=0, p=1, verdict="~" if ok else "REGRESSION", n=(0, 1)))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", type=pathlib.Path, required=True)
    ap.add_argument("--base", type=pathlib.Path, required=True)
    ap.add_argument("--cand", type=pathlib.Path, required=True)
    ap.add_argument("--sim", type=pathlib.Path)
    ap.add_argument("--linux-base", type=pathlib.Path)
    ap.add_argument("--linux-cand", type=pathlib.Path)
    ap.add_argument("--linux-host", help="ssh destination for the Linux start-up comparison")
    ap.add_argument("--out", type=pathlib.Path, required=True)
    ap.add_argument("--copies", type=int, default=6, help="fresh copies of each build for the start-up metrics")
    ap.add_argument("--startup-runs", type=int, default=100, help="start-up runs on each copy")
    ap.add_argument("--linux-runs", type=int, default=100, help="Linux start-up runs on each copy")
    ap.add_argument("--load-runs", type=int, default=6)
    ap.add_argument("--only", nargs="*", default=["mac", "linux", "load", "suite"])
    args = ap.parse_args()
    if args.copies < 4:
        ap.error("--copies must be at least 4: with fewer, p cannot reach 0.05 and nothing could count as a regression")
    args.sim = args.sim or args.repo / "target/release/her-sim"
    args.out.mkdir(parents=True, exist_ok=True)
    started = time.time()
    if "suite" in args.only:
        correctness(args, args.out)
    if "mac" in args.only:
        startup_mac(args, args.out)
    if "linux" in args.only and args.linux_host and args.linux_base and args.linux_cand:
        startup_linux(args, args.out)
    if "load" in args.only:
        load(args, args.out)
    (args.out / "ratchet.json").write_text(json.dumps(ROWS, indent=1))
    print(f"\n{'group':<16}{'metric':<26}{'base':>10}{'cand':>10}{'change':>9}{'p':>8}  verdict")
    for r in ROWS:
        fmt = lambda v: f"{v:10.3f}" if isinstance(v, float) else f"{v!s:>10}"
        print(f"{r['group']:<16}{r['metric']:<26}{fmt(r['base'])}{fmt(r['cand'])}{r['change_pct']:8.1f}%{r['p']:8.3f}  {r['verdict']}")
    regressions = [r for r in ROWS if r["verdict"] == "REGRESSION"]
    print(f"\n{len(regressions)} regression(s), {sum(r['verdict'] == 'better' for r in ROWS)} improvement(s), "
          f"{time.time() - started:.0f}s")


if __name__ == "__main__":
    main()
