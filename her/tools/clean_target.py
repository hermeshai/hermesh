#!/usr/bin/env python3
"""Free disk from a cargo target dir without racing a build.

A dev target dir fills up with two regenerable leftovers; this removes those and nothing else:

  target/debug/incremental       rustc's incremental cache
  target/debug/deps/*.rcgu.o     loose object files that macOS keeps for the debugger
                                 (split-debuginfo = "unpacked")

The two are hard links of each other, so deleting only one of them frees a fraction of what `du`
shows for it: both have to go. The next build regenerates what it needs; crates that are not
rebuilt keep working, but their frames lose file:line in a RUST_BACKTRACE until they are.

It takes cargo's own build-directory locks (.cargo-build-lock, .cargo-artifact-lock, .cargo-lock)
with a non-blocking flock. A running cargo holds them, so the script waits; while the script holds
them, a cargo that starts prints "Blocking waiting for file lock" instead of compiling into a
half-cleaned directory. Test binaries that are already running are not affected.

  python3 tools/clean_target.py [--wait SECONDS] [TARGET_DIR ...]

TARGET_DIR defaults to $CARGO_TARGET_DIR, then to this checkout's target/. macOS and Linux only.
"""

import argparse
import fcntl
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

LOCK_NAMES = (".cargo-build-lock", ".cargo-artifact-lock", ".cargo-lock")
COMPILERS = ("rustc", "rustdoc", "clippy-driver", "cargo", "cargo-clippy", "rust-analyzer")
# Cargo writes this signature into CACHEDIR.TAG at the root of every target dir it creates.
CACHEDIR_SIGNATURE = "Signature: 8a477f597d28d172789f06886806bc55"


def log(message):
    print(f"{time.strftime('%H:%M:%S')} {message}", flush=True)


def free_gib(path):
    return shutil.disk_usage(path).free / 2**30


def compilers_for(target):
    """Command lines of compiler-like processes whose arguments mention this target dir."""
    out = subprocess.run(["ps", "-axww", "-o", "pid=,command="], capture_output=True, text=True).stdout
    hits = []
    for line in out.splitlines():
        parts = line.split(None, 1)
        if len(parts) < 2:
            continue
        command = parts[1]
        if os.path.basename(command.split(" ", 1)[0]) in COMPILERS and (str(target) + "/") in command:
            hits.append(line.strip()[:160])
    return hits


def acquire(paths, deadline):
    """Exclusive non-blocking flock on every cargo lock file; the open fds, or None on timeout."""
    while True:
        fds = []
        try:
            for path in paths:
                fd = os.open(path, os.O_RDWR)
                fds.append(fd)
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fds
        except BlockingIOError:
            for fd in fds:
                os.close(fd)  # closing drops the flock; never wait while holding part of the set
            if time.time() >= deadline:
                return None
            time.sleep(0.4)


def is_cargo_target(target):
    tag = target / "CACHEDIR.TAG"
    try:
        signed = tag.read_text(errors="replace").startswith(CACHEDIR_SIGNATURE)
    except OSError:
        return False
    return signed and (target / "debug" / "deps").is_dir()


def clean(target, wait):
    started = time.time()
    debug = target / "debug"
    deps = debug / "deps"
    incremental = debug / "incremental"
    if not is_cargo_target(target):
        log(f"{target}: refused, not a cargo target dir")
        return
    locks = [debug / name for name in LOCK_NAMES if (debug / name).exists()]
    if not locks:
        log(f"{target}: refused, no cargo lock files in {debug}")
        return
    before = free_gib(target)
    names = [entry.name for entry in os.scandir(deps) if entry.name.endswith(".rcgu.o")]
    log(f"{target}: {len(names)} loose object files; waiting for cargo's build locks (up to {int(wait)}s)")
    deadline = started + wait
    while True:
        try:
            fds = acquire(locks, deadline)
        except FileNotFoundError:
            log(f"{target}: skipped, a cargo lock file disappeared")
            return
        if fds is None:
            log(f"{target}: skipped, a cargo build held the locks for {int(wait)}s")
            return
        busy = compilers_for(target)
        if not busy:
            break
        for fd in fds:
            os.close(fd)
        if time.time() >= deadline:
            log(f"{target}: skipped, a compiler is still running: {busy[0]}")
            return
        time.sleep(1)
    trash = None
    try:
        held = time.time()
        log(f"{target}: locks held after {held - started:.0f}s, cleaning")
        if incremental.is_dir():
            trash = incremental.with_name(f"incremental.deleting.{os.getpid()}")
            os.rename(incremental, trash)  # out of cargo's way at once; removed after the locks are released
        gone = 0
        for name in names:
            try:
                os.unlink(deps / name)
                gone += 1
            except FileNotFoundError:
                pass
            if gone and gone % 250000 == 0:
                log(f"{target}: {gone} object files unlinked")
        log(f"{target}: unlinked {gone} object files in {time.time() - held:.0f}s; releasing the locks")
    finally:
        for fd in fds:
            os.close(fd)
    if trash:
        subprocess.run(["rm", "-rf", "--", str(trash)], check=False)
    log(f"{target}: done in {time.time() - started:.0f}s, {before:.1f} -> {free_gib(target):.1f} GiB free")


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--wait", type=float, default=600, help="longest wait for a running build, in seconds (default 600)")
    parser.add_argument("targets", nargs="*", help="cargo target dirs (default: $CARGO_TARGET_DIR, else this checkout's target/)")
    args = parser.parse_args()
    default = os.environ.get("CARGO_TARGET_DIR") or Path(__file__).resolve().parents[1] / "target"
    for target in args.targets or [default]:
        clean(Path(os.path.realpath(target)), args.wait)


if __name__ == "__main__":
    sys.exit(main())
