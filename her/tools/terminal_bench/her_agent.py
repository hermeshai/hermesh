"""Run the real her CLI inside Harbor's task environment (Harbor 0.25.0)."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import tempfile
import tomllib
import certifi

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.models.agent.context import AgentContext


class HerAgent(BaseInstalledAgent):
    def __init__(self, *, binary_path, auth_home=None, models_path=None,
                 thinking="low", max_turns=0, **kwargs):
        super().__init__(**kwargs)
        self.binary = Path(binary_path).resolve(strict=True)
        self.binary_sha256 = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.auth_home = Path(auth_home).resolve(strict=True) if auth_home else None
        self.models_path = Path(models_path).resolve(strict=True) if models_path else None
        if bool(self.auth_home) == bool(self.models_path):
            raise ValueError("Specify exactly one of auth_home (ChatGPT) or models_path (test/API)")
        self.thinking = thinking
        self.max_turns = int(max_turns)
        if self.max_turns < 0:
            raise ValueError("max_turns must be nonnegative")
        # Keep the path identical: her keys subscription credentials by HER_HOME hash.
        self.remote_home = str(self.auth_home) if self.auth_home else "/tmp/her-benchmark-home"
        if not self.model_name:
            raise ValueError("An explicit her provider/model is required")

    @staticmethod
    def name():
        return "her"

    def get_version_command(self):
        return "/installed-agent/her --version"

    async def install(self, environment):
        await environment.upload_file(self.binary, "/installed-agent/her")
        await environment.upload_file(certifi.where(), "/installed-agent/ca.pem")
        await self.exec_as_root(environment, "chmod 755 /installed-agent/her && chmod 644 /installed-agent/ca.pem")
        await self.exec_as_agent(environment, "/installed-agent/her --version")

    @with_prompt_template
    async def run(self, instruction, environment, context: AgentContext):
        lock = None
        if self.auth_home:
            lock = (self.auth_home / "benchmark.lock").open("a")
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                lock.close()
                raise RuntimeError("ChatGPT evaluation requires -n 1; another trial owns this login")
        try:
            remote = self.remote_home + "/connections/chatgpt"
            await self.exec_as_root(environment, f"umask 022; mkdir -p {shlex.quote(remote)}; chmod 700 {shlex.quote(self.remote_home)} {shlex.quote(remote)}")
            if environment.default_user is not None:
                await self.exec_as_root(environment, "chown -R " + shlex.quote(str(environment.default_user)) + " " + shlex.quote(self.remote_home))
            if self.auth_home:
                for name in ("profile.json", "tokens.toml"):
                    source = self.auth_home / "connections/chatgpt" / name
                    if not source.is_file():
                        raise RuntimeError("Run run.py login first (dedicated file-store login required)")
                    await self._upload_config_text(environment, content=source.read_text(), remote_path=remote + "/" + name, filename=name)
                    await self.exec_as_root(environment, f"chmod 600 {shlex.quote(remote + '/' + name)}")
            else:
                await self._upload_config_text(environment, content=self.models_path.read_text(), remote_path=self.remote_home + "/models.toml", filename="models.toml")
            logs = str(self.environment_logs_dir)
            with tempfile.TemporaryDirectory() as tmp:
                prompt = Path(tmp) / "instruction.txt"
                prompt.write_text(instruction)
                await environment.upload_file(prompt, "/installed-agent/instruction.txt")
                await self.exec_as_root(environment, "chmod 644 /installed-agent/instruction.txt")
            argv = ["/installed-agent/her", "exec", "-", "--model", self.model_name,
                    "--thinking", self.thinking, "--permission-mode", "bypass-permissions",
                    "--output", "json", "--max-turns", str(self.max_turns)]
            command = (shlex.join(argv) + " < /installed-agent/instruction.txt > "
                       + shlex.quote(logs + "/her-result.json") + " 2> "
                       + shlex.quote(logs + "/her-stderr.log"))
            try:
                await self.exec_as_agent(environment, command, env={
                    "HER_HOME": self.remote_home, "HER_CREDENTIAL_STORE": "file",
                    "SSL_CERT_FILE": "/installed-agent/ca.pem"})
            finally:
                # Refresh tokens rotate. Persist them even if execution fails; never log them.
                if self.auth_home:
                    for name in ("tokens.toml", "profile.json"):
                        with tempfile.TemporaryDirectory(dir=self.auth_home) as tmp:
                            dest = Path(tmp) / name
                            await environment.download_file(remote + "/" + name, dest)
                            if name.endswith("toml"):
                                tomllib.loads(dest.read_text())
                            else:
                                json.loads(dest.read_text())
                            dest.chmod(0o600)
                            os.replace(dest, self.auth_home / "connections/chatgpt" / name)
                # Only sessions are collected; credential files stay outside logs.
                await self.exec_as_agent(environment, "if [ -d " + shlex.quote(self.remote_home + "/sessions") + " ]; then cp -R " + shlex.quote(self.remote_home + "/sessions") + " " + shlex.quote(logs + "/sessions") + "; fi")
        finally:
            if lock:
                lock.close()

    def populate_context_post_run(self, context):
        path = self.logs_dir / "her-result.json"
        if not path.exists() or not path.stat().st_size:
            return
        result = json.loads(path.read_text())
        totals = [result.get("usage", {})] + [a.get("usage", {}) for a in result.get("agents", [])]
        cache = sum(u.get("cache_read_tokens", 0) for u in totals)
        context.n_cache_tokens = cache
        context.n_input_tokens = cache + sum(u.get("input_tokens", 0) + u.get("cache_write_tokens", 0) for u in totals)
        context.n_output_tokens = sum(u.get("output_tokens", 0) for u in totals)
        # Subscription quota has no meaningful per-run API dollar price.
        context.metadata = {"binary_sha256": self.binary_sha256, "her_model": result.get("model"), "her_exit_code": result.get("exit_code"),
                            "turns": result.get("turns"), "max_turns": self.max_turns, "thinking": self.thinking, "billing": "chatgpt-subscription" if self.auth_home else "configured-provider"}
