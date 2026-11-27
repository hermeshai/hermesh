"""Exercise the settings window against two paired macOS daemons in a disposable home."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

if os.uname().sysname != "Darwin" or os.uname().machine != "arm64":
    raise SystemExit("This fixture requires macOS arm64, Node/npm, Go and pnpm.")

with tempfile.TemporaryDirectory(prefix="hm-provider-ui-", dir="/private/tmp") as folder:
    work = Path(folder)
    binaries = work / "bin"
    app = work / "app"
    subprocess.run(["python3", "scripts/build-provider-fixture.py", str(binaries), "--os", "darwin"], check=True)
    subprocess.run(["pnpm", "--dir", "apps/desktop", "build"], check=True,
                   env=dict(os.environ, HERMESH_DESKTOP_OUT=str(app)))
    home = work / "user"
    home.mkdir()
    # Preserve the user's live CLI installations and account data. Electron
    # connects to the test owner's socket and uses the fixture's own profile.
    profile = (f'(version 1)(allow default)(deny file-write*)'
               f'(allow file-write* (subpath "{work}") (subpath "/private/tmp") '
               f'(literal "/dev/null") (literal "/dev/tty"))'
               f'(deny file-read* (subpath "{Path.home()}") (subpath "/Applications/Claude.app") '
               f'(subpath "/Applications/ChatGPT.app") (subpath "/Applications/Codex.app"))'
               f'(deny mach-lookup (global-name "com.apple.securityd"))')
    prefix = work / "owner"
    backend = subprocess.Popen([
        "/usr/bin/sandbox-exec", "-p", profile, str(binaries / "test"), "-test.v",
        "-test.run", "^TestProviderMaintenanceReal$", "-test.timeout", "30m",
    ], cwd=home, env=dict(PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin", HOME=str(home),
                         TMPDIR=str(work), SHELL="/bin/sh", USER="test", HERMESH_PROVIDER_ACCEPTANCE="1",
                         HERMESH_PROVIDER_FIXTURE_BIN=str(binaries),
                         HERMESH_PROVIDER_PREVIOUS=str(binaries / "previous"),
                         HERMESH_PROVIDER_UI_LIFECYCLE=str(prefix)))
    try:
        for stage in ("install", "update"):
            fixture = prefix.with_suffix("." + stage)
            deadline = time.monotonic() + 120
            while not fixture.exists():
                if backend.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError(f"Target failed to reach {stage}")
                time.sleep(0.3)
            target = json.loads(fixture.read_text())["target"]
            subprocess.run([
                "pnpm", "--dir", "apps/desktop", "exec", "playwright", "test",
                "provider-maintenance.spec.ts", "--grep", "install or upgrade",
            ], check=True, env=dict(os.environ, HERMESH_DESKTOP_OUT=str(app),
                                   HERMESH_E2E_FIXTURE=str(fixture), HERMESH_PROVIDER_UI_TARGET=target,
                                   HERMESH_PROVIDER_UI_STAGE=stage, HERMESH_PW_OUT=str(work / "results")))
            Path(str(fixture) + ".done").touch()
        if backend.wait(timeout=60) != 0:
            raise RuntimeError("Target acceptance failed")
    finally:
        if backend.poll() is None:
            # Let the Go harness tear down its daemons through t.Cleanup.
            for stage in ("install", "update"):
                Path(str(prefix) + "." + stage + ".done").touch()
            try:
                backend.wait(timeout=45)
            except subprocess.TimeoutExpired:
                backend.terminate()
                backend.wait(timeout=20)
