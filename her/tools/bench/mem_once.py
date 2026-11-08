#!/usr/bin/env python3
"""Memory of one her launch: peak RSS (both OSes), peak physical footprint (macOS, via
/usr/bin/time -l) and, for services, what is still mapped after one task (Linux smaps_rollup).

  python3 tools/bench/mem_once.py --her PATH [--runs 15] [--memrun target/memrun]
Needs bench_startup.py and bench_protocol_startup.py alongside (local mock model, no network).
"""
import argparse, json, os, pathlib, platform, re, socket, statistics, subprocess, sys, tempfile, time

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from bench_startup import Mock, prepare  # noqa: E402
from bench_protocol_startup import Peer, task  # noqa: E402

MAC = platform.system() == "Darwin"
TIME_RE = {
    "max_rss": re.compile(rb"(\d+)\s+maximum resident set size"),
    "peak_footprint": re.compile(rb"(\d+)\s+peak memory footprint"),
}


MEMRUN = "target/memrun"  # Linux only; see memrun.c


def spawn(cmd, env, cwd, stdin=subprocess.DEVNULL):
    # A wrapper reports the child's peak: on Linux a child forked from Python would otherwise
    # carry Python's own pre-exec RSS in ru_maxrss.
    cmd = ["/usr/bin/time", "-l", *cmd] if MAC else [MEMRUN, *cmd]
    return subprocess.Popen(cmd, env=env, cwd=cwd, stdin=stdin, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, bufsize=0)


def finish(proc, out=b"", err=b""):
    """Reap the child and return its memory, in MiB."""
    out += proc.stdout.read() or b""
    err += proc.stderr.read() or b""
    _, status, usage = os.wait4(proc.pid, 0)
    proc.returncode = os.waitstatus_to_exitcode(status)
    if proc.returncode:
        raise RuntimeError(f"exit {proc.returncode}: {err.decode(errors='replace')[-800:]}")
    if MAC:
        values = {k: int(r.search(err).group(1)) / 2**20 for k, r in TIME_RE.items()}
    else:
        values = {"max_rss": json.loads(err.rsplit(b"MEMRUN ", 1)[1])["max_rss_kib"] / 1024}
    return values


def smaps(pid):
    """Linux: resident memory of a live process, split into shared (the binary's pages) and private."""
    try:
        text = pathlib.Path(f"/proc/{pid}/smaps_rollup").read_text()
    except OSError:
        return {}
    fields = {}
    for line in text.splitlines()[1:]:
        name, _, rest = line.partition(":")
        if rest.strip().endswith("kB"):
            fields[name] = int(rest.split()[0]) / 1024
    return {"rss_now": fields.get("Rss"), "private_dirty_now": fields.get("Private_Dirty"),
            "anonymous_now": fields.get("Anonymous")}


def child_pid(proc):
    """The her process: on macOS it is the child of /usr/bin/time."""
    out = subprocess.run(["pgrep", "-P", str(proc.pid)], capture_output=True, text=True).stdout.split()
    return int(out[0]) if out else None


def service(her, protocol, env, cwd, mock, sock):
    cmd = [her, "acp"] if protocol == "acp" else [her, "serve", "--listen", f"unix://{sock}"]
    proc = spawn(cmd, env, cwd, stdin=subprocess.PIPE)
    conn = None
    try:
        if protocol == "socket":
            deadline = time.monotonic() + 30
            while True:
                conn = socket.socket(socket.AF_UNIX)
                try:
                    conn.connect(str(sock))
                    break
                except (FileNotFoundError, ConnectionRefusedError):
                    conn.close()
                    conn = None
                    if time.monotonic() > deadline:
                        raise RuntimeError("socket not ready")
                    time.sleep(0.0005)
            peer = Peer(conn.fileno(), conn.fileno(), 30)
        else:
            peer = Peer(proc.stdout.fileno(), proc.stdin.fileno(), 30)
        init = {"protocolVersion": 1, "clientCapabilities": {}} if protocol == "acp" else {
            "protocol_version": 2, "client_info": {"name": "mem-once"}}
        peer.rpc("initialize", init)
        task(peer, protocol, cwd, mock)
        live = smaps(child_pid(proc)) if not MAC else {}
        if protocol == "socket":
            peer.rpc("server/shutdown", {})
        proc.stdin.close()
        values = finish(proc, err=b"")
        values.update(live)
        return values
    finally:
        if conn:
            conn.close()
        if proc.returncode is None:
            proc.kill()
        sock.unlink(missing_ok=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--her", required=True)
    ap.add_argument("--runs", type=int, default=15)
    ap.add_argument("--json")
    ap.add_argument("--memrun", default="target/memrun")
    a = ap.parse_args()
    global MEMRUN
    MEMRUN = str(pathlib.Path(a.memrun).resolve())
    her = str(pathlib.Path(a.her).resolve())
    mock = Mock()
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="mem-once-", dir="/tmp"))
    env, proj = prepare(tmp / "case", mock.port, config='model = "mock/bench"\npermission_mode = "manual"\n')
    sock = tmp / "s.sock"
    cases = {
        "/usr/bin/true": lambda: finish(spawn(["/usr/bin/true"], env, proj)),
        "her --version": lambda: finish(spawn([her, "--version"], env, proj)),
        "her status": lambda: finish(spawn([her, "status"], env, proj)),
        "her exec (one task)": lambda: finish(spawn(
            [her, "exec", "-m", "mock/bench", "--permission-mode", "manual", "hello"], env, proj)),
        "her acp (init + one task)": lambda: service(her, "acp", env, proj, mock, sock),
        "her serve (init + one task)": lambda: service(her, "socket", env, proj, mock, sock),
    }
    for _ in range(2):  # warm the file cache and the session folders
        for run in cases.values():
            run()
    results = {name: [run() for _ in range(a.runs)] for name, run in cases.items()}
    report = {"platform": platform.platform(), "machine": platform.machine(), "runs": a.runs,
              "binary": her, "medians_mib": {}}
    keys = ["max_rss", "peak_footprint", "rss_now", "private_dirty_now", "anonymous_now"]
    print(f"{'case':<32}" + "".join(f"{k:>16}" for k in keys if any(k in r[0] for r in results.values())))
    for name, rows in results.items():
        med = {k: statistics.median(r[k] for r in rows) for k in keys if k in rows[0] and rows[0][k] is not None}
        report["medians_mib"][name] = med
        print(f"{name:<32}" + "".join(f"{med[k]:16.2f}" if k in med else f"{'':>16}"
                                       for k in keys if any(k in r[0] for r in results.values())))
    if a.json:
        pathlib.Path(a.json).write_text(json.dumps(report, indent=1))


if __name__ == "__main__":
    main()
