#!/usr/bin/env python3
"""Everything one `her acp` process allocates through a mock task, freed or not, by call site
(macOS). Live-heap views miss short-lived allocations, yet those set the footprint: the
allocator keeps their pages. Needs MallocStackLogging=full; plain =1 records live blocks only.

   python3 tools/bench/mem_alloc_events.py BINARY [--top 30] [--save EVENTS.txt]
"""
import argparse, collections, pathlib, re, subprocess, sys, tempfile, time

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from bench_startup import Mock, prepare  # noqa: E402
from bench_protocol_startup import Peer, task  # noqa: E402

ALLOC = re.compile(r"ALLOC\s+(0x[0-9a-f]+)-0x[0-9a-f]+\s+\[size=(\d+)\]:\s*(.*)")
FREE = re.compile(r"FREE\s+(0x[0-9a-f]+):")
GENERIC = re.compile(r"_R[A-Za-z0-9_]*?(5alloc|4core|3std|9hashbrown|10serde_json|5serde|10serde_core|12futures_util|5bytes)[0-9A-Z]|__rust|RawVec")
ALLOCATOR_LIBS = {"dyld", "libdyld.dylib", "libsystem_pthread.dylib", "libsystem_malloc.dylib", "libsystem_c.dylib"}


def record(binary):
    mock = Mock()
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="alloc-events-", dir="/tmp"))
    env, proj = prepare(tmp / "c", mock.port, config='model = "mock/bench"\npermission_mode = "manual"\n')
    env["MallocStackLogging"] = "full"
    p = subprocess.Popen([binary, "acp"], env=env, cwd=proj, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, bufsize=0)
    peer = Peer(p.stdout.fileno(), p.stdin.fileno(), 30)
    peer.rpc("initialize", {"protocolVersion": 1, "clientCapabilities": {}})
    task(peer, "acp", proj, mock)
    time.sleep(0.3)
    out = subprocess.run(["malloc_history", str(p.pid), "-allEvents"], capture_output=True, text=True).stdout
    p.stdin.close()
    p.wait(timeout=10)
    return out


def symbols(addrs, binary, load):
    found = {}
    addrs = sorted(addrs)
    for i in range(0, len(addrs), 250):
        chunk = addrs[i:i + 250]
        res = subprocess.run(["atos", "-o", binary, "-l", load, *chunk], capture_output=True, text=True).stdout.splitlines()
        for addr, sym in zip(chunk, res):
            found[addr] = re.sub(r"::h[0-9a-f]{16}", "", re.sub(r"\s*\(in [^)]*\)", "", sym))
    return found


def readable(sym):
    parts = [p for p in re.findall(r"\d+([a-z_][a-z0-9_]{2,})", sym) if p not in ("new", "call", "poll", "run", "her")]
    return "::".join(parts[-3:]) if parts else sym[:50]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("binary")
    ap.add_argument("--top", type=int, default=30)
    ap.add_argument("--save")
    a = ap.parse_args()
    binary = str(pathlib.Path(a.binary).resolve())
    text = record(binary)
    if a.save:
        pathlib.Path(a.save).write_text(text)
    load = re.search(r"Load Address:\s+(0x[0-9a-f]+)", text).group(1)
    name = pathlib.Path(binary).name
    frame = re.compile(r"(0x[0-9a-f]+) \(" + re.escape(name) + r"\)")
    blocks, live = [], {}
    for line in text.splitlines():
        if m := ALLOC.match(line):
            live[m.group(1)] = len(blocks)
            blocks.append([int(m.group(2)), m.group(3), False])
        elif (m := FREE.match(line)) and m.group(1) in live:
            blocks[live.pop(m.group(1))][2] = True
    syms = symbols({x for _, s, _ in blocks for x in frame.findall(s)}, binary, load)

    def site(stack):
        names = [readable(syms.get(x, "?")) for x in frame.findall(stack) if not GENERIC.search(syms.get(x, "?"))]
        if names:
            return " < ".join(reversed(names[-3:]))
        libs = re.findall(r"\(([^)]+)\)\s*(\S+)", stack)
        for lib, fn in libs:
            if lib not in ALLOCATOR_LIBS and lib != name:
                return f"[{lib}] {fn[:60]}"
        return "[system]"

    total = sum(b[0] for b in blocks)
    freed = sum(b[0] for b in blocks if b[2])
    print(f"allocated {total / 1024:.0f} KiB in {len(blocks)} blocks; freed {freed / 1024:.0f} KiB; live {(total - freed) / 1024:.0f} KiB")
    agg = collections.defaultdict(lambda: [0, 0, 0])
    for size, stack, was_freed in blocks:
        row = agg[site(stack)]
        row[0] += size
        row[1] += 1
        row[2] += size if was_freed else 0
    print(f"{'KiB':>8} {'freed':>7} {'count':>6}  site")
    for key, (size, count, gone) in sorted(agg.items(), key=lambda kv: -kv[1][0])[:a.top]:
        print(f"{size / 1024:8.1f} {gone / 1024:7.1f} {count:6d}  {key[:200]}")


if __name__ == "__main__":
    main()
