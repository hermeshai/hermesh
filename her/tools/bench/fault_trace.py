#!/usr/bin/env python3
"""Where one fresh process takes its page faults: every fault of one exec, sorted into the section of
the binary it hit, or the stack or other anonymous memory.

Linux, as root: it reads the kernel's page-fault tracepoints, so nothing else needs installing. The
fixture is bench_cli.py's, and one run before the traced one warms it. A total a few faults below the
budget's number is expected: those come from the spawn, before exec. Address randomization is off for
the traced run, so runs line up.

  python3 tools/bench/fault_trace.py target/x86_64-unknown-linux-musl/dist/her status --json
"""
import argparse
import collections
import ctypes
import os
from pathlib import Path
import re
import struct
import subprocess
import tempfile

from bench_cli import write_sessions
from bench_startup import prepare

TRACING = Path("/sys/kernel/tracing" if os.path.exists("/sys/kernel/tracing/trace") else "/sys/kernel/debug/tracing")
EVENTS = ("exceptions/page_fault_user", "exceptions/page_fault_kernel", "sched/sched_process_exec")
FAULT = re.compile(r"page_fault_(user|kernel): address=(0x[0-9a-f]+) ip=(\S+) error_code=(0x[0-9a-f]+)")
ADDR_NO_RANDOMIZE = 0x0040000
STACK_BELOW = 0x7ffffffff000 - (8 << 20)  # x86-64, no randomization, the default stack limit


def sections(path):
    """Entry point and allocated sections of an ELF64 file."""
    data = path.read_bytes()
    entry, shoff = struct.unpack_from("<Q", data, 24)[0], struct.unpack_from("<Q", data, 40)[0]
    size, count, names = struct.unpack_from("<HHH", data, 58)
    headers = [struct.unpack_from("<IIQQQQ", data, shoff + i * size) for i in range(count)]
    table = headers[names][4]
    found = []
    for name, _, flags, addr, _, length in headers:
        if flags & 2 and length:
            found.append((addr, addr + length, data[table + name:data.index(b"\0", table + name)].decode()))
    return entry, sorted(found)


def trace(binary, arguments, env, cwd):
    """The faults of one exec, in order, as (mode, address, ip, error code), and its ru_minflt."""
    (TRACING / "tracing_on").write_text("0")
    (TRACING / "trace").write_text("")
    for event in EVENTS:
        (TRACING / "events" / event / "enable").write_text("1")
    ready, go = os.pipe()
    child = os.fork()
    if child == 0:
        os.close(go)
        ctypes.CDLL(None).personality(ADDR_NO_RANDOMIZE)
        (TRACING / "set_event_pid").write_text(str(os.getpid()))
        os.read(ready, 1)
        os.chdir(cwd)
        null = os.open(os.devnull, os.O_RDWR)
        for fd in (0, 1, 2):
            os.dup2(null, fd)
        os.execve(binary, [str(binary), *arguments], env)
    os.close(ready)
    (TRACING / "tracing_on").write_text("1")
    os.close(go)
    _, status, usage = os.wait4(child, 0)
    (TRACING / "tracing_on").write_text("0")
    text = (TRACING / "trace").read_text()
    for event in EVENTS:
        (TRACING / "events" / event / "enable").write_text("0")
    (TRACING / "set_event_pid").write_text("")
    if status:
        raise SystemExit(f"{binary} {arguments}: wait status {status}")
    faults = []
    for line in text[text.index("sched_process_exec"):].splitlines():
        if match := FAULT.search(line):
            mode, address, ip, error = match.groups()
            faults.append((mode, int(address, 16), int(ip, 16) if ip.startswith("0x") else 0, int(error, 16)))
    return faults, usage.ru_minflt


def report(binary, arguments, env, cwd):
    entry, table = sections(binary)
    faults, total = trace(binary, arguments, env, cwd)
    base = next(ip for mode, _, ip, _ in faults if mode == "user") - entry  # the first instruction faults
    low, high = table[0][0], table[-1][1]
    groups = collections.defaultdict(collections.Counter)
    for mode, address, _, error in faults:
        offset = address - base
        where = next((name for start, end, name in table if start <= offset < end), None)
        if where is None:
            where = "(between sections)" if low <= offset < high else "(stack)" if address >= STACK_BELOW else "(anonymous)"
        kind = "fetch" if error & 16 else "write" if error & 2 else "read"
        groups[where][f"{kind} {'write-protected' if error & 1 else 'not present'}{' from the kernel' if mode == 'kernel' else ''}"] += 1
    print(f"{binary.name} {' '.join(arguments)}: {len(faults)} faults after exec, {total} with the fork before it")
    for where, kinds in sorted(groups.items(), key=lambda item: -sum(item[1].values())):
        print(f"{sum(kinds.values()):6} {where:<20} " + ", ".join(f"{count} {kind}" for kind, count in kinds.most_common()))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("binary", type=Path)
    parser.add_argument("arguments", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="fault-trace-") as temporary:
        env, cwd = prepare(Path(temporary).resolve() / "fixture", 1, config='model = "mock/bench"\npermission_mode = "manual"\n')
        write_sessions(Path(env["HER_HOME"]), cwd, 8, 8 * 1024 * 1024)
        binary = args.binary.resolve()
        subprocess.run([binary, *args.arguments], env=env, cwd=cwd, stdin=subprocess.DEVNULL, capture_output=True, check=True)
        report(binary, args.arguments, env, cwd)


if __name__ == "__main__":
    main()
