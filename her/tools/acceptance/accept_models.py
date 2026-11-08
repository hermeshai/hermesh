#!/usr/bin/env python3
"""Acceptance check for model lists and the public catalog with a real provider
(docs/kernel/model-catalog-design.md §13.3).

Runs `her serve --stdio` in a temporary HER_HOME and, through the native protocol:

  1. a made-up key is refused by DeepSeek and not saved (`key_rejected`);
  2. the real key is checked against DeepSeek's model list and saved (`verified`);
  3. `models/list` holds exactly the ids DeepSeek's own `GET /models` returns, the catalog
     describes them, and the menu shows them;
  4. `models/refresh` with `catalog: true` takes the public catalog from models.dev;
  5. `her exec` answers with every model DeepSeek lists.

    DEEPSEEK_API_KEY=... python3 tools/acceptance/accept_models.py --her target/release/her

Uses a temporary HER_HOME and HOME, and a file instead of the keychain. Network traffic:
DeepSeek's model list and chat completions, and the public catalog.
"""

import argparse
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import urllib.request


class Rpc:
    def __init__(self, cmd, env):
        self.proc = subprocess.Popen(cmd, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        self.next = 0

    def call(self, method, params=None):
        self.next += 1
        msg = {"jsonrpc": "2.0", "id": self.next, "method": method}
        if params is not None:
            msg["params"] = params
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()
        while True:
            line = self.proc.stdout.readline()
            if not line:
                sys.exit(f"her closed the connection during {method}")
            reply = json.loads(line)
            if reply.get("id") == self.next:
                return reply

    def close(self):
        self.proc.stdin.close()
        self.proc.wait(timeout=30)


def check(ok, what):
    print(("PASS " if ok else "FAIL ") + what)
    if not ok:
        sys.exit(1)


def deepseek_ids(key):
    request = urllib.request.Request("https://api.deepseek.com/models", headers={"Authorization": f"Bearer {key}"})
    with urllib.request.urlopen(request, timeout=20) as response:
        return [m["id"] for m in json.load(response)["data"]]


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--her", default="target/release/her")
    args = parser.parse_args()
    her = str(pathlib.Path(args.her).resolve())
    key = os.environ.get("DEEPSEEK_API_KEY")
    if not key:
        sys.exit("set DEEPSEEK_API_KEY")
    official = deepseek_ids(key)
    print(f"DeepSeek lists: {', '.join(official)}")

    with tempfile.TemporaryDirectory(prefix="her-models-") as tmp:
        home = pathlib.Path(tmp, "her")
        (pathlib.Path(tmp, "user")).mkdir()
        home.mkdir()
        env = {k: v for k, v in os.environ.items() if not k.endswith("_API_KEY")}
        env.update(HER_HOME=str(home), HOME=str(pathlib.Path(tmp, "user")), HER_CREDENTIAL_STORE="file")
        rpc = Rpc([her, "serve", "--stdio"], env)
        rpc.call("initialize", {"protocol_version": 2, "client_info": {"name": "accept-models"}})

        bad = rpc.call("auth/set", {"provider": "deepseek", "api_key": "sk-her-acceptance-not-a-key"})
        check(bad.get("error", {}).get("code") == -32021, f"a made-up key is refused: {bad.get('error', {}).get('message')}")
        check(not (home / "credentials.toml").exists(), "the refused key is not saved")

        good = rpc.call("auth/set", {"provider": "deepseek", "api_key": key})
        check(good.get("result", {}).get("verified") is True, f"the real key is checked and saved: {good.get('result')}")

        models = rpc.call("models/list", {})["result"]
        provider = next(p for p in models["providers"] if p["id"] == "deepseek")
        check(provider["listing"]["source"] == "provider", "DeepSeek's models come from its own list")
        listed = [m for m in models["models"] if m["provider"] == "deepseek"]
        ids = {m["id"].split("/", 1)[1] for m in listed}
        check(set(official) <= ids, f"every model DeepSeek lists is there: {sorted(ids)}")
        for m in listed:
            if m["id"].split("/", 1)[1] in official:
                print(f"     {m['id']}: {m['name']}, window {m['context_window']}, levels {m['thinking_levels']}, "
                      f"known {m['known']}, shown {m['shown']}, cost {m.get('cost')}")
        check(all(m["known"] for m in listed if m["id"].split("/", 1)[1] in official), "the catalog describes them")
        check(any(m["shown"] for m in listed), "the menu shows them")
        check(key not in json.dumps(models), "the key never leaves the service")

        refreshed = rpc.call("models/refresh", {"provider": "deepseek", "catalog": True})
        check("result" in refreshed, "a manual refresh succeeds")
        state = json.loads((home / "models" / "catalog" / "state.json").read_text())
        check(bool(state.get("checked_at")), f"the public catalog was fetched: {state}")
        rpc.close()

        for model in official:
            proc = subprocess.run(
                [her, "exec", "--output", "json", "-m", f"deepseek/{model}", "Reply with the single word OK."],
                cwd=tmp, env=env, capture_output=True, text=True, timeout=300,
            )
            result = json.loads(proc.stdout) if proc.stdout.strip().startswith("{") else {}
            check(proc.returncode == 0, f"deepseek/{model} answers: {str(result.get('final_output') or proc.stderr)[:80]!r}")
    print("all checks passed")


if __name__ == "__main__":
    main()
