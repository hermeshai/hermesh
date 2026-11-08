#!/usr/bin/env python3
"""Memory of `<agent> acp` after each stage of one real DeepSeek task: started, initialize,
session/new, prompt answered. macOS: physical footprint (vmmap); Linux: anonymous and RSS.

  DEEPSEEK_API_KEY=... python3 tools/bench/mem_stages.py --bin PATH [--repeat 3]

The second agent in the mem_real.py comparison works too: both are configured.
"""
import argparse, json, os, pathlib, platform, re, statistics, subprocess, sys, tempfile, time

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import mem_real  # noqa: E402

MAC = platform.system() == "Darwin"


def snapshot(pid):
    if MAC:
        out = subprocess.run(["vmmap", "--summary", str(pid)], capture_output=True, text=True).stdout
        m = re.search(r"Physical footprint:\s+([\d.]+)([KMG])", out)
        value = float(m.group(1)) * {"K": 1 / 1024, "M": 1, "G": 1024}[m.group(2)]
        return {"footprint": value}
    fields = {}
    for line in pathlib.Path(f"/proc/{pid}/smaps_rollup").read_text().splitlines()[1:]:
        name, _, rest = line.partition(":")
        if rest.strip().endswith("kB"):
            fields[name] = int(rest.split()[0]) / 1024
    threads = len(os.listdir(f"/proc/{pid}/task"))
    return {"anonymous": fields["Anonymous"], "rss": fields["Rss"], "threads": threads}


def once(binary, root):
    home, proj = root / "home", root / "proj"
    env = {k: v for k, v in os.environ.items() if k in ("PATH", "LANG", "TMPDIR", "DEEPSEEK_API_KEY")}
    env.update(HOME=str(home), NO_COLOR="1", HER_HOME=str(root / "herhome"), HER_CREDENTIAL_STORE="file",
               FX_AUTO_UPGRADE="0")
    p = subprocess.Popen([binary, "acp"], env=env, cwd=proj, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, bufsize=0)
    acp = mem_real.Acp.__new__(mem_real.Acp)
    acp.p, acp.buf, acp.seq = p, b"", 0
    stages = {}
    time.sleep(0.3)
    stages["started"] = snapshot(p.pid)
    acp.reply(acp.send("initialize", {"protocolVersion": 1, "clientCapabilities": {}}), [])
    stages["initialize"] = snapshot(p.pid)
    session = acp.reply(acp.send("session/new", {"cwd": str(proj), "mcpServers": []}), [])
    stages["session/new"] = snapshot(p.pid)
    text = []
    acp.reply(acp.send("session/prompt", {"sessionId": session["sessionId"],
                                          "prompt": [{"type": "text", "text": mem_real.PROMPT}]}), text)
    time.sleep(0.3)
    stages["task done"] = snapshot(p.pid)
    p.stdin.close()
    p.wait(timeout=30)
    return stages


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", required=True)
    ap.add_argument("--repeat", type=int, default=3)
    a = ap.parse_args()
    root = pathlib.Path(tempfile.mkdtemp(prefix="stage-probe-", dir="/tmp"))
    (root / "proj" / ".git").mkdir(parents=True)
    (root / "proj" / ".git" / "HEAD").write_text("ref: refs/heads/main\n")
    (root / "proj" / "AGENTS.md").write_text("Use 4-space indentation.\n")
    (root / "home" / ".fx").mkdir(parents=True)
    (root / "home" / ".fx" / "settings.json").write_text(json.dumps({
        "provider": "deepseek",
        "providers": {"deepseek": {"protocol": "openai-chat-completions", "base_url": "https://api.deepseek.com",
                                   "auth": {"type": "bearer", "env": "DEEPSEEK_API_KEY"}}},
        "models": {"deepseek": "deepseek-flash"}}))
    (root / "herhome").mkdir()
    (root / "herhome" / "config.toml").write_text('model = "deepseek/deepseek-flash"\npermission_mode = "manual"\n')
    runs = [once(str(pathlib.Path(a.bin).resolve()), root) for _ in range(a.repeat)]
    for stage in runs[0]:
        vals = {k: statistics.median(r[stage][k] for r in runs) for k in runs[0][stage]}
        print(f"{stage:<14}" + "  ".join(f"{k} {v:7.2f}" for k, v in vals.items()))


if __name__ == "__main__":
    main()
