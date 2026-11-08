#!/usr/bin/env python3
"""Where a process spends non-idle thread time, from a macOS `sample PID 3 -file out.txt`.
Self time per function, inclusive time per her function, and the her callers of a leaf.
Samples count blocked syscalls too: a thread waiting in fcntl(F_FULLFSYNC) is not burning CPU.

   python3 tools/bench/sample_hotspots.py out.txt [--leaf __fcntl] [--top 25]

Sample a process that is busy: run the load with --settle-ms 0 and pgrep -n, or an orphan from
an earlier run gets sampled and the call graph comes back empty. sample pauses the target.
"""
import argparse, collections, re

LINE = re.compile(r"^(?P<indent>[ +!:|]*)(?P<count>\d+) (?P<rest>.*)$")
IDLE = ("__psynch_cvwait", "kevent", "semaphore_wait_trap", "__ulock_wait", "mach_msg", "__workq_kernreturn",
        "swtch_pri", "__psynch_mutexwait", "__semwait")


def nodes(path):
    found, inside = [], False
    for raw in open(path, errors="replace"):
        if raw.startswith("Call graph:"):
            inside = True
        elif inside and raw.startswith(("Total number", "Sort by")):
            break
        elif inside and (m := LINE.match(raw.rstrip("\n"))):
            found.append((len(m.group("indent")), int(m.group("count")), re.sub(r"\s+\(in [^)]*\).*$", "", m.group("rest")).strip()))
    return found


def short(sym):
    if not sym.startswith("_R"):
        return sym[:90]
    parts = [p for p in re.findall(r"\d+([A-Za-z_][A-Za-z0-9_]{2,})", sym) if not re.match(r"^(Cs[0-9A-Za-z]{8,}|Nt|Nv|Ns|Ni)", p)]
    return "::".join(parts[-4:])[:110]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("sample")
    ap.add_argument("--leaf", help="also list the her callers of this exact leaf symbol, e.g. __fcntl")
    ap.add_argument("--top", type=int, default=25)
    a = ap.parse_args()
    tree = nodes(a.sample)
    self_time, inclusive, callers, waits, stack = collections.Counter(), collections.Counter(), collections.Counter(), 0, []
    for i, (depth, count, sym) in enumerate(tree):
        while stack and stack[-1][0] >= depth:
            stack.pop()
        stack.append((depth, sym))
        children, j, child_depth = 0, i + 1, None
        while j < len(tree) and tree[j][0] > depth:
            child_depth = child_depth or tree[j][0]
            if tree[j][0] == child_depth:
                children += tree[j][1]
            j += 1
        own = count - children
        if own <= 0:
            continue
        chain = [short(s) for _, s in stack if "her" in s]
        if a.leaf and sym == a.leaf:
            callers[" < ".join(reversed(chain[-4:]))] += own
        if "__psynch_mutexwait" in sym:
            waits += own
        if any(x in sym for x in IDLE):
            continue
        self_time[short(sym)] += own
        for name in set(chain):
            inclusive[name] += own
    total = sum(self_time.values())
    print(f"non-idle samples {total}; lock waits {waits}")
    if not total:
        raise SystemExit("no busy samples: was the process doing anything?")
    print("-- self")
    for key, value in self_time.most_common(a.top):
        print(f"{value:7d} {value / total * 100:5.1f}%  {key}")
    print("-- inclusive, her functions")
    for key, value in inclusive.most_common(a.top):
        print(f"{value:7d} {value / total * 100:5.1f}%  {key}")
    if a.leaf:
        print(f"-- callers of {a.leaf}")
        for key, value in callers.most_common(a.top):
            print(f"{value:7d}  {key[:300]}")


if __name__ == "__main__":
    main()
