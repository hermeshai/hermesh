#!/usr/bin/env python3
"""Small, serial Terminal-Bench runner; subscription auth never enters job metadata."""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
STATE = HERE / ".state"
AUTH = STATE / "auth"
DATASET = "terminal-bench/terminal-bench@4.0.0"


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=["login", "models", "smoke", "oracle", "pilot", "full", "view"])
    p.add_argument("--model", help="model ID returned by models; defaults to gpt-6-luna if available")
    p.add_argument("--sandbox", default="docker")
    p.add_argument("--task", help="task name/glob for a pinned pilot or oracle run")
    p.add_argument("--thinking", default="low")
    p.add_argument("--attempts", type=int, default=1)
    p.add_argument("--max-turns", type=int, help="Override smoke=4/pilot=0/full=0 (0 means unlimited)")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--force-build", action="store_true", help="Build official task Dockerfiles instead of pulling prebuilt images")
    args = p.parse_args()
    if args.max_turns is not None and args.max_turns < 0:
        p.error("--max-turns must be nonnegative")
    if args.attempts < 1:
        p.error("--attempts must be positive")
    AUTH.mkdir(parents=True, exist_ok=True, mode=0o700)
    native = REPO / "target/debug/her"
    env = {**os.environ, "HER_HOME": str(AUTH), "HER_CREDENTIAL_STORE": "file"}
    if args.action in ("login", "models"):
        cmd = [str(native), "models"]
        cmd += ["login", "chatgpt-api"] if args.action == "login" else ["--json", "--all"]
        if not native.exists():
            p.error("Build the host CLI first: cargo build -p her-cli --bin her")
    elif args.action == "view":
        cmd = ["harbor", "view", str(HERE / "jobs")]
    else:
        cmd = ["harbor", "run", "-n", "1", "-k", str(args.attempts), "-r", "0",
               "-e", args.sandbox, "-o", str(HERE / "jobs")]
        if args.force_build:
            cmd += ["--force-build"]
        if args.action == "smoke":
            cmd += ["-p", str(HERE / "smoke_task")]
        else:
            cmd += ["-d", DATASET]
            if args.task:
                cmd += ["-i", args.task if "/" in args.task else "terminal-bench/" + args.task]
            elif args.action != "full":
                cmd += ["-l", "1"]
        if args.action == "oracle":
            cmd += ["-a", "oracle"]
        else:
            binary = STATE / "bin/her"
            if not binary.exists() and not args.dry_run:
                p.error("Build the Linux binary first: bash build.sh")
            profile = AUTH / "connections/chatgpt/profile.json"
            if not profile.exists():
                if not args.dry_run:
                    p.error("Run: uv run python run.py login")
                model = "local-chatgpt-api-<installation>/" + (args.model or "gpt-6-luna")
            else:
                status = json.loads(subprocess.check_output(
                    [str(native), "models", "status", "chatgpt-api"], env=env))
                connection = status.get("connection") or {}
                if status.get("state") != "connected" or connection.get("state") != "ready":
                    p.error("ChatGPT connection is not ready; run login")
                chosen = args.model or "gpt-6-luna"
                available = {m["id"] for m in connection.get("models", [])}
                if chosen not in available:
                    p.error("Choose --model from: " + ", ".join(sorted(available)))
                model = connection["provider_id"] + "/" + chosen
            cmd += ["-a", "her_agent:HerAgent", "-m", model,
                    "--skip-model-parsing-and-provider-mapping",
                    "--ak", "binary_path=" + str(binary), "--ak", "auth_home=" + str(AUTH),
                    "--ak", "thinking=" + args.thinking, "--ak", "max_turns=" + (str(args.max_turns) if args.max_turns is not None else "4" if args.action == "smoke" else "0")]
    print(shlex.join(cmd), flush=True)
    if not args.dry_run:
        # import-path agents load from this directory, including when run from repo root.
        env["PYTHONPATH"] = str(HERE) + os.pathsep + env.get("PYTHONPATH", "")
        raise SystemExit(subprocess.call(cmd, cwd=HERE, env=env))


if __name__ == "__main__":
    main()
