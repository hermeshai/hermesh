#!/usr/bin/env python3
"""Memory of one launch of fx and of her against the real DeepSeek API, measured the same
way: peak RSS on both OSes (Linux through memrun.c, which keeps Python's pre-exec RSS out),
plus peak physical footprint on macOS (/usr/bin/time -l). Runs alternate between the tools.

  DEEPSEEK_API_KEY=... python3 tools/bench/mem_real.py --fx FX --her HER [--runs 8] [--local-runs 15]

The key comes from the environment and is only passed on to the two processes; nothing here
writes it anywhere. Both use the model deepseek-flash at https://api.deepseek.com.
"""
import argparse, json, os, pathlib, platform, re, select, statistics, subprocess, sys, tempfile, time

MAC = platform.system() == "Darwin"
MEMRUN = "target/memrun"  # Linux only; see memrun.c
PROMPT = "Reply with exactly one word: ok"
TIME_RE = {"max_rss": re.compile(rb"(\d+)\s+maximum resident set size"),
           "peak_footprint": re.compile(rb"(\d+)\s+peak memory footprint")}


def wrap(cmd):
    return ["/usr/bin/time", "-l", *cmd] if MAC else [MEMRUN, *cmd]


def memory(err):
    if MAC:
        return {k: int(r.search(err).group(1)) / 2**20 for k, r in TIME_RE.items()}
    return {"max_rss": json.loads(err.rsplit(b"MEMRUN ", 1)[1])["max_rss_kib"] / 1024}


def run(cmd, env, cwd, expect=None, timeout=180):
    p = subprocess.run(wrap(cmd), env=env, cwd=cwd, stdin=subprocess.DEVNULL, capture_output=True, timeout=timeout)
    if p.returncode != 0:
        raise RuntimeError(f"{cmd[0]} {cmd[1]} exit {p.returncode}: {p.stderr.decode(errors='replace')[-600:]}")
    if expect and expect not in p.stdout.decode(errors="replace").lower():
        raise RuntimeError(f"unexpected output from {cmd[1]}: {p.stdout[-300:]!r}")
    return memory(p.stderr)


class Acp:
    """Minimal ACP client over the process's stdio."""

    def __init__(self, cmd, env, cwd):
        self.p = subprocess.Popen(wrap(cmd), env=env, cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, bufsize=0)
        self.buf, self.seq = b"", 0

    def send(self, method, params):
        self.seq += 1
        self.p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": self.seq, "method": method, "params": params}).encode() + b"\n")
        return self.seq

    def reply(self, rid, text, timeout=180):
        deadline = time.monotonic() + timeout
        while True:
            while b"\n" not in self.buf:
                left = deadline - time.monotonic()
                if left <= 0 or not select.select([self.p.stdout], [], [], left)[0]:
                    raise TimeoutError("ACP reply")
                chunk = os.read(self.p.stdout.fileno(), 65536)
                if not chunk:
                    raise RuntimeError("ACP stream closed: " + self.p.stderr.read().decode(errors="replace")[-600:])
                self.buf += chunk
            line, _, self.buf = self.buf.partition(b"\n")
            msg = json.loads(line)
            if "method" in msg and "id" in msg:  # a request from the agent, e.g. permission: refuse
                self.p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": msg["id"], "error": {"code": -32601, "message": "no"}}).encode() + b"\n")
                continue
            if "method" in msg:
                update = msg.get("params", {}).get("update", {})
                if update.get("sessionUpdate") == "agent_message_chunk":
                    text.append(update.get("content", {}).get("text", ""))
                continue
            if msg.get("id") == rid:
                if "error" in msg:
                    raise RuntimeError(f"ACP error: {msg['error']}")
                return msg["result"]

    def task(self, cwd):
        self.reply(self.send("initialize", {"protocolVersion": 1, "clientCapabilities": {}}), [])
        session = self.reply(self.send("session/new", {"cwd": str(cwd), "mcpServers": []}), [])
        text = []
        self.reply(self.send("session/prompt", {"sessionId": session["sessionId"],
                                                "prompt": [{"type": "text", "text": PROMPT}]}), text)
        if "ok" not in "".join(text).lower():
            raise RuntimeError(f"unexpected ACP answer: {''.join(text)[:200]!r}")
        live = {} if MAC else self.live()
        self.p.stdin.close()
        err = self.p.stderr.read()
        self.p.stdout.read()
        self.p.wait(timeout=30)
        return {**memory(err), **live}

    def live(self):
        """Linux: the agent's resident memory after the task, before it exits."""
        pid = subprocess.run(["pgrep", "-P", str(self.p.pid)], capture_output=True, text=True).stdout.split()[0]
        fields = {}
        for line in pathlib.Path(f"/proc/{pid}/smaps_rollup").read_text().splitlines()[1:]:
            name, _, rest = line.partition(":")
            if rest.strip().endswith("kB"):
                fields[name] = int(rest.split()[0]) / 1024
        return {"rss_after_task": fields["Rss"], "anonymous_after_task": fields["Anonymous"],
                "private_dirty_after_task": fields["Private_Dirty"]}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fx", required=True)
    ap.add_argument("--her", required=True)
    ap.add_argument("--runs", type=int, default=8)
    ap.add_argument("--local-runs", type=int, default=15)
    ap.add_argument("--json")
    ap.add_argument("--memrun", default="target/memrun")
    ap.add_argument("--only", nargs="*", default=["local", "task", "acp"])
    a = ap.parse_args()
    global MEMRUN
    MEMRUN = str(pathlib.Path(a.memrun).resolve())
    if not os.environ.get("DEEPSEEK_API_KEY"):
        sys.exit("DEEPSEEK_API_KEY is not set")
    fx, her = str(pathlib.Path(a.fx).resolve()), str(pathlib.Path(a.her).resolve())
    root = pathlib.Path(tempfile.mkdtemp(prefix="mem-real-", dir="/tmp"))
    home, proj = root / "home", root / "proj"
    (proj / ".git").mkdir(parents=True)
    (proj / ".git" / "HEAD").write_text("ref: refs/heads/main\n")
    (proj / "AGENTS.md").write_text("Use 4-space indentation.\n")
    home.mkdir()
    # fx 0.0.16 reads only $HOME/.fx (FX_HOME came later).
    fx_home, her_home = home / ".fx", root / "herhome"
    fx_home.mkdir()
    her_home.mkdir()
    (fx_home / "settings.json").write_text(json.dumps({
        "provider": "deepseek",
        "providers": {"deepseek": {"protocol": "openai-chat-completions", "base_url": "https://api.deepseek.com",
                                   "auth": {"type": "bearer", "env": "DEEPSEEK_API_KEY"}}},
        "models": {"deepseek": "deepseek-flash"}}))
    (her_home / "config.toml").write_text('model = "deepseek/deepseek-flash"\npermission_mode = "manual"\n')
    base = {k: v for k, v in os.environ.items() if k in ("PATH", "LANG", "TMPDIR", "DEEPSEEK_API_KEY", "TERM")}
    base.update(HOME=str(home), NO_COLOR="1")
    fx_env = dict(base, FX_AUTO_UPGRADE="0")
    her_env = dict(base, HER_HOME=str(her_home), HER_CREDENTIAL_STORE="file")
    cases = {}
    if "local" in a.only:
        cases.update({
            "fx --version": (a.local_runs, lambda: run([fx, "--version"], fx_env, proj)),
            "her --version": (a.local_runs, lambda: run([her, "--version"], her_env, proj)),
            "fx status --json": (a.local_runs, lambda: run([fx, "status", "--json"], fx_env, proj)),
            "her status --json": (a.local_runs, lambda: run([her, "status", "--json"], her_env, proj)),
        })
    if "task" in a.only:
        cases.update({
            "fx ask (one DeepSeek task)": (a.runs, lambda: run([fx, "ask", PROMPT], fx_env, proj, expect="ok")),
            "her exec (one DeepSeek task)": (a.runs, lambda: run([her, "exec", PROMPT], her_env, proj, expect="ok")),
        })
    if "acp" in a.only:
        cases.update({
            "fx acp (init + one task)": (a.runs, lambda: Acp([fx, "acp"], fx_env, proj).task(proj)),
            "her acp (init + one task)": (a.runs, lambda: Acp([her, "acp"], her_env, proj).task(proj)),
        })
    for _, (_, fn) in cases.items():  # warm-up, and a check that every case works
        fn()
    results = {name: [] for name in cases}
    rounds = max(n for n, _ in cases.values())
    for i in range(rounds):
        for name, (n, fn) in cases.items():
            if i < n:
                results[name].append(fn())
    keys = ["max_rss", "peak_footprint"] if MAC else ["max_rss", "rss_after_task", "anonymous_after_task", "private_dirty_after_task"]
    report = {"platform": platform.platform(), "runs": a.runs, "local_runs": a.local_runs, "medians_mib": {}}
    print(f"{'case':<34}" + "".join(f"{k:>16}" for k in keys) + f"{'min-max rss':>16}")
    for name, rows in results.items():
        med = {k: statistics.median(r[k] for r in rows) for k in keys if k in rows[0]}
        lo, hi = min(r["max_rss"] for r in rows), max(r["max_rss"] for r in rows)
        report["medians_mib"][name] = med
        print(f"{name:<34}" + "".join(f"{med[k]:16.2f}" if k in med else f"{'':>16}" for k in keys) + f"{lo:8.1f}-{hi:<7.1f}")
    if a.json:
        pathlib.Path(a.json).write_text(json.dumps(report, indent=1))


if __name__ == "__main__":
    main()
