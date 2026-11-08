#!/usr/bin/env python3
"""Real-model acceptance of the her CLI, in a clean home (standalone-cli-design §1.3, §16.2).

    BAILIAN_TOKEN_PLAN_API_KEY=… python3 tools/acceptance/cli_acceptance.py target/debug/her

Spends a few thousand tokens. The key stays in this process's environment and is never printed.
Each scenario prints PASS or FAIL with what it saw; the exit code is the number of failures.
"""

import json
import os
import pty
import re
import select
import shutil
import subprocess
import sys
import tempfile
import time

HER = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else "target/debug/her")
MODEL = os.environ.get("ACCEPTANCE_MODEL", "bailian-token-plan/deepseek-v4.1-flash")
KEY_NAME = "BAILIAN_TOKEN_PLAN_API_KEY"
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
KEY = os.environ.get(KEY_NAME)
if not KEY:
    sys.exit(f"{KEY_NAME} is not set")

failures = 0


def report(name, ok, detail=""):
    global failures
    failures += 0 if ok else 1
    print(f"{'PASS' if ok else 'FAIL'}  {name}{'  ' + detail if detail else ''}", flush=True)


class Env:
    """A home and a project of their own, with nothing inherited but PATH."""

    def __init__(self, with_key=True):
        self.dir = tempfile.mkdtemp(prefix="her-accept-")
        self.home = os.path.join(self.dir, "home")
        self.work = os.path.join(self.dir, "work")
        os.makedirs(self.home)
        os.makedirs(self.work)
        subprocess.run(["git", "init", "-q", self.work], check=True)
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": self.home,
            "HER_HOME": os.path.join(self.home, ".her"),
            "HER_CREDENTIAL_STORE": "file",
            "HER_NO_BROWSER": "1",
            "HER_LANG": "en",
            "TERM": "xterm-256color",
        }
        if with_key:
            self.env[KEY_NAME] = KEY
            self.env["HER_MODEL"] = MODEL

    def run(self, *args, input=None, timeout=240):
        return subprocess.run([HER, *args], cwd=self.work, env=self.env, input=input, capture_output=True, text=True, timeout=timeout)

    def path(self, name):
        return os.path.join(self.work, name)

    def clean(self):
        shutil.rmtree(self.dir, ignore_errors=True)


ANSI = re.compile(r"\x1b\[[0-9;?<>]*[A-Za-z]")


class Terminal:
    """her on a pseudo-terminal."""

    def __init__(self, env, *args):
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(env.work)
            os.execve(HER, [HER, *args], env.env)
        self.seen = ""

    def pump(self, seconds=0.1):
        ready, _, _ = select.select([self.fd], [], [], seconds)
        if ready:
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                return False
            if not data:
                return False
            self.seen += ANSI.sub("", data.decode("utf8", "replace"))
        return True

    def wait_for(self, text, timeout=180, after=0):
        deadline = time.time() + timeout
        while time.time() < deadline:
            at = self.seen.find(text, after)
            if at >= 0:
                return at + len(text)
            if not self.pump():
                break
        raise TimeoutError(f"no {text!r} in: …{self.seen[-1500:]}")

    def type(self, text):
        os.write(self.fd, text.encode())

    def close(self):
        try:
            os.kill(self.pid, 9)
            os.waitpid(self.pid, 0)
        except OSError:
            pass

    def wait_exit(self, timeout=20):
        deadline = time.time() + timeout
        while time.time() < deadline:
            self.pump(0.05)
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                return os.waitstatus_to_exitcode(status)
        raise TimeoutError("still running")


def s2_first_run():
    env = Env(with_key=False)
    out = env.run("exec", "--json", "hello")
    failure = json.loads(out.stdout)
    report("S2 no model: exit 2, says her login", out.returncode == 2 and failure["error"]["kind"] == "no_model" and "her login" in failure["error"]["help"])
    out = env.run("login", "bailian-token-plan", "--key-stdin", "--model", MODEL, input=KEY)
    report("S2 her login with the real key", out.returncode == 0 and KEY not in out.stdout + out.stderr, (out.stdout + out.stderr).strip()[-120:])
    out = env.run("login", "bailian-token-plan", "--key-stdin", input="sk-not-a-real-key")
    report("S2 a wrong key is refused", out.returncode == 2 and "sk-not-a-real-key" not in out.stdout + out.stderr, (out.stdout + out.stderr).strip()[-100:])
    out = env.run("exec", "--json", "Reply with the single word: ready")
    result = json.loads(out.stdout)
    report("S2 a task runs on the saved key", out.returncode == 0 and "ready" in result["final_output"].lower(), result["final_output"][:60])
    out = env.run("logout", "bailian-token-plan")
    env.clean()


def s3_interactive():
    env = Env()
    with open(env.path("calc.py"), "w") as f:
        f.write("def add(a, b):\n    return a - b\n")
    term = Terminal(env)
    try:
        term.wait_for("› ", 30)
        term.type("calc.py has a bug in add. Fix it with the edit tool. Do not run anything.\r")
        # However it goes about it, the edit asks, with what changes.
        term.wait_for("[y] yes")
        shown = "- " in term.seen and "+ " in term.seen
        report("S3 the question shows the change", shown)
        term.type("y\r")
        term.wait_for("tokens:", 240)
        fixed = "a + b" in open(env.path("calc.py")).read()
        report("S3 the edit happened after yes", fixed)
        at = len(term.seen)
        term.type("Now reply with exactly the word 中文\r")
        term.wait_for("中文", 120, at)
        term.wait_for("tokens:", 120, at)
        report("S3 a second message, wide characters", True)
        at = len(term.seen)
        term.type("/rename acceptance\r")
        term.wait_for("renamed", 10, at)
        term.type("/quit\r")
        report("S3 quits with 0", term.wait_exit() == 0 and "her resume" in term.seen)
    except TimeoutError as err:
        report("S3 interactive", False, str(err)[-400:])
    finally:
        term.close()
    term = Terminal(env, "resume")
    try:
        term.wait_for("› ", 30)
        report("S3 resume shows the conversation", "calc.py has a bug" in term.seen)
        at = len(term.seen)
        term.type("Which file did we just fix? Reply with only its name.\r")
        term.wait_for("calc.py", 120, at)
        report("S3 the conversation goes on", True)
        term.type("/quit\r")
        term.wait_exit()
    except TimeoutError as err:
        report("S3 resume", False, str(err)[-400:])
    finally:
        term.close()
    env.clean()


def s4_scripts():
    env = Env()
    out = env.run("exec", "--json", "Create a file named made.txt containing hi, with the write tool.")
    result = json.loads(out.stdout)
    report("S4 refused writes exit 3", out.returncode == 3 and result["refused"] and not os.path.exists(env.path("made.txt")), f"exit {out.returncode}")
    out = env.run("exec", "--json", "--permission-mode", "accept-edits", "Create a file named made.txt containing hi, with the write tool.")
    report("S4 with a mode it runs", out.returncode == 0 and os.path.exists(env.path("made.txt")), f"exit {out.returncode}")
    out = env.run("exec", "--output", "stream-json", "Reply with the word done.")
    kinds = [json.loads(line)["type"] for line in out.stdout.splitlines()]
    report("S4 the event stream", out.returncode == 0 and "task_completed" in kinds)
    out = env.run("exec", "--resume", "last", "--json", "What word did you just reply with? Reply with only that word.")
    report("S4 exec --resume", out.returncode == 0 and "done" in json.loads(out.stdout)["final_output"].lower())
    env.clean()


def s5_acp():
    env = Env()
    server = subprocess.Popen([HER, "acp"], cwd=env.work, env=env.env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    next_id = [0]

    def send(method, params):
        next_id[0] += 1
        server.stdin.write(json.dumps({"jsonrpc": "2.0", "id": next_id[0], "method": method, "params": params}) + "\n")
        server.stdin.flush()
        return next_id[0]

    def until_answer(wanted):
        chunks = []
        while True:
            message = json.loads(server.stdout.readline())
            if message.get("id") == wanted and "method" not in message:
                return message, chunks
            if message.get("method") == "session/update":
                update = message["params"]["update"]
                if update.get("sessionUpdate") == "agent_message_chunk":
                    chunks.append(update["content"].get("text", ""))
            elif message.get("method") == "session/request_permission":
                options = message["params"]["options"]
                allow = next(o for o in options if o["kind"].startswith("allow"))
                server.stdin.write(json.dumps({"jsonrpc": "2.0", "id": message["id"], "result": {"outcome": {"outcome": "selected", "optionId": allow["optionId"]}}}) + "\n")
                server.stdin.flush()

    init, _ = until_answer(send("initialize", {"protocolVersion": 1, "clientCapabilities": {"auth": {"terminal": True}}, "clientInfo": {"name": "acceptance", "version": "1"}}))
    report("S5 ACP offers ways to sign in", len(init["result"]["authMethods"]) == 3)
    created, _ = until_answer(send("session/new", {"cwd": env.work, "mcpServers": []}))
    session = created["result"]["sessionId"]
    names = [o["name"] for o in created["result"].get("configOptions", [])]
    report("S5 ACP options in English", any(n == "Model" for n in names), str(names))
    answer, chunks = until_answer(send("session/prompt", {"sessionId": session, "prompt": [{"type": "text", "text": "Reply with the single word: ready"}]}))
    report("S5 ACP runs a task", "ready" in "".join(chunks).lower(), "".join(chunks)[:50])
    answer, chunks = until_answer(send("session/prompt", {"sessionId": session, "prompt": [{"type": "text", "text": "Create note.txt containing hi with the write tool, then say done."}]}))
    report("S5 ACP asks and writes", os.path.exists(env.path("note.txt")), str(answer.get("result")))
    server.stdin.close()
    server.terminate()
    env.clean()


def s6_programs():
    env = Env()
    for runner, script in (("python3", "minimal.py"), ("node", "minimal.mjs")):
        if not shutil.which(runner):
            report(f"S6 {script}", True, f"no {runner} here")
            continue
        out = subprocess.run([runner, os.path.join(ROOT, "examples/rpc", script), "Reply with the single word: ready", HER], cwd=env.work, env=env.env, capture_output=True, text=True, timeout=240)
        report(f"S6 {script}", out.returncode == 0 and "ready" in out.stdout.lower(), out.stdout.strip()[:50] or out.stderr[-200:])
    env.clean()


def s8_models():
    env = Env()
    out = env.run("models", "check", MODEL, "--json")
    try:
        result = json.loads(out.stdout)
    except ValueError:
        result = {}
    report("S8 her models check", out.returncode == 0, json.dumps(result, ensure_ascii=False)[:400] or out.stderr[-300:])
    out = env.run("doctor", "--online")
    report("S8 her doctor --online", out.returncode == 0)
    # The free services limit how often they answer; one of them is enough to show it works.
    outs = [env.run("web-search", "check", service, "--json") for service in ("exa", "parallel")]
    report("S8 a real web search", any(out.returncode == 0 for out in outs), " ".join(out.stdout.strip()[:90] for out in outs))
    env.clean()


def s9_pdf():
    env = Env()
    # A minimal PDF: the point is what happens without PDF support.
    with open(env.path("report.pdf"), "wb") as f:
        f.write(b"%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Count 0/Kids[]>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")
    out = env.run("doctor", "--json")
    pdf = next((c for c in json.loads(out.stdout)["checks"] if c["name"] == "pdf component"), {})
    missing = "not installed" in pdf.get("detail", "")
    if not missing:
        report("S9 PDF component", True, f"present here: {pdf.get('detail')}; run without it to see the rest")
    else:
        out = env.run("exec", "--json", "--permission-mode", "accept-edits", "Read report.pdf with the document tools and tell me what is in it.")
        reply = json.loads(out.stdout)["final_output"]
        report("S9 without PDF support the model says so", "her install pdf" in reply or "install" in reply.lower() or "pdf" in reply.lower(), reply[:200])
    env.clean()


for scenario in (s2_first_run, s3_interactive, s4_scripts, s5_acp, s6_programs, s8_models, s9_pdf):
    print(f"\n{scenario.__name__}", flush=True)
    try:
        scenario()
    except Exception as err:  # A scenario that blows up is a failed one, and the others still run.
        report(scenario.__name__, False, repr(err)[:400])

print(f"\n{failures} failure(s)")
sys.exit(failures)
